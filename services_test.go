package rook_test

// The Slack, GitLab and Jira bridges against their stand-ins: threads,
// mentions, envelopes Slack sends again, files, hosts as values, questions
// answered by Slack's buttons, comments as chats, and several bridges in one
// project.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zummed/corbie/client"
	"github.com/zummed/corbie/instance"
	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// headers records the Authorization and PRIVATE-TOKEN headers each request to
// a stand-in carried, by path, so a test can see which host a token went to.
type headers struct {
	mu   sync.Mutex
	seen []string // "<method> <path> auth=<..> private=<..>"
}

func (h *headers) wrap(srv *httptest.Server) {
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		h.seen = append(h.seen, fmt.Sprintf("%s %s auth=%s private=%s", r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("PRIVATE-TOKEN")))
		h.mu.Unlock()
		inner.ServeHTTP(w, r)
	})
}

func (h *headers) all() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.seen)
}

// override puts a handler in front of a stand-in: it answers when it gives
// true, and the stand-in answers otherwise.
func override(srv *httptest.Server, first func(w http.ResponseWriter, r *http.Request) bool) {
	inner := srv.Config.Handler
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !first(w, r) {
			inner.ServeHTTP(w, r)
		}
	})
}

var slackHosts = []string{"files.slack.com", "slack.com", "wss-primary.slack.com", "wss-backup.slack.com"}

// everyBridge makes a project with Slack, GitLab and Jira, each pointed at its
// stand-in, the helper stub, and alice and bob on every bridge.
func everyBridge(t *testing.T, w *world, s *slack, gl *gitlab, j *jira) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project")
	if err := exampleProject(dir, []string{"slack", "gitlab", "jira"}); err != nil {
		t.Fatal(err)
	}
	point(t, dir, "slack", s.Server, slackHosts...)
	point(t, dir, "gitlab", gl.Server)
	point(t, dir, "jira", j.Server)
	write(t, dir, map[string]string{
		"agents/helper.yaml": helperStub,
		"people.yaml":        "people:\n  alice: { slack: U1, gitlab: al, jira: acc-1 }\n  bob: { slack: U2, gitlab: bo, jira: acc-2 }\ngroups: {}\n",
		"rules.yaml":         "places:\n  dm: { readers: [asker] }\n  \"#help\": { readers: [asker] }\n  group/project: { readers: [asker] }\n  OPS: { readers: [asker] }\nagents:\n  - { name: helper, for: [alice] }\n",
	})
	w.secret("SLACK_TOKEN", "xoxb-bot")
	w.secret("SLACK_APP_TOKEN", "xapp-app")
	w.secret("GITLAB_TOKEN", "gl-token")
	w.secret("JIRA_TOKEN", "jira-token")
	w.value("GITLAB_SITE", strings.TrimPrefix(gl.URL, "http://"))
	jiraAt(w, j)
	return dir
}

// tick moves the clock past a poll, and waits until the stand-in was asked
// again.
func tick(w *world, asked func() int) {
	w.t.Helper()
	before := asked()
	w.until("asked again", func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return asked() > before
	})
}

// logged gives what the runs of a bridge's listener logged, in order.
func logged(w *world, bridge string) []string {
	var out []string
	for _, run := range listenerRuns(w, bridge) {
		for _, e := range w.record(run.ID) {
			if e.Name == "log" {
				out = append(out, fmt.Sprint(e.Data))
			}
		}
	}
	return out
}

// listenerRuns gives the runs of a bridge's listener, the children of its
// bridge, newest last.
func listenerRuns(w *world, bridge string) []*client.Run {
	var out []*client.Run
	for _, run := range w.at("bridges/" + bridge + ".yaml") {
		if run.Parent != "" {
			out = append(out, run)
		}
	}
	return out
}

// failures gives the errors of every failed run under the project, and of
// every failed job a run's record shows.
func failures(w *world) []string {
	w.t.Helper()
	runs, err := w.List(context.Background(), client.Filter{Finished: true})
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for _, run := range runs {
		if run.Status == "failed" {
			full := w.show(run.ID)
			out = append(out, run.Machine+": "+full.Error+full.Refused)
		}
		for _, e := range w.record(run.ID) {
			for _, ev := range e.Events {
				if ev.Name == "failed" {
					b, _ := json.Marshal(ev.Data)
					out = append(out, run.Machine+" job failed: "+string(b))
				}
			}
			for _, a := range e.Asks {
				if a.Kind == instance.AskNote || strings.Contains(strings.ToLower(a.Kind), "fail") {
					out = append(out, run.Machine+" "+a.Kind+": "+a.Text)
				}
			}
		}
	}
	return out
}

// dm is a direct message from a person to the bot on Slack.
func dm(user, ts, text string) map[string]any {
	return map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": user, "ts": ts, "text": text}
}

// inChannel is a message in channel C1, in a thread when thread is given.
func inChannel(user, ts, thread, text string) map[string]any {
	e := map[string]any{"type": "message", "channel": "C1", "channel_type": "channel", "user": user, "ts": ts, "text": text}
	if thread != "" {
		e["thread_ts"] = thread
	}
	return e
}

func withHelp(t *testing.T, dir string) {
	t.Helper()
	setSite(t, dir, "rules.yaml", "  dm: { readers: [asker] }\n", "  dm: { readers: [asker] }\n  \"#help\": { readers: [asker] }\n")
}

// A message naming the bot in a channel starts a thread's chat, and a reply in
// that thread naming it again is the same chat and the same conversation, and is
// answered in the thread.
func TestSlackThreadReplyKeepsItsConversation(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	withHelp(t, dir)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", inChannel("U1", "4.0", "", "<@UBOT> first"))
	w.until("the first answer", func() bool { return len(s.posted()) >= 1 })
	s.says("b", inChannel("U1", "5.0", "4.0", "<@UBOT> second"))
	w.until("the second answer", func() bool { return len(s.posted()) >= 2 })
	for _, post := range s.posted() {
		if post["channel"] != "C1" || post["thread_ts"] != "4.0" {
			t.Errorf("a reply was posted at %v", post)
		}
	}
	chats := keyed(w, ".rook/blocks/chat.yaml")
	if len(chats) != 1 || chats["slack:C1/4.0"] == nil {
		t.Errorf("the chats are %v", slices.Collect(func(yield func(string) bool) {
			for k := range chats {
				yield(k)
			}
		}))
	}
	conversations := keyed(w, ".rook/blocks/conversation.yaml")
	if len(conversations) != 1 || conversations["slack:C1/4.0:helper"] == nil {
		t.Errorf("there are %d conversations", len(conversations))
	}
}

// A reply in a thread of a direct message is its own chat, and is answered in
// that thread; the top of the direct message is another.
func TestSlackDirectMessageThreadIsItsOwnChat(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", dm("U1", "3.0", "top"))
	w.until("the first answer", func() bool { return len(s.posted()) >= 1 })
	reply := dm("U1", "4.0", "in a thread")
	reply["thread_ts"] = "3.0"
	s.says("b", reply)
	w.until("the second answer", func() bool { return len(s.posted()) >= 2 })
	got := s.posted()
	if got[0]["thread_ts"] != nil || got[1]["thread_ts"] != "3.0" {
		t.Errorf("the replies went to %v", got)
	}
	chats := keyed(w, ".rook/blocks/chat.yaml")
	if chats["slack:D1"] == nil || chats["slack:D1/3.0"] == nil {
		t.Errorf("the chats are %v", chats)
	}
}

