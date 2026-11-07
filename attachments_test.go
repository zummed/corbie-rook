package rook_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// A picture sent on Slack is downloaded when the message is answered, and the
// model is shown it as an image beside the message.
func TestSlackPictureIsShownToTheModel(t *testing.T) {
	t.Skip("R3 revives this: Slack")
	s := newSlack(t)
	w := newWorld(t)
	w.secret("TEST_KEY", "sk-test")
	dir := slackProject(t, w, s)
	model := &llmstandin.Server{Answer: func(llmstandin.Call) llmstandin.Reply { return llmstandin.Reply{Text: "a cat"} }}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	picture := "\x89PNG picture"
	s.files = map[string]string{"/files/T1-F1/cat.png": picture}
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "subtype": "file_share", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "3.0", "text": "what is this?",
		"files": []any{map[string]any{"id": "F1", "name": "cat.png", "mimetype": "image/png", "size": len(picture), "url_private_download": s.URL + "/files/T1-F1/cat.png"}}})
	w.until("the answer", func() bool { return len(s.posted()) >= 1 })
	shown := fmt.Sprint(model.Calls[0].Messages)
	for _, want := range []string{"alice asks: what is this?", "cat.png", "image/png", base64.StdEncoding.EncodeToString([]byte(picture))} {
		if !strings.Contains(shown, want) {
			t.Errorf("%q is not in %s", want, shown)
		}
	}
}

// A file sent with no text is a message, and one over the largest size is
// named to the model and not downloaded.
func TestSlackLargeFileIsNamedAndNotDownloaded(t *testing.T) {
	t.Skip("R3 revives this: Slack")
	s := newSlack(t)
	w := newWorld(t)
	w.secret("TEST_KEY", "sk-test")
	dir := slackProject(t, w, s)
	model := &llmstandin.Server{Answer: func(llmstandin.Call) llmstandin.Reply { return llmstandin.Reply{Text: "too big"} }}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "subtype": "file_share", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "3.0", "text": "",
		"files": []any{map[string]any{"id": "F2", "name": "film.mp4", "mimetype": "video/mp4", "size": 64 << 20, "url_private_download": s.URL + "/files/T1-F2/film.mp4"}}})
	w.until("the answer", func() bool { return len(s.posted()) >= 1 })
	shown := fmt.Sprint(model.Calls[0].Messages)
	if !strings.Contains(shown, "film.mp4") || strings.Contains(shown, "blob") {
		t.Errorf("the model was shown %s; want the file's name and no blob", shown)
	}
}

