package rook_test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// slackAgentProject makes a project whose Slack bridge reaches the stand-in,
// and whose helper is the starter's, with a stand-in model.
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