// alice, whom people.yaml names, names the bot in a channel rules.yaml does
// not list, which anyone reads, so her agent, for [alice], does not answer
// there. She is told the bot does not answer her there.
func TestSlackOwnerInAnUnlistedChannelIsToldTheBotIsNotForHere(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", inChannel("U1", "4.0", "", "<@UBOT> hello"))
	w.until("an answer", func() bool { return len(s.posted()) >= 1 })
	if got := s.posted()[0]["markdown_text"]; got != "This bot does not answer you here." {
		t.Errorf("alice was told %q in #help, which rules.yaml does not list", got)
	}
}

// Slack sends an envelope again after the listener reconnects, under a new
// envelope ID. The event in it is answered once, since the listener's cursor
// carries what it heard to the next job.
func TestSlackResentEnvelopeAfterReconnectIsAnsweredOnce(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	event := dm("U1", "3.0", "once only")
	s.says("a", event)
	w.until("the answer", func() bool { return len(s.posted()) >= 1 })
	s.mu.Lock()
	first := s.socket
	s.mu.Unlock()
	s.send(map[string]any{"type": "disconnect", "reason": "refresh_requested"})
	w.until("Slack connected again", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.socket != first
	})
	s.says("b", event)
	w.until("the resent envelope received", func() bool { return s.acked() >= 2 })
	time.Sleep(30 * interval)
	if got := s.posted(); len(got) != 1 {
		t.Fatalf("the bot posted %d times: %v", len(got), got)
	}
}

// A press of a question's button that Slack sends twice answers the question
// once, and the question is changed once.
func TestSlackResentPressAnswersOnce(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	write(t, dir, map[string]string{"on/asker.yaml": `
		flow: asker
		allow: [send:.rook/blocks/ask]
		stages:
		  asking: { job: { call: { at: .rook/blocks/ask }, input: { people: [alice], text: "Restart?", choices: ["yes", "no"] } } }
		output: ${stages.asking.output.choice}
	`})
	w.sync(dir)
	asker := w.start("on/asker.yaml", nil)
	w.until("Slack connected", s.connected)
	w.until("the question", func() bool { return len(s.posted()) == 1 })
	blocks, _ := json.Marshal(s.posted()[0]["blocks"])
	value := strings.Split(strings.Split(string(blocks), `"value":"`)[1], `"`)[0]
	value = value[:strings.LastIndex(value, " ")] + " yes"
	press := map[string]any{"type": "interactive", "payload": map[string]any{
		"type": "block_actions", "user": map[string]any{"id": "U1"}, "actions": []any{map[string]any{"action_id": "choice-0", "value": value}},
		"channel": map[string]any{"id": "D9"}, "message": map[string]any{"ts": "1.1", "text": "Restart?"},
	}}
	press["envelope_id"] = "p1"
	s.send(press)
	press["envelope_id"] = "p2"
	s.send(press)
	w.until("the asker done", func() bool { return w.show(asker).Status == "done" })
	time.Sleep(30 * interval)
	s.mu.Lock()
	updates := len(s.updates)
	s.mu.Unlock()
	if got := w.show(asker).Output; got != "yes" || updates != 1 {
		t.Fatalf("the choice was %v, and the question changed %d times", got, updates)
	}
}

// One message in a channel whose name Slack will not give the bot (as
// conversations.info answers for a channel the bot cannot read) does not stop
// the bot hearing the next message: the direct message after it is answered
// without waiting for the listener's minute of rest.
func TestSlackUnreadableChannelDoesNotStopTheListener(t *testing.T) {
	s := newSlack(t)
	override(s.Server, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/conversations.info" && r.URL.Query().Get("channel") == "C7" {
			fmt.Fprint(w, `{"ok": false, "error": "channel_not_found"}`)
			return true
		}
		return false
	})
	w := newWorld(t)
	dir := slackProject(t, w, s)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	bad := inChannel("U1", "4.0", "", "<@UBOT> from a shared channel")
	bad["channel"] = "C7"
	s.says("a", bad)
	time.Sleep(20 * interval)
	// Sent as Slack would, ignoring a connection the listener closed.
	data, _ := json.Marshal(map[string]any{"envelope_id": "b", "type": "events_api", "payload": map[string]any{"type": "event_callback", "event": dm("U1", "5.0", "after it")}})
	s.mu.Lock()
	conn := s.socket
	s.mu.Unlock()
	sendErr := conn.Write(context.Background(), 1, data)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(s.posted()) == 0 {
		time.Sleep(interval)
	}
	if len(s.posted()) == 0 {
		t.Fatalf("one message in a channel conversations.info refuses stopped the listener for its minute of rest, and the direct message after it was not heard (send: %v); acks: %d; failures:\n%s", sendErr, s.acked(), strings.Join(failures(w), "\n"))
	}
}

// A message whose channel Slack fails to name once, with a 500, is left
// unacknowledged, so Slack sends it again; the resend is answered, and a later
// resend of it is not answered again. A message in a channel Slack refuses to
// name, and a press with no user, are acknowledged and skipped.
func TestSlackListenerLeavesAFailedEnvelopeForSlackToSendAgain(t *testing.T) {
	s := newSlack(t)
	var mu sync.Mutex
	failed := false
	override(s.Server, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/conversations.info" {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Query().Get("channel") {
		case "C7":
			if !failed {
				failed = true
				w.WriteHeader(500)
				fmt.Fprint(w, "upstream error")
				return true
			}
		case "C8":
			fmt.Fprint(w, `{"ok": false, "error": "channel_not_found"}`)
			return true
		}
		return false
	})
	w := newWorld(t)
	dir := slackProject(t, w, s)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	acked := func(id string) bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return slices.Contains(s.acks, id)
	}
	lost := inChannel("U1", "4.0", "", "<@UBOT> once")
	lost["channel"] = "C7"
	refused := inChannel("U1", "5.0", "", "<@UBOT> never")
	refused["channel"] = "C8"
	s.says("a", lost)
	s.says("b", refused)
	// A press with no user, which can never be handled, is acknowledged too.
	s.send(map[string]any{"envelope_id": "d", "type": "interactive", "payload": map[string]any{
		"type": "block_actions", "actions": []any{map[string]any{"action_id": "choice-0", "value": "20260101T000000.000Z-0123456789 yes"}},
	}})
	w.until("the refused envelopes received", func() bool { return acked("b") && acked("d") })
	time.Sleep(10 * interval)
	if acked("a") || len(s.posted()) != 0 {
		t.Fatalf("after a 500, the envelope was acknowledged: %v, and the bot posted %v", acked("a"), s.posted())
	}
	s.says("a", lost)
	w.until("the resent envelope received", func() bool { return acked("a") })
	w.until("the answer", func() bool { return len(s.posted()) >= 1 })
	s.says("c", lost)
	w.until("the second resend received", func() bool { return acked("c") })
	time.Sleep(20 * interval)
	if got := s.posted(); len(got) != 1 || got[0]["channel"] != "C7" {
		t.Fatalf("the bot posted %v", got)
	}
}