// telegramProject makes a project whose Telegram calls reach the stand-in,
// and whose agent is testdata's helper, with a stand-in model that answers
// "seen".
func telegramProject(t *testing.T, w *world, tg *telegram) (string, *llmstandin.Server) {
	t.Helper()
	w.secret("TELEGRAM_TOKEN", "123:abc")
	w.secret("TEST_KEY", "sk-test")
	dir := pointed(t, "telegram", tg.Server, "api.telegram.org")
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
	t.Skip("R4 revives this: Telegram")
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

// Telegram gives a bot at most 20 MB of a file, so a larger one is named to
// the model and not downloaded.
func TestTelegramLargeFileIsNamedAndNotDownloaded(t *testing.T) {
	t.Skip("R4 revives this: Telegram")
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

// Each item of an album is a message of its own, with its own caption, and
// the text sent after it is heard once.
func TestTelegramAlbumItemsAreMessages(t *testing.T) {
	t.Skip("R4 revives this: Telegram")
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := telegramProject(t, w, tg)
	one, two := "\xff\xd8 one", "\xff\xd8 two"
	tg.files["A1"], tg.files["A2"] = one, two
	tg.says(
		map[string]any{"media_group_id": "g1", "caption": "before", "photo": photo("A1", len(one))},
		map[string]any{"media_group_id": "g1", "caption": "after", "photo": photo("A2", len(two))},
		map[string]any{"text": "thanks"},
	)
	w.sync(dir)
	w.until("three answers", func() bool { return len(tg.said()) >= 3 })
	time.Sleep(10 * interval)
	if len(model.Calls) != 3 {
		t.Fatalf("the model was asked %d times; want each photo, then the text", len(model.Calls))
	}
	// A call holds the conversation so far, so each is read by its last
	// message, and found by its caption.
	one64, two64 := base64.StdEncoding.EncodeToString([]byte(one)), base64.StdEncoding.EncodeToString([]byte(two))
	var shown []string
	for _, c := range model.Calls {
		shown = append(shown, fmt.Sprint(c.Messages[len(c.Messages)-1]))
	}
	for _, want := range [][]string{{"alice asks: before", one64}, {"alice asks: after", two64}, {"alice asks: thanks"}} {
		if !slices.ContainsFunc(shown, func(s string) bool { return strings.Contains(s, want[0]) && strings.Contains(s, want[len(want)-1]) }) {
			t.Errorf("no call shows %q", want)
		}
	}
	for _, s := range shown {
		if strings.Contains(s, one64) && strings.Contains(s, two64) {
			t.Errorf("a call was shown both photos: %s", s)
		}
	}
	w.titled(map[string][]string{"message": {"after", "before", "thanks"}})
}

const secret1 = "029d6592aaaaaaaaaaaaaaaaaaaaaaaa"
const secret2 = "0c2c754ebbbbbbbbbbbbbbbbbbbbbbbb"

// attachment is one of the issue's files, made by author at a time.
func attachment(id, name, kind string, size int, author string, at time.Time) map[string]any {
	return map[string]any{"id": id, "filename": name, "mimeType": kind, "size": size, "created": jiraTime(at), "author": map[string]any{"accountId": author}}
}

// jiraAnswering starts the Jira router on j with a real agent and a stand-in
// model, and gives a function that waits for the next ask.
func jiraAnswering(t *testing.T, j *jira) (*world, *llmstandin.Server, func()) {
	w := newWorld(t)
	w.secret("JIRA_TOKEN", "jira-token")
	w.secret("TEST_KEY", "sk-test")
	dir := pointed(t, "jira", j.Server)
	jiraAt(w, j)
	model := &llmstandin.Server{Answer: func(llmstandin.Call) llmstandin.Reply { return llmstandin.Reply{Text: "seen"} }}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	w.sync(dir)
	ask := func() {
		before := j.asked()
		w.until("Jira asked", func() bool {
			w.set(w.now().Add(31 * time.Second))
			time.Sleep(5 * interval)
			return j.asked() > before
		})
	}
	ask()
	return w, model, ask
}

// A picture a comment shows is downloaded when the comment is answered, and the
// model is shown it as an image beside the comment.
func TestJiraPictureIsShownToTheModel(t *testing.T) {
	t.Skip("R6 revives this: Jira")
	at := time.Now().Add(time.Second)
	picture := "\x89PNG picture"
	j := newJira(t)
	j.files = []map[string]any{attachment("10001", "cat.png", "image/png", len(picture), "acc-1", at)}
	j.contents = map[string]string{"10001": picture}
	w, model, ask := jiraAnswering(t, j)
	j.comment("1", "acc-1", "[~accountid:acc-bot] what is this? !cat.png|thumbnail!", at)
	ask()
	w.until("the answer", func() bool { return len(j.saying()) >= 1 })
	shown := fmt.Sprint(model.Calls[0].Messages)
	for _, want := range []string{"what is this?", "cat.png", "image/png", base64.StdEncoding.EncodeToString([]byte(picture))} {
		if !strings.Contains(shown, want) {
			t.Errorf("%q is not in %s", want, shown)
		}
	}
}

// A comment that only names the bot and links a file is answered with the file.
func TestJiraFileAloneIsAnswered(t *testing.T) {
	t.Skip("R6 revives this: Jira")
	at := time.Now().Add(time.Second)
	j := newJira(t)
	j.files = []map[string]any{attachment("10001", "log.txt", "text/plain", 9, "acc-1", at)}
	j.contents = map[string]string{"10001": "it failed"}
	w, _, ask := jiraAnswering(t, j)
	j.comment("1", "acc-1", "[~accountid:acc-bot] [^log.txt]", at)
	ask()
	w.until("the answer", func() bool { return len(j.saying()) >= 1 })
	if got := j.downloaded(); !slices.Equal(got, []string{"10001"}) {
		t.Fatalf("Jira gave the files %v", got)
	}
}

// A comment has the files it names, and of two with one name the newest. An
// external image names no file, and one over 32 MiB is described and not
// downloaded. A new conversation is given the issue's other files too, as earlier
// ones, without their contents and without the comment's own again.
func TestJiraCommentHasTheFilesItNames(t *testing.T) {
	t.Skip("R6 revives this: Jira")
	at := time.Now().Add(time.Second)
	j := newJira(t)
	j.files = []map[string]any{
		attachment("1", "cat.png", "image/png", 7, "acc-1", at.Add(-2*time.Second)),
		attachment("2", "cat.png", "image/png", 7, "acc-1", at.Add(-time.Second)),
		attachment("4", "dog.png", "image/png", 7, "acc-1", at.Add(time.Second)),
		attachment("7", "bird.png", "image/png", 7, "acc-1", at),
		attachment("5", "x.png", "image/png", 7, "acc-1", at),
		attachment("6", "film.mp4", "video/mp4", 64<<20, "acc-1", at),
	}
	j.contents = map[string]string{"1": "old cat", "2": "new cat", "4": "dog", "5": "x", "7": "bird"}
	w, model, ask := jiraAnswering(t, j)
	j.comment("1", "acc-1", "[~accountid:acc-bot] wow! !cat.png! and !dog.png|thumbnail! and !https://example.com/x.png! [^film.mp4]", at)
	ask()
	w.until("the answer", func() bool { return len(j.saying()) >= 1 })
	if got := j.downloaded(); !slices.Equal(slices.Sorted(slices.Values(got)), []string{"2", "4"}) {
		t.Fatalf("Jira gave the files %v", got)
	}
	shown := fmt.Sprint(model.Calls[0].Messages)
	for _, want := range []string{"video/mp4", "67108864", `"earlier":[`, `"name":"bird.png"`} {
		if !strings.Contains(shown, want) {
			t.Errorf("%q is not in %s", want, shown)
		}
	}
}

// An opened issue has its files, whoever attached them, except each one a
// restricted comment names, even where the description names it too.
func TestJiraIssueHasItsFiles(t *testing.T) {
	t.Skip("R6 revives this: Jira")
	at := time.Now().Add(time.Second)
	j := newJira(t)
	j.files = []map[string]any{
		attachment("1", "mine.png", "image/png", 4, "acc-1", at),
		attachment("2", "theirs.png", "image/png", 6, "acc-2", at),
		attachment("3", "secret.png", "image/png", 6, "acc-1", at),
		attachment("4", "both.png", "image/png", 4, "acc-1", at),
	}
	j.contents = map[string]string{"1": "mine", "2": "theirs", "3": "secret", "4": "both"}
	w, _, ask := jiraAnswering(t, j)
	j.mu.Lock()
	j.fields = map[string]any{"created": jiraTime(at), "description": "[~accountid:acc-bot] it fails !both.png!", "comment": map[string]any{"comments": []any{
		map[string]any{"id": "5", "body": "!secret.png! [^both.png]", "created": jiraTime(at.Add(time.Second)), "author": map[string]any{"accountId": "acc-1"}, "visibility": map[string]any{"type": "role", "value": "Administrators"}},
	}}}
	j.mu.Unlock()
	ask()
	w.until("the answer", func() bool { return len(j.saying()) >= 1 })
	if got := j.downloaded(); !slices.Equal(got, []string{"1", "2"}) {
		t.Fatalf("Jira gave the files %v", got)
	}
}

// An overheard reply that holds only a file is heard, and attend.yaml is given a
// line naming its files.
func TestSlackOverheardFileIsNamedToAttend(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	echoing(t, dir)
	setSite(t, dir, router("slack"), "      restart: { when: always }\n", "      restart: { when: always }\n      input: { overhear: true }\n")
	setSite(t, dir, "rules.yaml", "  dm: { readers: [asker] }\n", "  dm: { readers: [asker] }\n  \"#help\": { readers: [asker] }\n")
	if err := os.WriteFile(filepath.Join(dir, "attend.yaml"), []byte(dedent(attendStub)), 0o644); err != nil {
		t.Fatal(err)
	}
	s.thread = []map[string]any{
		{"ts": "1.0", "user": "U1", "text": "<@UBOT> start"},
		{"ts": "2.0", "user": "UBOT", "text": "send a screenshot"},
	}
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "channel": "C1", "channel_type": "channel", "user": "U1", "ts": "5.0", "thread_ts": "1.0", "text": "",
		"files": []any{map[string]any{"id": "F1", "name": "error.png", "mimetype": "image/png", "size": 3, "url_private_download": s.URL + "/files/T1-F1/error.png"}}})
	w.until("attend asked", func() bool { return len(stubs(t, w, "attend-stub")) == 1 })
	if said := fmt.Sprint(w.show(stubs(t, w, "attend-stub")[0].ID).Input.(map[string]any)["said"]); said != "(files: error.png)" {
		t.Fatalf("attend was given %q", said)
	}
}

