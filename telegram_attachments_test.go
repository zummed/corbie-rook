package rook_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// A telegram stands in for Telegram's Bot API, for the bot with the token
// "123:abc".
type telegram struct {
	*httptest.Server
	mu      sync.Mutex
	updates []map[string]any  // each message sent to the bot, in order; its update_id is its place from 1
	heard   int               // the highest offset asked from, below which every update was heard
	sent    []map[string]any  // each message the bot sent, in order
	files   map[string]string // what each file_id downloads
	got     []string          // each file_id getFile was asked for
}

func newTelegram(t *testing.T) *telegram {
	tg := &telegram{files: map[string]string{}}
	tg.Server = httptest.NewServer(http.HandlerFunc(tg.serve))
	t.Cleanup(tg.Close)
	return tg
}

func (tg *telegram) serve(w http.ResponseWriter, r *http.Request) {
	if path, ok := strings.CutPrefix(r.URL.Path, "/file/bot123:abc/files/"); ok {
		tg.mu.Lock()
		body, ok := tg.files[path]
		tg.mu.Unlock()
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, body)
		return
	}
	method, ok := strings.CutPrefix(r.URL.Path, "/bot123:abc/")
	if !ok {
		fmt.Fprint(w, `{"ok": false, "error_code": 401, "description": "Unauthorized"}`)
		return
	}
	var ask map[string]any
	json.NewDecoder(r.Body).Decode(&ask)
	reply := func(result any) { json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result}) }
	switch method {
	case "getUpdates":
		// It waits for an update at most a third of a second, so that the
		// server closes quickly when the test ends.
		offset, _ := ask["offset"].(float64)
		for end := time.Now().Add(min(time.Duration(ask["timeout"].(float64))*time.Second, 300*time.Millisecond)); ; time.Sleep(10 * time.Millisecond) {
			tg.mu.Lock()
			tg.heard = max(tg.heard, int(offset))
			from := max(tg.heard, 1)
			var out []map[string]any
			if from <= len(tg.updates) {
				out = []map[string]any{{"update_id": from, "message": tg.updates[from-1]}}
			}
			tg.mu.Unlock()
			if out != nil || time.Now().After(end) {
				reply(out)
				return
			}
		}
	case "getFile":
		id := ask["file_id"].(string)
		tg.mu.Lock()
		tg.got = append(tg.got, id)
		tg.mu.Unlock()
		reply(map[string]any{"file_id": id, "file_path": "files/" + id})
	case "sendMessage":
		tg.mu.Lock()
		tg.sent = append(tg.sent, ask)
		reply(map[string]any{"message_id": 100 + len(tg.sent)})
		tg.mu.Unlock()
	case "sendChatAction":
		reply(true)
	default:
		fmt.Fprint(w, `{"ok": false, "error_code": 404, "description": "Not Found"}`)
	}
}

// says sends the bot messages from alice, in her chat with it.
func (tg *telegram) says(messages ...map[string]any) {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	for _, m := range messages {
		m["message_id"] = 10 + len(tg.updates)
		m["from"] = map[string]any{"id": 1, "is_bot": false, "first_name": "Alice"}
		m["chat"] = map[string]any{"id": 1, "type": "private"}
		tg.updates = append(tg.updates, m)
	}
}

func (tg *telegram) said() []string {
	tg.mu.Lock()
	defer tg.mu.Unlock()
	var out []string
	for _, m := range tg.sent {
		out = append(out, fmt.Sprint(m["text"]))
	}
	return out
}