// A message Telegram gives while getUpdates fails once, with a 500, is not
// lost: the listener asks again after its rest, and the message is answered
// once. A message with no chat, which can never be handled, is passed over.
func TestTelegramListenerLosesNoMessageToAFailure(t *testing.T) {
	tg := newTelegram(t)
	var mu sync.Mutex
	asks, failed := 0, false
	override(tg.Server, func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		asks++
		if !failed && len(tg.updates) > 0 {
			failed = true
			w.WriteHeader(500)
			fmt.Fprint(w, "upstream error")
			return true
		}
		return false
	})
	asked := func() int {
		mu.Lock()
		defer mu.Unlock()
		return asks
	}
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": echoAgent("helper"),
		"agents/turn.js":     turnJS,
	})
	tg.update(map[string]any{"message": map[string]any{"message_id": 5, "from": map[string]any{"id": 1, "is_bot": false}, "text": "no chat"}})
	tg.dm(1, "after it")
	w.sync(dir)
	w.until("getUpdates failed", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return failed
	})
	for i := 0; i < 5 && !slices.ContainsFunc(tg.texts("1"), func(s string) bool { return strings.Contains(s, "after it") }); i++ {
		tick(w, asked)
	}
	time.Sleep(20 * interval)
	if got := tg.texts("1"); len(got) != 1 {
		t.Fatalf("the bot said %q", got)
	}
}

// GITLAB_SITE and JIRA_SITE given with https:// fail the listener, saying
// each is a host name.
func TestSiteWithASchemeIsExplained(t *testing.T) {
	for _, c := range []struct{ bridge, value, want string }{
		{"gitlab", "https://gitlab.example.com", "GITLAB_SITE is a host name, such as gitlab.example.com, with no https:// and no path: it is https://gitlab.example.com"},
		{"jira", "https://example.atlassian.net/", "JIRA_SITE is a host name, such as example.atlassian.net, with no https:// and no path: it is https://example.atlassian.net/"},
	} {
		t.Run(c.bridge, func(t *testing.T) {
			w := newWorld(t)
			srv := httptest.NewServer(http.NotFoundHandler())
			t.Cleanup(srv.Close)
			dir := pointed(t, c.bridge, srv)
			w.secret(strings.ToUpper(c.bridge)+"_TOKEN", "token")
			w.value("JIRA_EMAIL", "bot@example.com")
			w.value(strings.ToUpper(c.bridge)+"_SITE", c.value)
			w.sync(dir)
			w.until(c.want, func() bool {
				return slices.ContainsFunc(failures(w), func(s string) bool { return strings.Contains(s, c.want) })
			})
		})
	}
}

// A file whose address is not on Slack's own file host is left out of the
// message, and the bot token is never sent to its host.
func TestSlackFileOnAnotherHostGetsNoToken(t *testing.T) {
	other := &headers{}
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "secret file") }))
	t.Cleanup(elsewhere.Close)
	other.wrap(elsewhere)
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	echoing(t, dir)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	e := dm("U1", "3.0", "look at this")
	e["subtype"] = "file_share"
	e["files"] = []any{map[string]any{"id": "F1", "name": "x.txt", "mimetype": "text/plain", "size": 11, "url_private_download": elsewhere.URL + "/x.txt"}}
	s.says("a", e)
	w.until("the answer", func() bool { return len(s.posted()) >= 1 })
	time.Sleep(10 * interval)
	if seen := other.all(); len(seen) != 0 {
		t.Fatalf("the other host was asked %v", seen)
	}
	for _, run := range stubs(t, w, "route") {
		if in := fmt.Sprint(w.show(run.ID).Input); strings.Contains(in, elsewhere.URL) {
			t.Errorf("the message names the other host's file: %s", in)
		}
	}
}

// A message that is only the bot's name is answered, with nothing said.
func TestSlackNameAloneIsHeard(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	withHelp(t, dir)
	write(t, dir, map[string]string{"agents/helper.yaml": `
		statechart: helper
		input: { said: string, asker: string, home: string, conversation: "any?" }
		initial: e
		states:
		  e: { end: done, output: { reply: "said [${input.said}]" } }
	`})
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", inChannel("U1", "4.0", "", "<@UBOT>"))
	w.until("an answer", func() bool { return len(s.posted()) >= 1 })
	if got := s.posted()[0]["markdown_text"]; got != "said []" {
		t.Errorf("the bot said %v", got)
	}
}

// An edited message that comes to name the bot is not heard, and one deleted
// is not either: Slack's message_changed and message_deleted are not messages.
func TestSlackEditsAreNotHeard(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	withHelp(t, dir)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "subtype": "message_changed", "channel": "C1", "channel_type": "channel", "ts": "5.0",
		"message": map[string]any{"user": "U1", "ts": "4.0", "text": "<@UBOT> edited"}})
	s.says("b", map[string]any{"type": "message", "subtype": "message_deleted", "channel": "C1", "channel_type": "channel", "ts": "6.0"})
	s.says("c", dm("U1", "7.0", "then this"))
	w.until("the answer", func() bool { return len(s.posted()) >= 1 })
	time.Sleep(20 * interval)
	if got := s.posted(); len(got) != 1 || got[0]["markdown_text"] != "then this" {
		t.Fatalf("the bot posted %v", got)
	}
}

// A long answer is said in two messages. When Slack fails the second post once,
// only that post is tried again, and the person reads each part once.
func TestSlackLongAnswerIsNotSaidTwice(t *testing.T) {
	s := newSlack(t)
	var mu sync.Mutex
	posts := 0
	override(s.Server, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/chat.postMessage" {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		posts++
		if posts == 2 {
			w.WriteHeader(500)
			fmt.Fprint(w, "upstream error")
			return true
		}
		return false
	})
	w := newWorld(t)
	dir := slackProject(t, w, s)
	long := strings.Repeat("a", 11000) + "\n" + strings.Repeat("b", 3000)
	replying(t, dir, long)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", dm("U1", "3.0", "tell me everything"))
	w.until("an answer", func() bool { return len(s.posted()) >= 2 })
	time.Sleep(30 * interval)
	var firsts int
	for _, p := range s.posted() {
		if strings.HasPrefix(fmt.Sprint(p["markdown_text"]), "aaaa") {
			firsts++
		}
	}
	if firsts != 1 {
		t.Fatalf("the first part of the answer was posted %d times in %d posts", firsts, len(s.posted()))
	}
}