// sending makes a stand-in model that calls send with args on its first turn,
// and then replies "sent".
func sending(args string) func(llmstandin.Call) llmstandin.Reply {
	return func(c llmstandin.Call) llmstandin.Reply {
		if c.Turn == 1 {
			return llmstandin.Reply{Calls: []llmstandin.ToolCall{{ID: "t1", Name: "send", Args: args}}}
		}
		return llmstandin.Reply{Text: "sent"}
	}
}

// An agent sends a text as a file, typed by its name, in the chat it was
// asked in.
func TestTelegramAgentSendsATextAsAFile(t *testing.T) {
	t.Skip("R4 revives this: Telegram")
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := telegramProject(t, w, tg)
	model.Answer = sending(`{"name": "report.csv", "text": "a,b\n1,2\n"}`)
	tg.says(map[string]any{"text": "the report, please"})
	w.sync(dir)
	w.until("the reply", func() bool { return len(tg.said()) >= 1 })
	want := upload{Method: "sendDocument", Field: "document", To: "1", Name: "report.csv", Type: "text/csv", Body: "a,b\n1,2\n"}
	if len(tg.uploads) != 1 || tg.uploads[0] != want {
		t.Fatalf("the bot sent %+v; want %+v", tg.uploads, want)
	}
	if got := tg.said(); got[0] != "sent" {
		t.Errorf("the bot said %v", got)
	}
}

