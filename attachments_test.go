package rook_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// A picture sent on Slack is downloaded when the message is answered, and the
// model is shown it as an image beside the message.
func TestSlackPictureIsShownToTheModel(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
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
	t.Skip("R7 revives this: conversation, agent and starter")
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
	t.Skip("R7 revives this: the conversation gives a message's files to the agent")
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

// Each item of an album is a message of its own, with its own caption, and
// the text sent after it is heard once.
func TestTelegramAlbumItemsAreMessages(t *testing.T) {
	t.Skip("R7 revives this: the conversation gives a message's files to the agent")
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

// filesAgent stands in for the agent at agents/assistant: it replies with a
// line for each message of the turn, giving each file the message carries, as
// name, type, size and ID, after its text.
var filesAgent = map[string]string{
	"agents/assistant.yaml": `
		flow: assistant
		input: { messages: list, medium: string, asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
		stages:
		  turn: { job: { script: ./files.js, input: { messages: "${input.messages}" } } }
		output: ${stages.turn.output}
	`,
	"agents/files.js": `
		const said = (m) => [m.text].concat((m.attachments || []).map((a) => [a.name, a.type, a.size, a.id].join(" "))).join(" + ")
		return { reply: input.messages.map(said).join("\n") }
	`,
}

// Telegram's listener sends a message's file as a reference, and downloads
// nothing: a document by its name and type, a voice message with no caption
// by its kind, a photo in its largest size, and each item of an album as a
// message of its own, with its own caption. The conversation downloads each
// once, with the bot's files/get.
func TestTelegramFilesAreSentAsReferences(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, filesAgent)
	tg.files["D1"], tg.files["V1"], tg.files["A1"], tg.files["A2"] = "the notes", "a voice", "one", "two"
	tg.dm(1, "", map[string]any{"caption": "read this", "document": map[string]any{"file_id": "D1", "file_unique_id": "u", "file_name": "notes.txt", "mime_type": "text/plain", "file_size": 18}})
	tg.dm(1, "", map[string]any{"voice": map[string]any{"file_id": "V1", "file_unique_id": "u", "mime_type": "audio/ogg", "duration": 2, "file_size": 7}})
	tg.dm(1, "", map[string]any{"media_group_id": "g1", "caption": "before", "photo": photo("A1", 9)})
	tg.dm(1, "", map[string]any{"media_group_id": "g1", "caption": "after", "photo": photo("A2", 9)})
	tg.dm(1, "thanks")
	w.sync(dir)
	tg.waitFor(w, "1", "thanks")
	want := []string{
		"read this + notes.txt text/plain 18 D1",
		" + voice audio/ogg 7 V1",
		"before + photo.jpg image/jpeg 9 A1",
		"after + photo.jpg image/jpeg 9 A2",
		"thanks",
	}
	if got := tg.lines("1"); !slices.Equal(got, want) {
		t.Errorf("alice was told %q", got)
	}
	// Only the conversation's files/get downloads, each file once.
	if got := slices.Sorted(slices.Values(tg.got)); !slices.Equal(got, []string{"A1", "A2", "D1", "V1"}) {
		t.Errorf("Telegram was asked for the files %v", got)
	}
}

// A bot's files/get downloads each file it is given, as a blob of its type. A
// file over the 20 MB Telegram gives a bot is given back as it is, with no
// download; a file Telegram cannot give fails the call, naming the file and
// not the token.
func TestTelegramFilesGetDownloadsEachFile(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, nil)
	tg.files["P1"] = "\xff\xd8 a cat"
	w.sync(dir)
	large := map[string]any{"name": "film.mp4", "type": "video/mp4", "size": 25 << 20, "id": "D1"}
	id := w.start(bot+"/telegram/files/get.yaml", map[string]any{"attachments": []any{
		map[string]any{"name": "photo.jpg", "type": "image/jpeg", "size": 8, "id": "P1"}, large,
	}})
	w.until("the download", func() bool { return w.show(id).HasEnded() })
	run := w.show(id)
	got, _ := run.Output.([]any)
	if run.Status != "done" || len(got) != 2 {
		t.Fatalf("files/get ended %s with %v: %s", run.Status, run.Output, run.Error)
	}
	if file, _ := got[0].(map[string]any)["file"].(map[string]any); file["type"] != "image/jpeg" || file["size"] != 8.0 {
		t.Errorf("the photo was given as %v", got[0])
	}
	if !reflect.DeepEqual(got[1], map[string]any{"name": "film.mp4", "type": "video/mp4", "size": float64(25 << 20), "id": "D1"}) {
		t.Errorf("the large file was given as %v", got[1])
	}
	if !slices.Equal(tg.got, []string{"P1"}) {
		t.Errorf("getFile was asked for %v", tg.got)
	}
	gone := w.start(bot+"/telegram/files/get.yaml", map[string]any{"attachments": []any{
		map[string]any{"name": "gone.pdf", "type": "application/pdf", "size": 10, "id": "GONE"},
	}})
	w.until("the failed download", func() bool { return w.show(gone).HasEnded() })
	if run := w.show(gone); run.Status != "failed" || !strings.Contains(run.Error, "the download of gone.pdf with status 404") || strings.Contains(run.Error, "123:abc") {
		t.Errorf("files/get ended %s: %s", run.Status, run.Error)
	}
}