// alice asks on Slack for a held tool. The approval asks her by a Slack button,
// her press lets the tool run, and the message can be followed: listener, chat,
// conversation, agent, the tool and its approval, and the question, which names
// the approval that called it. The agent's run lists the tools it used.
func TestSlackApprovalByButtonCanBeFollowed(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir, model := slackAgentProject(t, w, s)
	replies := []llmstandin.Reply{
		{Calls: []llmstandin.ToolCall{{ID: "c1", Name: "open", Args: `{"part": "messages"}`}}},
		{Calls: []llmstandin.ToolCall{{ID: "c2", Name: "tell", Args: `{"person": "bob", "text": "hello bob"}`}}},
		{Text: "told"},
	}
	var mu sync.Mutex
	turn := 0
	model.Answer = func(llmstandin.Call) llmstandin.Reply {
		mu.Lock()
		defer mu.Unlock()
		turn++
		return replies[min(turn, len(replies))-1]
	}
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", dm("U1", "3.0", "tell bob hello"))
	w.until("the question", func() bool {
		for _, p := range s.posted() {
			if p["blocks"] != nil {
				return true
			}
		}
		return false
	})
	var question map[string]any
	for _, p := range s.posted() {
		if p["blocks"] != nil {
			question = p
		}
	}
	if question["channel"] != "D9" {
		t.Errorf("the question went to %v", question["channel"])
	}
	blocks, _ := json.Marshal(question["blocks"])
	if !strings.Contains(string(blocks), "tell") || !strings.Contains(string(blocks), "hello bob") {
		t.Errorf("the question does not show the held call: %s", blocks)
	}
	var yes string
	for _, part := range strings.Split(string(blocks), `"value":"`)[1:] {
		v := strings.Split(part, `"`)[0]
		if strings.HasSuffix(v, " yes") {
			yes = v
		}
	}
	if yes == "" {
		t.Fatalf("no yes button in %s", blocks)
	}
	s.send(map[string]any{"envelope_id": "p1", "type": "interactive", "payload": map[string]any{
		"type": "block_actions", "user": map[string]any{"id": "U1"}, "actions": []any{map[string]any{"action_id": "choice-0", "value": yes}},
		"channel": map[string]any{"id": "D9"}, "message": map[string]any{"ts": "1.1"},
	}})
	w.until("bob told and alice answered", func() bool {
		var bob, told bool
		for _, p := range s.posted() {
			bob = bob || p["markdown_text"] == "hello bob"
			told = told || p["markdown_text"] == "told"
		}
		return bob && told
	})

	sent := func(from, kind, at string) []string {
		var out []string
		for _, ask := range w.asked(from, kind) {
			if ask.Location == at {
				out = append(out, ask.Run)
			}
		}
		return out
	}
	listeners := listenerRuns(w, "slack")
	if len(listeners) == 0 {
		t.Fatal("no listener")
	}
	listener := listeners[0].ID
	chats := sent(listener, instance.AskEvent, location(".rook/blocks/chat"))
	if len(chats) == 0 {
		t.Fatal("the listener's record names no chat")
	}
	chat := chats[0]
	if run := w.show(chat); run.StartedBy != listener || run.Key != "slack:D1" {
		t.Errorf("the chat is %+v", run)
	}
	conversations := sent(chat, instance.AskEvent, location(".rook/blocks/conversation"))
	if len(conversations) == 0 {
		t.Fatal("the chat's record names no conversation")
	}
	agents := sent(conversations[0], instance.AskCall, location("agents/helper"))
	if len(agents) != 1 {
		t.Fatalf("the conversation called %v", agents)
	}
	runs, err := w.List(context.Background(), client.Filter{Parent: agents[0], Finished: true})
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]string{}
	for _, run := range runs {
		tools[run.Machine] = run.ID
	}
	if tools["tell"] == "" || tools["approval"] == "" {
		t.Fatalf("the agent's runs are %v", tools)
	}
	asked := sent(tools["approval"], instance.AskCall, location(".rook/blocks/ask"))
	if len(asked) != 1 || w.show(asked[0]).StartedBy != tools["approval"] {
		t.Errorf("the approval's record names the questions %v", asked)
	}
	// The listener's record names the question the press went to.
	pressed := false
	for _, ask := range w.asked(listener, instance.AskEvent) {
		if ask.Location == location(".rook/blocks/ask") || (len(asked) == 1 && ask.To == asked[0]) {
			pressed = true
		}
	}
	if !pressed {
		t.Errorf("the listener's record does not name the question its press went to")
	}
}

// A question to two people on Slack goes to each in a direct message. One's
// press answers it, and both messages lose their buttons and show who answered.
func TestSlackQuestionToTwoPeopleClosesBoth(t *testing.T) {
	s := newSlack(t)
	var mu sync.Mutex
	opened := 0
	override(s.Server, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/conversations.open" {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		opened++
		fmt.Fprintf(w, `{"ok": true, "channel": {"id": "D%d"}}`, 10+opened)
		return true
	})
	var tsMu sync.Mutex
	ts := 0
	override(s.Server, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/chat.postMessage" {
			return false
		}
		var post map[string]any
		json.NewDecoder(r.Body).Decode(&post)
		s.mu.Lock()
		s.posts = append(s.posts, post)
		s.mu.Unlock()
		tsMu.Lock()
		ts++
		fmt.Fprintf(w, `{"ok": true, "ts": "%d.5"}`, ts)
		tsMu.Unlock()
		return true
	})
	w := newWorld(t)
	dir := slackProject(t, w, s)
	write(t, dir, map[string]string{"on/asker.yaml": `
		flow: asker
		allow: [send:.rook/blocks/ask]
		stages:
		  asking: { job: { call: { at: .rook/blocks/ask }, input: { people: [alice, bob], text: "Ship it?", choices: ["yes", "no"] } } }
		output: ${stages.asking.output.choice}
	`})
	w.sync(dir)
	asker := w.start("on/asker.yaml", nil)
	w.until("Slack connected", s.connected)
	w.until("two questions", func() bool { return len(s.posted()) == 2 })
	blocks, _ := json.Marshal(s.posted()[0]["blocks"])
	value := strings.Split(strings.Split(string(blocks), `"value":"`)[1], `"`)[0]
	value = value[:strings.LastIndex(value, " ")] + " yes"
	s.send(map[string]any{"envelope_id": "p1", "type": "interactive", "payload": map[string]any{
		"type": "block_actions", "user": map[string]any{"id": "U2"}, "actions": []any{map[string]any{"action_id": "choice-0", "value": value}},
	}})
	w.until("the asker done", func() bool { return w.show(asker).Status == "done" })
	w.until("both closed", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.updates) >= 2
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	channels := map[any]bool{}
	for _, u := range s.updates {
		channels[u["channel"]] = true
		if u["text"] != "Ship it?\n\nbob said yes." {
			t.Errorf("a question was changed to %v", u)
		}
	}
	if len(channels) != 2 {
		t.Errorf("the questions changed are %v", s.updates)
	}
}

// A comment that ends a sentence with the bot's name, as "can you look @bot.",
// names the bot, as GitLab itself takes it, and is answered.
func TestGitLabNameAtTheEndOfASentenceIsHeard(t *testing.T) {
	gl := newGitLab(t)
	w := newWorld(t)
	w.secret("GITLAB_TOKEN", "gl-token")
	dir := pointed(t, "gitlab", gl.Server)
	w.value("GITLAB_SITE", strings.TrimPrefix(gl.URL, "http://"))
	gl.note(1, 2, "al", "before")
	w.sync(dir)
	w.until("the first ask", func() bool { return gl.asked() > 0 })
	gl.note(2, 2, "al", "The build fails, can you look @bot.")
	gl.note(3, 2, "al", "Thanks, @bot!")
	tick(w, gl.asked)
	tick(w, gl.asked)
	time.Sleep(20 * interval)
	got := gl.said()
	if len(got) != 2 {
		t.Fatalf("of a comment ending '@bot.' and one ending '@bot!', the bot answered %d: %q", len(got), got)
	}
}