// An agent sends back a picture it was shown.
func TestTelegramAgentSendsAPhoto(t *testing.T) {
	t.Skip("R4 revives this: Telegram")
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := telegramProject(t, w, tg)
	model.Answer = sending(`{"name": "cat.jpg", "blob": "blob1"}`)
	cat := "\xff\xd8 a cat"
	tg.files["P1"] = cat
	tg.says(map[string]any{"caption": "send it back", "photo": photo("P1", len(cat))})
	w.sync(dir)
	w.until("the reply", func() bool { return len(tg.said()) >= 1 })
	want := upload{Method: "sendDocument", Field: "document", To: "1", Name: "cat.jpg", Type: "image/jpeg", Body: cat}
	if len(tg.uploads) != 1 || tg.uploads[0] != want {
		t.Fatalf("the bot sent %+v; want %+v", tg.uploads, want)
	}
}

// slackAgentProject makes a project whose Slack calls reach the stand-in,
// and whose agent is testdata's helper, with a stand-in model.
func slackAgentProject(t *testing.T, w *world, s *slack) (string, *llmstandin.Server) {
	t.Helper()
	w.secret("TEST_KEY", "sk-test")
	dir := slackProject(t, w, s)
	model := &llmstandin.Server{Answer: func(llmstandin.Call) llmstandin.Reply { return llmstandin.Reply{Text: "seen"} }}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	return dir, model
}

// An agent sends a text as a file, typed by its name, in the direct message it
// was asked in.
func TestSlackAgentSendsATextAsAFile(t *testing.T) {
	t.Skip("R3 revives this: Slack")
	s := newSlack(t)
	w := newWorld(t)
	dir, model := slackAgentProject(t, w, s)
	model.Answer = sending(`{"name": "report.csv", "text": "a,b\n1,2\n"}`)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "3.0", "text": "the report, please"})
	w.until("the reply", func() bool { return len(s.posted()) >= 1 })
	want := upload{Method: "files.completeUploadExternal", To: "D1", Name: "report.csv", Type: "text/csv", Body: "a,b\n1,2\n"}
	if len(s.shared) != 1 || s.shared[0] != want {
		t.Fatalf("the bot sent %+v; want %+v", s.shared, want)
	}
	if got := s.posted()[0]["markdown_text"]; got != "sent" {
		t.Errorf("the bot said %v", got)
	}
}