// A machine sends a file in a chat with the bot's placed files/send: a text it
// made, typed by its name, and a photo files/get downloaded, as Telegram's
// documents.
func TestTelegramFilesSendSendsADocument(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		bot + "/telegram/files/send.yaml": "include: .rook/telegram/files/send.yaml\n",
		"agents/sender.yaml": `
			flow: sender
			allow: [send:gromit/telegram/files/get, send:gromit/telegram/files/send]
			stages:
			  made: { job: { script: ./made.js } }
			  report: { job: { call: { at: gromit/telegram/files/send }, input: { in: "1", name: report.csv, file: "${stages.made.output}" } } }
			  got: { job: { call: { at: gromit/telegram/files/get }, input: { attachments: [{ name: photo.jpg, type: image/jpeg, size: 8, id: P1 }] } } }
			  photo: { job: { call: { at: gromit/telegram/files/send }, input: { in: "1", name: cat.jpg, file: "${stages.got.output[0].file}" } }, follows: [report] }
		`,
		"agents/made.js": `return blobs.make("a,b\n1,2\n", { type: "text/csv" })`,
	})
	cat := "\xff\xd8 a cat"
	tg.files["P1"] = cat
	w.sync(dir)
	id := w.start("agents/sender.yaml", nil)
	w.until("the sends", func() bool { return w.show(id).HasEnded() })
	if run := w.show(id); run.Status != "done" {
		t.Fatalf("the sender ended %s: %s", run.Status, run.Error)
	}
	want := []upload{
		{Method: "sendDocument", Field: "document", To: "1", Name: "report.csv", Type: "text/csv", Body: "a,b\n1,2\n"},
		{Method: "sendDocument", Field: "document", To: "1", Name: "cat.jpg", Type: "image/jpeg", Body: cat},
	}
	if !slices.Equal(tg.uploads, want) {
		t.Errorf("the bot sent %+v; want %+v", tg.uploads, want)
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

// uploading is a machine that uploads a text as a file at the bot's Slack
// files/upload, which the project places, in a conversation `in` names.
func uploading(t *testing.T, dir string) {
	t.Helper()
	write(t, dir, map[string]string{
		bot + "/slack/files/upload.yaml": "include: .rook/slack/files/upload.yaml",
		"on/report.yaml": `
			flow: report
			input: { in: string }
			allow: [send:gromit/slack/files/upload]
			stages:
			  file: { job: { script: ./file.js } }
			  upload: { job: { call: { at: gromit/slack/files/upload }, input: { in: "${input.in}", name: report.csv, file: "${stages.file.output}" } } }
			output: ${stages.upload.output}
		`,
		"on/file.js": `return blobs.make("a,b\n", { type: "text/csv" })`,
	})
}

// A machine uploads a file to a person by their member ID, which Slack puts in
// their direct message with the bot, and to a channel's thread.
func TestSlackSendsAFileToAPerson(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	uploading(t, dir)
	w.sync(dir)
	for _, in := range []string{"U1", "C1/4.0"} {
		id := w.start("on/report.yaml", map[string]any{"in": in})
		w.until("the upload", func() bool { return w.show(id).Status == "done" })
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	want := []upload{
		{Method: "files.completeUploadExternal", To: "D9", Name: "report.csv", Type: "text/csv", Body: "a,b\n"},
		{Method: "files.completeUploadExternal", To: "C1/4.0", Name: "report.csv", Type: "text/csv", Body: "a,b\n"},
	}
	if !slices.Equal(s.shared, want) {
		t.Fatalf("the bot sent %+v; want %+v", s.shared, want)
	}
}

// A token without the scope files:write uploads nothing, and the upload fails
// with Slack's refusal.
func TestSlackRefusedUploadFailsSayingWhy(t *testing.T) {
	s := newSlack(t)
	s.noWrite = true
	w := newWorld(t)
	dir := slackProject(t, w, s)
	uploading(t, dir)
	w.sync(dir)
	id := w.start("on/report.yaml", map[string]any{"in": "U1"})
	w.until("the upload failed", func() bool { return w.show(id).Status == "failed" })
	if got := w.show(id).Error; !strings.Contains(got, "Slack refused files.getUploadURLExternal: missing_scope") || len(s.shared) != 0 {
		t.Fatalf("the upload failed with %q, and the bot sent %+v", got, s.shared)
	}
}

// files/get downloads a message's file with the bot token, as a blob, and
// gives one over 32 MiB as it is, without downloading it.
func TestSlackFilesGetDownloadsAFile(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	picture := "\x89PNG picture"
	s.files = map[string]string{"/files/T1-F1/cat.png": picture}
	write(t, dir, map[string]string{
		"on/get.yaml": `
			flow: get
			input: { attachments: list }
			allow: [send:gromit/slack/files/get]
			stages:
			  get: { job: { call: { at: gromit/slack/files/get }, input: { attachments: "${input.attachments}" } } }
			  read: { job: { script: ./read.js, input: { files: "${stages.get.output}" } } }
			output: ${stages.read.output}
		`,
		"on/read.js": `return input.files.map((f) => f.name + " " + f.type + " " + (f.file ? blobs.read(f.file, { as: "bytes" }).length + " bytes" : "not downloaded"))`,
	})
	w.sync(dir)
	id := w.start("on/get.yaml", map[string]any{"attachments": []any{
		map[string]any{"name": "cat.png", "type": "image/png", "size": len(picture), "id": s.URL + "/files/T1-F1/cat.png"},
		map[string]any{"name": "film.mp4", "type": "video/mp4", "size": 64 << 20, "id": s.URL + "/files/T1-F2/film.mp4"},
	}})
	w.until("the files", func() bool { st := w.show(id).Status; return st == "done" || st == "failed" })
	got, _ := json.Marshal(w.show(id).Output)
	if want := fmt.Sprintf(`["cat.png image/png %d bytes","film.mp4 video/mp4 not downloaded"]`, len(picture)); string(got) != want {
		t.Fatalf("files/get gave %s: %s", got, w.show(id).Error)
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

// A reply in a Slack thread carries its own files, and its history names the
// files earlier in the thread.
func TestSlackThreadGivesItsEarlierFiles(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	echoing(t, dir)
	file := func(id, name, mimetype string) []any {
		return []any{map[string]any{"id": id, "name": name, "mimetype": mimetype, "size": 3, "url_private_download": s.URL + "/files/T1-" + id + "/" + name}}
	}
	s.thread = []map[string]any{{"ts": "1.0", "user": "U1", "text": "", "files": file("F1", "fox.png", "image/png")}}
	s.files = map[string]string{"/files/T1-F2/note.txt": "abc"}
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "2.0", "thread_ts": "1.0", "text": "what are these?",
		"files": file("F2", "note.txt", "text/plain")})
	w.until("the answer", func() bool { return len(s.posted()) >= 1 })
	if got := fmt.Sprint(s.posted()[0]["markdown_text"]); got != "[Alice:  (files: fox.png)] what are these?" {
		t.Fatalf("the bot said %q", got)
	}
	sent := emitted(w, "slack", "message")
	if files, _ := json.Marshal(sent[0]["attachments"]); string(files) != `[{"id":"`+s.URL+`/files/T1-F2/note.txt","name":"note.txt","size":3,"type":"text/plain"}]` {
		t.Errorf("the message's files are %s", files)
	}
}