// Two projects are watched. A comment on each goes to its own chat, keyed by
// the project and issue, with the project as its place, and is answered on its
// own issue.
func TestGitLabTwoProjectsAreTwoChats(t *testing.T) {
	gl := newGitLab(t)
	var mu sync.Mutex
	other := []map[string]any{}
	var otherNotes []string
	override(gl.Server, func(w http.ResponseWriter, r *http.Request) bool {
		path := r.URL.EscapedPath()
		if !strings.Contains(path, "group%2Fother") {
			return false
		}
		if r.Header.Get("PRIVATE-TOKEN") != "gl-token" {
			w.WriteHeader(401)
			return true
		}
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(path, "/events"):
			if r.URL.Query().Get("page") > "1" {
				fmt.Fprint(w, `[]`)
				return true
			}
			json.NewEncoder(w).Encode(other)
		case r.Method == "GET" && strings.HasSuffix(path, "/notes"):
			fmt.Fprint(w, `[]`)
		case r.Method == "GET" && strings.HasSuffix(path, "/issues/3"):
			fmt.Fprint(w, `{"title": "Other", "description": "x", "author": {"username": "al"}}`)
		case r.Method == "POST" && strings.HasSuffix(path, "/issues/3/notes"):
			var body struct{ Body string }
			json.NewDecoder(r.Body).Decode(&body)
			otherNotes = append(otherNotes, body.Body)
			fmt.Fprint(w, `{"id": 5}`)
		default:
			w.WriteHeader(404)
		}
		return true
	})
	w := newWorld(t)
	w.secret("GITLAB_TOKEN", "gl-token")
	dir := pointed(t, "gitlab", gl.Server)
	w.value("GITLAB_SITE", strings.TrimPrefix(gl.URL, "http://"))
	setSite(t, dir, "bridges/gitlab.yaml", "projects: [group/project]", "projects: [group/project, group/other]")
	setSite(t, dir, "rules.yaml", "  OPS:", "  group/other: { readers: [asker] }\n  OPS:")
	gl.note(1, 2, "al", "before")
	w.sync(dir)
	w.until("the first ask", func() bool { return gl.asked() > 0 })
	gl.note(2, 2, "al", "@bot on the first")
	mu.Lock()
	other = []map[string]any{{"id": 1, "author_id": 2, "author_username": "al", "note": map[string]any{"id": 1, "noteable_type": "Issue", "noteable_iid": 3, "body": "@bot on the other"}}}
	mu.Unlock()
	// The first ask of the other project marked where it stood, so the note
	// there must come after.
	tick(w, gl.asked)
	mu.Lock()
	other = append([]map[string]any{{"id": 2, "author_id": 2, "author_username": "al", "note": map[string]any{"id": 2, "noteable_type": "Issue", "noteable_iid": 3, "body": "@bot again on the other"}}}, other...)
	mu.Unlock()
	both := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(gl.said()) >= 1 && len(otherNotes) >= 1
	}
	for end := time.Now().Add(5 * time.Second); !both() && time.Now().Before(end); {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
	}
	if !both() {
		mu.Lock()
		t.Fatalf("the first issue was told %q and the other %q; failures:\n%s", gl.said(), otherNotes, strings.Join(failures(w), "\n"))
	}
	chats := keyed(w, ".rook/blocks/chat.yaml")
	if chats["gitlab:group/project#7"] == nil || chats["gitlab:group/other#3"] == nil {
		var keys []string
		for k := range chats {
			keys = append(keys, k)
		}
		t.Errorf("the chats are %v", keys)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(otherNotes, "|"), "on the other") || strings.Contains(strings.Join(gl.said(), "|"), "other") {
		t.Errorf("the first issue was told %q and the other %q", gl.said(), otherNotes)
	}
}

// A comment on a merge request naming the bot is not heard, since the bridge
// hears issues, and nothing fails.
func TestGitLabMergeRequestCommentIsNotHeard(t *testing.T) {
	gl := newGitLab(t)
	w := newWorld(t)
	w.secret("GITLAB_TOKEN", "gl-token")
	dir := pointed(t, "gitlab", gl.Server)
	w.value("GITLAB_SITE", strings.TrimPrefix(gl.URL, "http://"))
	gl.note(1, 2, "al", "before")
	w.sync(dir)
	w.until("the first ask", func() bool { return gl.asked() > 0 })
	gl.happens(2, map[string]any{"author_id": 2, "author_username": "al", "note": map[string]any{"id": 2, "noteable_type": "MergeRequest", "noteable_iid": 4, "body": "@bot review this"}})
	gl.note(3, 2, "al", "@bot an issue")
	tick(w, gl.asked)
	w.until("the answer", func() bool { return len(gl.said()) >= 1 })
	tick(w, gl.asked)
	if got := gl.said(); len(got) != 1 || !strings.Contains(got[0], "an issue") {
		t.Fatalf("the bot said %q", got)
	}
}

// With overhear, a stranger's comment that does not name the bot is not told
// the bot is not theirs, and calls no attend: Rook reads nothing for someone
// with no agent.
func TestGitLabOverheardStrangerIsNotRefused(t *testing.T) {
	gl := newGitLab(t)
	w := newWorld(t)
	w.secret("GITLAB_TOKEN", "gl-token")
	dir := pointed(t, "gitlab", gl.Server)
	w.value("GITLAB_SITE", strings.TrimPrefix(gl.URL, "http://"))
	setSite(t, dir, "bridges/gitlab.yaml", "projects: [group/project] }", "projects: [group/project], overhear: true }")
	if err := os.WriteFile(filepath.Join(dir, "attend.yaml"), []byte(dedent(attendStub)), 0o644); err != nil {
		t.Fatal(err)
	}
	gl.note(1, 2, "al", "before")
	w.sync(dir)
	w.until("the first ask", func() bool { return gl.asked() > 0 })
	gl.note(2, 3, "stranger", "please fix this")
	gl.note(3, 2, "al", "please, bot")
	gl.note(4, 2, "al", "just talking")
	tick(w, gl.asked)
	w.until("the answer", func() bool { return len(gl.said()) >= 1 })
	tick(w, gl.asked)
	time.Sleep(20 * interval)
	if got := gl.said(); len(got) != 1 || !strings.Contains(got[0], "please, bot") {
		t.Fatalf("the bot said %q", got)
	}
	if n := len(stubs(t, w, "attend-stub")); n != 2 {
		t.Errorf("attend was called %d times, and not for alice's two comments only", n)
	}
}

// A project with no GITLAB_SITE: the listener fails, and its error names the
// value and where to set it.
func TestGitLabWithNoSiteNamesTheValue(t *testing.T) {
	gl := newGitLab(t)
	w := newWorld(t)
	w.secret("GITLAB_TOKEN", "gl-token")
	dir := pointed(t, "gitlab", gl.Server)
	w.sync(dir)
	w.kept("bridges/gitlab.yaml")
	var errs string
	w.until("the listener fails", func() bool {
		errs = strings.Join(failures(w), "\n")
		return strings.Contains(errs, "GITLAB_SITE")
	})
	if !strings.Contains(errs, "/project") {
		t.Errorf("the error does not say where the value is looked for: %s", errs)
	}
	if gl.asked() != 0 {
		t.Errorf("GitLab was asked")
	}
	// Setting the value now: the page says to cancel the bridge and submit.
	w.value("GITLAB_SITE", strings.TrimPrefix(gl.URL, "http://"))
	bridge := w.kept("bridges/gitlab.yaml")
	if _, err := w.Cancel(context.Background(), bridge, 0); err != nil {
		t.Fatal(err)
	}
	w.until("the bridge cancelled", func() bool { return w.show(bridge).Status == "cancelled" })
	w.sync(dir)
	w.until("GitLab asked once the value is set", func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return gl.asked() > 0
	})
}