// An agent sends back a picture it was shown, in the thread of the channel
// message it was asked in.
func TestSlackAgentSendsAPictureInTheThread(t *testing.T) {
	t.Skip("R3 revives this: Slack")
	s := newSlack(t)
	w := newWorld(t)
	dir, model := slackAgentProject(t, w, s)
	setSite(t, dir, "rules.yaml", "  dm: { readers: [asker] }\n", "  dm: { readers: [asker] }\n  \"#help\": { readers: [asker] }\n")
	model.Answer = sending(`{"name": "cat.png", "blob": "blob1"}`)
	picture := "\x89PNG picture"
	s.files = map[string]string{"/files/T1-F1/cat.png": picture}
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "subtype": "file_share", "channel": "C1", "channel_type": "channel", "user": "U1", "ts": "4.0", "text": "<@UBOT> send it back",
		"files": []any{map[string]any{"id": "F1", "name": "cat.png", "mimetype": "image/png", "size": len(picture), "url_private_download": s.URL + "/files/T1-F1/cat.png"}}})
	w.until("the reply", func() bool { return len(s.posted()) >= 1 })
	want := upload{Method: "files.completeUploadExternal", To: "C1/4.0", Name: "cat.png", Type: "image/png", Body: picture}
	if len(s.shared) != 1 || s.shared[0] != want {
		t.Fatalf("the bot sent %+v; want %+v", s.shared, want)
	}
}

// A token without the scope files:write sends nothing, and the model is given
// Slack's refusal.
func TestSlackAgentIsToldASendIsRefused(t *testing.T) {
	t.Skip("R3 revives this: Slack")
	s := newSlack(t)
	s.noWrite = true
	w := newWorld(t)
	dir, model := slackAgentProject(t, w, s)
	model.Answer = sending(`{"name": "report.csv", "text": "a,b\n"}`)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "3.0", "text": "the report, please"})
	w.until("the reply", func() bool { return len(s.posted()) >= 1 })
	if len(s.shared) != 0 {
		t.Fatalf("the bot sent %+v", s.shared)
	}
	if shown := fmt.Sprint(model.Calls[1].Messages); !strings.Contains(shown, "Slack refused files.getUploadURLExternal: missing_scope") {
		t.Errorf("the model was shown %s", shown)
	}
}

// An agent sends a text as a file, typed by its name: it is attached to the
// issue it was asked on, and a comment links it, before the reply.
func TestJiraAgentSendsATextAsAFile(t *testing.T) {
	t.Skip("R6 revives this: Jira")
	at := time.Now().Add(time.Second)
	j := newJira(t)
	w, model, ask := jiraAnswering(t, j)
	model.Answer = sending(`{"name": "report.csv", "text": "a,b\n1,2\n"}`)
	j.comment("1", "acc-1", "[~accountid:acc-bot] the report, please", at)
	ask()
	w.until("the reply", func() bool { return len(j.saying()) >= 2 })
	want := upload{Method: "attachments", Field: "file", To: "OPS-12", Name: "report.csv", Type: "text/csv", Body: "a,b\n1,2\n"}
	if got := j.attached(); len(got) != 1 || got[0] != want {
		t.Fatalf("the bot attached %+v; want %+v", got, want)
	}
	if got := j.saying(); !slices.Equal(got, []string{"[^report.csv]", "sent"}) {
		t.Errorf("the bot said %q", got)
	}
}

// An agent sends back a picture it was shown, and a comment shows it.
func TestJiraAgentSendsAPicture(t *testing.T) {
	t.Skip("R6 revives this: Jira")
	at := time.Now().Add(time.Second)
	picture := "\x89PNG picture"
	j := newJira(t)
	j.files = []map[string]any{attachment("10001", "cat.png", "image/png", len(picture), "acc-1", at)}
	j.contents = map[string]string{"10001": picture}
	w, model, ask := jiraAnswering(t, j)
	model.Answer = sending(`{"name": "cat.png", "blob": "blob1"}`)
	j.comment("1", "acc-1", "[~accountid:acc-bot] send it back !cat.png!", at)
	ask()
	w.until("the reply", func() bool { return len(j.saying()) >= 2 })
	want := upload{Method: "attachments", Field: "file", To: "OPS-12", Name: "cat.png", Type: "image/png", Body: picture}
	if got := j.attached(); len(got) != 1 || got[0] != want {
		t.Fatalf("the bot attached %+v; want %+v", got, want)
	}
	if got := j.saying(); got[0] != "!cat.png!" {
		t.Errorf("the bot said %q", got)
	}
}