// telegramProject makes a project whose Telegram bridge reaches the stand-in,
// and whose helper is the starter's, with a stand-in model that answers
// "seen".
func telegramProject(t *testing.T, w *world, tg *telegram) (string, *llmstandin.Server) {
	t.Helper()
	w.secret("TELEGRAM_TOKEN", "123:abc")
	w.secret("TEST_KEY", "sk-test")
	dir := pointed(t, "telegram", tg.Server, "api.telegram.org")
	if err := os.WriteFile(filepath.Join(dir, "people.yaml"), []byte("people:\n  alice: { telegram: \"1\" }\ngroups: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := &llmstandin.Server{Answer: func(llmstandin.Call) llmstandin.Reply { return llmstandin.Reply{Text: "seen"} }}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	return dir, model
}

func photo(id string, bytes int) []any {
	return []any{
		map[string]any{"file_id": id + "-small", "file_unique_id": "u1", "width": 90, "height": 60, "file_size": 2},
		map[string]any{"file_id": id, "file_unique_id": "u2", "width": 1280, "height": 960, "file_size": bytes},
		map[string]any{"file_id": id + "-medium", "file_unique_id": "u3", "width": 320, "height": 240, "file_size": 4},
	}
}

// A photo with a caption is downloaded when the message is answered, in its
// largest size, and the model is shown it as an image beside the caption. A
// photo with no caption is answered too.
func TestTelegramPhotoIsShownToTheModel(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := telegramProject(t, w, tg)
	cat, dog := "\xff\xd8 a cat", "\xff\xd8 a dog"
	tg.files["P1"], tg.files["P2"] = cat, dog
	tg.says(map[string]any{"caption": "what is this?", "photo": photo("P1", len(cat))})
	w.sync(dir)
	w.until("the answer", func() bool { return len(tg.said()) >= 1 })
	shown := fmt.Sprint(model.Calls[0].Messages)
	for _, want := range []string{"alice asks: what is this?", "photo.jpg", "image/jpeg", base64.StdEncoding.EncodeToString([]byte(cat))} {
		if !strings.Contains(shown, want) {
			t.Errorf("%q is not in %s", want, shown)
		}
	}
	tg.says(map[string]any{"photo": photo("P2", len(dog))})
	w.until("the second answer", func() bool { return len(tg.said()) >= 2 })
	if shown := fmt.Sprint(model.Calls[1].Messages); !strings.Contains(shown, base64.StdEncoding.EncodeToString([]byte(dog))) {
		t.Errorf("the model was shown %s; want the second photo", shown)
	}
	if got := strings.Join(tg.got, " "); got != "P1 P2" {
		t.Errorf("getFile was asked for %s; want the largest of each photo", got)
	}
}

// Telegram gives a bot at most 20 MiB of a file, so a larger one is named to
// the model and not downloaded.
func TestTelegramLargeFileIsNamedAndNotDownloaded(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := telegramProject(t, w, tg)
	tg.says(map[string]any{"document": map[string]any{"file_id": "D1", "file_unique_id": "u", "file_name": "film.mp4", "mime_type": "video/mp4", "file_size": 25 << 20}})
	w.sync(dir)
	w.until("the answer", func() bool { return len(tg.said()) >= 1 })
	shown := fmt.Sprint(model.Calls[0].Messages)
	if !strings.Contains(shown, "film.mp4") || strings.Contains(shown, "blob") || len(tg.got) != 0 {
		t.Errorf("the model was shown %s, and getFile asked for %v; want the file's name and no download", shown, tg.got)
	}
}

// The items of an album are one message, with the caption one of them has.
// The text sent after it is a message of its own, heard once.
func TestTelegramAlbumIsOneMessage(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := telegramProject(t, w, tg)
	one, two := "\xff\xd8 one", "\xff\xd8 two"
	tg.files["A1"], tg.files["A2"] = one, two
	tg.says(
		map[string]any{"media_group_id": "g1", "photo": photo("A1", len(one))},
		map[string]any{"media_group_id": "g1", "caption": "which is better?", "photo": photo("A2", len(two))},
		map[string]any{"text": "thanks"},
	)
	w.sync(dir)
	w.until("two answers", func() bool { return len(tg.said()) >= 2 })
	time.Sleep(10 * interval)
	shown := fmt.Sprint(model.Calls[0].Messages)
	for _, want := range []string{"alice asks: which is better?", base64.StdEncoding.EncodeToString([]byte(one)), base64.StdEncoding.EncodeToString([]byte(two))} {
		if !strings.Contains(shown, want) {
			t.Errorf("%q is not in %s", want, shown)
		}
	}
	if len(model.Calls) != 2 || !strings.Contains(fmt.Sprint(model.Calls[1].Messages), "alice asks: thanks") {
		t.Errorf("the model was asked %d times; want the album, then the text", len(model.Calls))
	}
	w.titled(map[string][]string{"message": {"thanks", "which is better?"}})
}