// A project with no GITLAB_TOKEN: the listener fails before anything is sent,
// naming the secret, and GitLab is never asked.
func TestGitLabWithNoTokenNamesTheSecret(t *testing.T) {
	gl := newGitLab(t)
	w := newWorld(t)
	dir := pointed(t, "gitlab", gl.Server)
	w.value("GITLAB_SITE", strings.TrimPrefix(gl.URL, "http://"))
	w.sync(dir)
	w.kept("bridges/gitlab.yaml")
	w.until("the listener fails naming the token", func() bool {
		return strings.Contains(strings.Join(failures(w), "\n"), "GITLAB_TOKEN")
	})
	if gl.asked() != 0 {
		t.Errorf("GitLab was asked")
	}
}

// The GitLab token goes only to the host GITLAB_SITE names: a comment linking
// an upload on another host is not downloaded there, and that host sees no
// token.
func TestGitLabTokenGoesOnlyToTheSite(t *testing.T) {
	other := &headers{}
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "x") }))
	t.Cleanup(elsewhere.Close)
	other.wrap(elsewhere)
	gl := newGitLab(t)
	w := newWorld(t)
	dir, _ := gitlabProject(t, w, gl)
	_ = dir
	gl.note(2, 2, "al", "@bot see ![a]("+elsewhere.URL+"/uploads/"+secret1+"/a.png) and [b](//"+strings.TrimPrefix(elsewhere.URL, "http://")+"/uploads/"+secret1+"/b.txt)")
	answered(w, gl)
	if seen := other.all(); len(seen) != 0 {
		t.Fatalf("the other host was asked %v", seen)
	}
}

// A project with no JIRA_EMAIL: the listener fails naming it, and Jira is not
// asked.
func TestJiraWithNoEmailNamesTheValue(t *testing.T) {
	j := newJira(t)
	w := newWorld(t)
	w.secret("JIRA_TOKEN", "jira-token")
	dir := pointed(t, "jira", j.Server)
	w.value("JIRA_SITE", strings.TrimPrefix(j.URL, "http://"))
	w.sync(dir)
	w.kept("bridges/jira.yaml")
	w.until("the listener fails naming JIRA_EMAIL", func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return strings.Contains(strings.Join(failures(w), "\n"), "JIRA_EMAIL")
	})
}

// Two Jira projects are asked about in one search, and a comment on each is its
// own chat, with the project's key as its place.
func TestJiraTwoProjectsAreAsked(t *testing.T) {
	j := newJira(t)
	var mu sync.Mutex
	var jqls []string
	override(j.Server, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/rest/api/2/search/jql" {
			mu.Lock()
			jqls = append(jqls, r.URL.Query().Get("jql"))
			mu.Unlock()
		}
		return false
	})
	w := newWorld(t)
	w.secret("JIRA_TOKEN", "jira-token")
	dir := pointed(t, "jira", j.Server)
	jiraAt(w, j)
	setSite(t, dir, "bridges/jira.yaml", "projects: [OPS]", "projects: [OPS, DEV]")
	w.sync(dir)
	tick(w, j.asked)
	tick(w, j.asked)
	mu.Lock()
	defer mu.Unlock()
	if len(jqls) == 0 || !strings.Contains(jqls[len(jqls)-1], `project in ("OPS", "DEV")`) {
		t.Fatalf("Jira was asked %q", jqls)
	}
}

// An issue opened naming the bot is heard as it is, and answered on the issue.
func TestJiraOpenedIssueNamingTheBotIsAnswered(t *testing.T) {
	j := newJira(t)
	w := newWorld(t)
	w.secret("JIRA_TOKEN", "jira-token")
	dir := pointed(t, "jira", j.Server)
	jiraAt(w, j)
	w.sync(dir)
	tick(w, j.asked)
	at := time.Now().Add(time.Second)
	j.mu.Lock()
	j.fields = map[string]any{"created": jiraTime(at), "updated": jiraTime(time.Now().Add(-time.Hour)), "description": "[~accountid:acc-bot] the disk is full"}
	j.mu.Unlock()
	tick(w, j.asked)
	w.until("the answer", func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return len(j.saying()) >= 1
	})
	tick(w, j.asked)
	time.Sleep(10 * interval)
	if got := j.saying(); len(got) != 1 || !strings.Contains(got[0], "the disk is full") || strings.Contains(got[0], "accountid") {
		t.Fatalf("the bot said %q", got)
	}
}

// The Jira token goes only to JIRA_SITE: a file's download that Jira would
// redirect elsewhere is asked without a redirect, and nothing is asked of the
// other host.
func TestJiraTokenGoesOnlyToTheSite(t *testing.T) {
	j := newJira(t)
	seen := &headers{}
	seen.wrap(j.Server)
	w, _, next := jiraAnswering(t, j)
	at := time.Now().Add(time.Second)
	j.mu.Lock()
	j.files = []map[string]any{attachment("100", "log.txt", "text/plain", 5, "acc-1", at)}
	j.contents = map[string]string{"100": "hello"}
	j.mu.Unlock()
	j.comment("1", "acc-1", "[~accountid:acc-bot] see [^log.txt]", at)
	next()
	w.until("the answer", func() bool { return len(j.saying()) >= 1 })
	for _, line := range seen.all() {
		if strings.Contains(line, "/attachment/content/") && !strings.Contains(line, "auth=Basic") {
			t.Errorf("a download was asked without the token: %s", line)
		}
	}
	if got := j.downloaded(); len(got) != 1 {
		t.Errorf("downloaded %v", got)
	}
}