// A file Jira refuses is named in no comment, and the model is told Jira's
// status.
func TestJiraRefusedFileIsNotNamed(t *testing.T) {
	t.Skip("R6 revives this: Jira")
	at := time.Now().Add(time.Second)
	j := newJira(t)
	j.largest = 4
	w, model, ask := jiraAnswering(t, j)
	model.Answer = sending(`{"name": "report.csv", "text": "a,b\n1,2\n"}`)
	j.comment("1", "acc-1", "[~accountid:acc-bot] the report, please", at)
	ask()
	w.until("the reply", func() bool { return len(j.saying()) >= 1 })
	if got := j.saying(); !slices.Equal(got, []string{"sent"}) {
		t.Errorf("the bot said %q", got)
	}
	if told := fmt.Sprint(model.Calls[1].Messages); !strings.Contains(told, "Jira answered POST /issue/OPS-12/attachments with 413") {
		t.Errorf("the model was told %s", told)
	}
}

// A name is sent with each [, ], |, ! and ^ as _, and the comment names the
// file as Jira keeps it, which may differ.
func TestJiraCommentNamesTheFileAsJiraKeepsIt(t *testing.T) {
	t.Skip("R6 revives this: Jira")
	at := time.Now().Add(time.Second)
	j := newJira(t)
	j.stored = "report (1).csv"
	w, model, ask := jiraAnswering(t, j)
	model.Answer = sending(`{"name": "report [1].csv", "text": "a,b\n1,2\n"}`)
	j.comment("1", "acc-1", "[~accountid:acc-bot] the report, please", at)
	ask()
	w.until("the reply", func() bool { return len(j.saying()) >= 2 })
	if got := j.attached(); len(got) != 1 || got[0].Name != "report _1_.csv" {
		t.Fatalf("the bot attached %+v", got)
	}
	if got := j.saying(); got[0] != "[^report (1).csv]" {
		t.Errorf("the bot said %q", got)
	}
}

// An image Jira does not show in a comment, such as an SVG, is linked.
func TestJiraOtherImageIsLinked(t *testing.T) {
	t.Skip("R6 revives this: Jira")
	at := time.Now().Add(time.Second)
	j := newJira(t)
	w, model, ask := jiraAnswering(t, j)
	model.Answer = sending(`{"name": "logo.svg", "text": "<svg/>"}`)
	j.comment("1", "acc-1", "[~accountid:acc-bot] the logo, please", at)
	ask()
	w.until("the reply", func() bool { return len(j.saying()) >= 2 })
	if got := j.attached(); len(got) != 1 || got[0].Type != "image/svg+xml" {
		t.Fatalf("the bot attached %+v", got)
	}
	if got := j.saying(); got[0] != "[^logo.svg]" {
		t.Errorf("the bot said %q", got)
	}
}

// A machine sends a file to a person by the address lookup.yaml gives, their
// member id, and Slack puts it in their direct message with the bot.
func TestSlackSendsAFileToAPerson(t *testing.T) {
	t.Skip("R3 revives this: Slack")
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	machine := "flow: report\nstages:\n  send:\n    job: { machine: .rook/blocks/send.yaml, input: { address: { bridge: slack, id: U1 }, name: report.csv, text: \"a,b\\n\" } }\n"
	if err := os.WriteFile(filepath.Join(dir, "on", "report.yaml"), []byte(machine), 0o644); err != nil {
		t.Fatal(err)
	}
	w.sync(dir)
	w.start("on/report.yaml", nil)
	w.until("the file shared", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.shared) == 1
	})
	if want := (upload{Method: "files.completeUploadExternal", To: "D9", Name: "report.csv", Type: "text/csv", Body: "a,b\n"}); s.shared[0] != want {
		t.Fatalf("the bot sent %+v; want %+v", s.shared[0], want)
	}
}

// looking makes a stand-in model that, on its first turn, calls look with the id
// its prompt gives the earlier file named name, and then replies "seen".
func looking(name string) func(llmstandin.Call) llmstandin.Reply {
	return func(c llmstandin.Call) llmstandin.Reply {
		if c.Turn > 1 {
			return llmstandin.Reply{Text: "seen"}
		}
		file := regexp.MustCompile(`\{[^{}]*"name":"` + regexp.QuoteMeta(name) + `"[^{}]*\}`).FindString(fmt.Sprint(c.Messages))
		id := regexp.MustCompile(`"id":"([^"]*)"`).FindStringSubmatch(file)
		if id == nil {
			return llmstandin.Reply{Text: "no " + name + " in the prompt"}
		}
		args, _ := json.Marshal(map[string]string{"id": id[1]})
		return llmstandin.Reply{Calls: []llmstandin.ToolCall{{ID: "t1", Name: "look", Args: string(args)}}}
	}
}

// A reply in a Slack thread starts a conversation that is given the files earlier
// in the thread without their contents, beside its own. The model is shown an
// earlier picture when it looks at it.
func TestSlackThreadGivesItsEarlierFiles(t *testing.T) {
	t.Skip("R3 revives this: Slack")
	s := newSlack(t)
	w := newWorld(t)
	w.secret("TEST_KEY", "sk-test")
	dir := slackProject(t, w, s)
	model := &llmstandin.Server{Answer: looking("fox.png")}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	picture, note := "\x89PNG fox", "a sentence"
	s.files = map[string]string{"/files/T1-F1/fox.png": picture, "/files/T1-F2/note.txt": note}
	file := func(id, name, mimetype, body string) []any {
		return []any{map[string]any{"id": id, "name": name, "mimetype": mimetype, "size": len(body), "url_private_download": s.URL + "/files/T1-" + id + "/" + name}}
	}
	s.thread = []map[string]any{{"ts": "1.0", "user": "U1", "text": "", "files": file("F1", "fox.png", "image/png", picture)}}
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "2.0", "thread_ts": "1.0", "text": "what are these?",
		"files": file("F2", "note.txt", "text/plain", note)})
	w.until("the answer", func() bool { return len(s.posted()) >= 1 })
	if got := s.posted(); !strings.Contains(fmt.Sprint(got), "seen") {
		t.Fatalf("the bot said %v", got)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(picture))
	first := fmt.Sprint(model.Calls[0].Messages)
	for _, want := range []string{"note.txt", `"earlier":[`, "(files: fox.png)"} {
		if !strings.Contains(first, want) {
			t.Errorf("%q is not in %s", want, first)
		}
	}
	if strings.Contains(first, encoded) {
		t.Errorf("the model was shown the earlier picture before it looked: %s", first)
	}
	if shown := fmt.Sprint(model.Calls[1].Messages); !strings.Contains(shown, encoded) {
		t.Errorf("the model was not shown the picture it looked at: %s", shown)
	}
}

// A Jira issue with more comments than the history's text holds still gives its
// files.
func TestJiraLongIssueGivesItsFiles(t *testing.T) {
	t.Skip("R6 revives this: Jira")
	at := time.Now().Add(time.Second)
	j := newJira(t)
	j.files = []map[string]any{attachment("1", "shot.png", "image/png", 7, "acc-1", at.Add(-time.Hour))}
	w, model, ask := jiraAnswering(t, j)
	for n := 2; n < 27; n++ {
		j.comment(strconv.Itoa(n), "acc-1", "comment "+strconv.Itoa(n), at.Add(-time.Minute))
	}
	j.comment("30", "acc-1", "[~accountid:acc-bot] what is wrong?", at)
	ask()
	w.until("the answer", func() bool { return len(j.saying()) >= 1 })
	if shown := fmt.Sprint(model.Calls[0].Messages); !strings.Contains(shown, `"name":"shot.png"`) {
		t.Errorf("the model was not given the issue's file: %s", shown)
	}
	if got := j.downloaded(); len(got) != 0 {
		t.Errorf("Jira gave the files %v", got)
	}
}