// One project runs Slack, GitLab and Jira at once. alice is answered on each
// where she asked, in a chat of its own, and each service sees only its own
// token.
func TestEveryBridgeAtOnce(t *testing.T) {
	s, gl, j := newSlack(t), newGitLab(t), newJira(t)
	sh, gh, jh := &headers{}, &headers{}, &headers{}
	sh.wrap(s.Server)
	gh.wrap(gl.Server)
	jh.wrap(j.Server)
	w := newWorld(t)
	dir := everyBridge(t, w, s, gl, j)
	gl.note(1, 2, "al", "before")
	w.sync(dir)
	for _, b := range []string{"slack", "gitlab", "jira"} {
		w.kept("bridges/" + b + ".yaml")
	}
	w.until("Slack connected", s.connected)
	w.until("GitLab asked", func() bool { return gl.asked() > 0 })
	tick(w, j.asked)
	s.says("a", dm("U1", "3.0", "on slack"))
	gl.note(2, 2, "al", "@bot on gitlab")
	j.comment("1", "acc-1", "[~accountid:acc-bot] on jira", time.Now().Add(time.Second))
	w.until("each answered", func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return len(s.posted()) >= 1 && len(gl.said()) >= 1 && len(j.saying()) >= 1
	})
	if s.posted()[0]["markdown_text"] != "on slack" || gl.said()[0] != "on gitlab" || j.saying()[0] != "on jira" {
		t.Errorf("Slack was told %v, GitLab %q, Jira %q", s.posted()[0]["markdown_text"], gl.said(), j.saying())
	}
	chats := keyed(w, ".rook/blocks/chat.yaml")
	for _, key := range []string{"slack:D1", "gitlab:group/project#7", "jira:OPS-12"} {
		if chats[key] == nil {
			t.Errorf("no chat %s", key)
		}
	}
	for _, line := range sh.all() {
		if strings.Contains(line, "gl-token") || strings.Contains(line, "Basic") {
			t.Errorf("Slack was sent another service's token: %s", line)
		}
	}
	for _, line := range gh.all() {
		if strings.Contains(line, "xox") || strings.Contains(line, "Basic") {
			t.Errorf("GitLab was sent another service's token: %s", line)
		}
	}
	for _, line := range jh.all() {
		if strings.Contains(line, "xox") || strings.Contains(line, "gl-token") {
			t.Errorf("Jira was sent another service's token: %s", line)
		}
	}
}

// alice asks on a GitLab issue for a held tool. GitLab asks no questions, so
// the approval asks her on Slack, where she is reached directly, and her press
// there lets the tool tell bob on Slack. The answer goes back to the issue.
func TestGitLabRequestApprovedOnSlack(t *testing.T) {
	s, gl, j := newSlack(t), newGitLab(t), newJira(t)
	w := newWorld(t)
	dir := everyBridge(t, w, s, gl, j)
	w.secret("TEST_KEY", "sk-test")
	replies := []llmstandin.Reply{
		{Calls: []llmstandin.ToolCall{{ID: "c1", Name: "open", Args: `{"part": "messages"}`}}},
		{Calls: []llmstandin.ToolCall{{ID: "c2", Name: "tell", Args: `{"person": "bob", "text": "hello bob"}`}}},
		{Text: "told"},
	}
	var mu sync.Mutex
	turn := 0
	model := &llmstandin.Server{Answer: func(llmstandin.Call) llmstandin.Reply {
		mu.Lock()
		defer mu.Unlock()
		turn++
		return replies[min(turn, len(replies))-1]
	}}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	gl.note(1, 2, "al", "before")
	w.sync(dir)
	w.until("Slack connected", s.connected)
	w.until("GitLab asked", func() bool { return gl.asked() > 0 })
	gl.note(2, 2, "al", "@bot tell bob hello")
	w.until("the question on Slack", func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		for _, p := range s.posted() {
			if p["blocks"] != nil {
				return true
			}
		}
		return false
	})
	var blocks []byte
	for _, p := range s.posted() {
		if p["blocks"] != nil {
			blocks, _ = json.Marshal(p["blocks"])
		}
	}
	var yes string
	for _, part := range strings.Split(string(blocks), `"value":"`)[1:] {
		if v := strings.Split(part, `"`)[0]; strings.HasSuffix(v, " yes") {
			yes = v
		}
	}
	s.send(map[string]any{"envelope_id": "p1", "type": "interactive", "payload": map[string]any{
		"type": "block_actions", "user": map[string]any{"id": "U1"}, "actions": []any{map[string]any{"action_id": "choice-0", "value": yes}},
	}})
	w.until("told on GitLab", func() bool { return len(gl.said()) >= 1 })
	if got := gl.said(); got[0] != "told" {
		t.Errorf("the issue was told %q", got)
	}
	bob := false
	for _, p := range s.posted() {
		bob = bob || p["markdown_text"] == "hello bob"
	}
	if !bob {
		t.Errorf("bob was not told on Slack: %v", s.posted())
	}
}

// In a project with only GitLab, a question to a person whose people.yaml entry
// has a Slack id fails at once, saying once that the project does not run Slack.
func TestGitLabOnlyProjectAskingASlackPersonFailsClearly(t *testing.T) {
	gl := newGitLab(t)
	w := newWorld(t)
	w.secret("GITLAB_TOKEN", "gl-token")
	dir := pointed(t, "gitlab", gl.Server)
	w.value("GITLAB_SITE", strings.TrimPrefix(gl.URL, "http://"))
	write(t, dir, map[string]string{"on/asker.yaml": `
		flow: asker
		allow: [send:.rook/blocks/ask]
		stages:
		  asking: { job: { call: { at: .rook/blocks/ask }, input: { people: [alice], text: "Well?", choices: ["yes", "no"] } } }
		output: ${stages.asking.output.choice}
	`})
	w.sync(dir)
	asker := w.start("on/asker.yaml", nil)
	var run *client.Run
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run = w.show(asker)
		if run.Status != "running" && run.Status != "waiting" {
			break
		}
		time.Sleep(interval)
	}
	if run.Status != "failed" {
		var qs []string
		for _, q := range w.at(".rook/blocks/ask.yaml") {
			qs = append(qs, q.Machine+" "+q.Status+" "+w.show(q.ID).Error)
		}
		t.Fatalf("the asker is %s after 5s; the question's runs: %v; failures: %v", run.Status, qs, failures(w))
	}
	if !strings.Contains(run.Error, "people.yaml reaches alice on slack, which this project does not run") || strings.Count(run.Error, " at line ") != 1 {
		t.Errorf("the asker failed with %q", run.Error)
	}
}

// Starter with no model

// init's reminders

// A Slack project with no SLACK_APP_TOKEN: the listener fails before anything
// is sent, naming the secret.
func TestSlackWithNoAppTokenNamesTheSecret(t *testing.T) {
	s := newSlack(t)
	seen := &headers{}
	seen.wrap(s.Server)
	w := newWorld(t)
	dir := pointed(t, "slack", s.Server, slackHosts...)
	w.secret("SLACK_TOKEN", "xoxb-bot")
	w.sync(dir)
	w.kept("bridges/slack.yaml")
	w.until("the listener fails naming SLACK_APP_TOKEN", func() bool {
		return strings.Contains(strings.Join(failures(w), "\n"), "SLACK_APP_TOKEN is not set")
	})
	if got := seen.all(); len(got) != 0 {
		t.Errorf("Slack was asked %v", got)
	}
}

// In a project with only Jira, a question to a person known only on Jira fails
// at once, saying they cannot be reached directly.
func TestJiraOnlyQuestionFailsSayingWhy(t *testing.T) {
	j := newJira(t)
	w := newWorld(t)
	w.secret("JIRA_TOKEN", "jira-token")
	dir := pointed(t, "jira", j.Server)
	jiraAt(w, j)
	write(t, dir, map[string]string{
		"people.yaml": "people:\n  alice: { jira: acc-1 }\ngroups: {}\n",
		"on/asker.yaml": `
			flow: asker
			allow: [send:.rook/blocks/ask]
			stages:
			  asking: { job: { call: { at: .rook/blocks/ask }, input: { people: [alice], text: "Well?", choices: ["yes", "no"] } } }
			output: ${stages.asking.output.choice}
		`,
	})
	w.sync(dir)
	asker := w.start("on/asker.yaml", nil)
	w.until("the asker ends", func() bool { s := w.show(asker).Status; return s == "failed" || s == "done" })
	if run := w.show(asker); run.Status != "failed" || !strings.Contains(run.Error, "no way to reach alice directly") {
		t.Fatalf("the asker is %s: %s", run.Status, run.Error)
	}
}

// With overhear on Jira, a comment that does not name the bot goes to attend,
// and one that attend says yes to is answered on the issue.
func TestJiraOverheardCommentGoesToAttend(t *testing.T) {
	j := newJira(t)
	w := newWorld(t)
	w.secret("JIRA_TOKEN", "jira-token")
	dir := pointed(t, "jira", j.Server)
	jiraAt(w, j)
	setSite(t, dir, "bridges/jira.yaml", "projects: [OPS] }", "projects: [OPS], overhear: true }")
	if err := os.WriteFile(filepath.Join(dir, "attend.yaml"), []byte(dedent(attendStub)), 0o644); err != nil {
		t.Fatal(err)
	}
	w.sync(dir)
	tick(w, j.asked)
	at := time.Now().Add(time.Second)
	j.comment("1", "acc-1", "please look", at)
	j.comment("2", "acc-1", "just noting", at.Add(time.Millisecond))
	tick(w, j.asked)
	w.until("the answer", func() bool { return len(j.saying()) >= 1 })
	tick(w, j.asked)
	time.Sleep(10 * interval)
	if got := j.saying(); len(got) != 1 || got[0] != "please look" {
		t.Fatalf("the bot said %q", got)
	}
}

// In a project that runs Telegram and not Slack, a person whose people.yaml
// entry gives a Slack id before a Telegram one is asked on Telegram, the bridge
// that reaches them.
func TestQuestionGoesToABridgeTheProjectRuns(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
		"people.yaml": "people:\n  alice: { slack: U1, telegram: \"1\" }\ngroups: {}\n",
		"on/asker.yaml": `
			flow: asker
			allow: [send:.rook/blocks/ask]
			stages:
			  asking: { job: { call: { at: .rook/blocks/ask }, input: { people: [alice], text: "Well?", choices: ["yes", "no"] } } }
			output: ${stages.asking.output.choice}
		`,
	})
	removeListener(t, dir)
	w.sync(dir)
	asker := w.start("on/asker.yaml", nil)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && len(questions(w)) == 0 && w.show(asker).Status != "failed" {
		time.Sleep(interval)
	}
	if run := w.show(asker); run.Status == "failed" {
		t.Fatalf("alice, reached on Telegram, was not asked there: %s", run.Error)
	}
	if len(questions(w)) == 0 {
		t.Fatal("no question waits")
	}
}

// In a project that runs Telegram and not Slack, tell says the text to a person
// whose people.yaml entry gives a Slack id first on Telegram.
func TestTellGoesToABridgeTheProjectRuns(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
		"people.yaml": "people:\n  alice: { slack: U1, telegram: \"1\" }\ngroups: {}\n",
		"on/teller.yaml": `
			flow: teller
			stages:
			  telling: { job: { machine: .rook/blocks/tell.yaml, input: { person: alice, text: "hi" } } }
		`,
	})
	removeListener(t, dir)
	w.sync(dir)
	teller := w.start("on/teller.yaml", nil)
	w.until("the teller ends", func() bool { return w.show(teller).HasEnded() })
	if run := w.show(teller); run.Status != "done" {
		t.Fatalf("the teller is %s: %s", run.Status, run.Error)
	}
	if to := sayings(t, w)["hi"]["to"]; to != "1" {
		t.Fatalf("alice was told at %v", to)
	}
}

// A question whose bridge runs but fails is not asked on the person's next
// bridge: it fails with the bridge's error, given once.
func TestQuestionOnAFailingBridgeFailsWithItsError(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
		".rook/bridges/telegram/telegram.js": `if (input.do === "ask") throw new Error("Telegram is down")
			return {}`,
		"people.yaml": "people:\n  alice: { telegram: \"1\", slack: U1 }\ngroups: {}\n",
		"on/asker.yaml": `
			flow: asker
			allow: [send:.rook/blocks/ask]
			stages:
			  asking: { job: { call: { at: .rook/blocks/ask }, input: { people: [alice], text: "Well?", choices: ["yes", "no"] } } }
			output: ${stages.asking.output.choice}
		`,
	})
	removeListener(t, dir)
	w.sync(dir)
	asker := w.start("on/asker.yaml", nil)
	w.until("the asker ends", func() bool { return w.show(asker).HasEnded() })
	if run := w.show(asker); run.Status != "failed" || !strings.Contains(run.Error, "Telegram is down") {
		t.Fatalf("the asker is %s: %s", run.Status, run.Error)
	}
	if got := w.show(asker).Error; strings.Count(got, "Telegram is down") != 1 || strings.Count(got, " at line ") > 2 {
		t.Errorf("the asker's error repeats what failed: %s", got)
	}
}

// In a project that runs Telegram and Slack, tell says the text on the first
// bridge in the person's people.yaml entry: Telegram for alice, whose entry
// gives it first, and Slack for bob, whose entry gives Slack first.
func TestTellGoesToTheFirstBridgeInTheEntry(t *testing.T) {
	w := newWorld(t)
	dir := filepath.Join(t.TempDir(), "project")
	if err := exampleProject(dir, []string{"telegram", "slack"}); err != nil {
		t.Fatal(err)
	}
	write(t, dir, map[string]string{
		".rook/bridges/telegram/telegram.js": telegramScript,
		".rook/bridges/slack/slack.js":       `return input.do === "cut" ? [{ plain: input.text }] : { sent: input.to + ":1" }`,
		"people.yaml":                        "people:\n  alice: { telegram: \"1\", slack: U1 }\n  bob: { slack: U2, telegram: \"2\" }\ngroups: {}\n",
		"on/teller.yaml": `
			flow: teller
			stages:
			  alice: { job: { machine: .rook/blocks/tell.yaml, input: { person: alice, text: "hi alice" } } }
			  bob: { job: { machine: .rook/blocks/tell.yaml, input: { person: bob, text: "hi bob" } } }
		`,
	})
	for _, name := range []string{"telegram", "slack"} {
		if err := os.Remove(filepath.Join(dir, "bridges", name+".yaml")); err != nil {
			t.Fatal(err)
		}
	}
	w.secret("TELEGRAM_TOKEN", "123:abc")
	w.secret("SLACK_TOKEN", "xoxb-bot")
	w.sync(dir)
	teller := w.start("on/teller.yaml", nil)
	w.until("the teller ends", func() bool { return w.show(teller).HasEnded() })
	to := func(path string) []any {
		var out []any
		for _, run := range w.at(path) {
			out = append(out, w.show(run.ID).Input.(map[string]any)["to"])
		}
		return out
	}
	if got := to(".rook/bridges/telegram/say.yaml"); !reflect.DeepEqual(got, []any{"1"}) {
		t.Errorf("Telegram was told at %v", got)
	}
	if got := to(".rook/bridges/slack/say.yaml"); !reflect.DeepEqual(got, []any{"U2"}) {
		t.Errorf("Slack was told at %v", got)
	}
}
