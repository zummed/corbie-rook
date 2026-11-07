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

// everyService makes a project with a bot on Slack, GitLab and Jira, each
// pointed at its stand-in, with the agent at agents/assistant standing in.
func everyService(t *testing.T, w *world, s *slack, gl *gitlab, j *jira) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project")
	if err := exampleProject(dir, []string{"slack", "gitlab", "jira"}); err != nil {
		t.Fatal(err)
	}
	point(t, dir, "slack", s.Server, slackHosts...)
	point(t, dir, "gitlab", gl.Server)
	point(t, dir, "jira", j.Server)
	write(t, dir, map[string]string{"agents/assistant.yaml": helperStub})
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

// logged gives what the runs of a service's listener logged, in order.
func logged(w *world, service string) []string {
	var out []string
	for _, run := range listenerRuns(w, service) {
		for _, e := range w.record(run.ID) {
			if e.Name == "log" {
				out = append(out, fmt.Sprint(e.Data))
			}
		}
	}
	return out
}

// listenerRuns gives the runs of a service's listener, the children of its
// router, newest last.
func listenerRuns(w *world, service string) []*client.Run {
	var out []*client.Run
	for _, run := range w.at(router(service)) {
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

// A message naming the bot in a channel starts a thread, and a reply in that
// thread naming it again goes to the same conversation, and is answered in the
// thread.
func TestSlackThreadReplyKeepsItsConversation(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
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
	conversations := keyed(w, "conversation.yaml")
	if len(conversations) != 1 || len(conversations["slack:C1/4.0:U1:assistant"]) != 1 {
		t.Errorf("the conversations are %v", conversations)
	}
}

// A reply in a thread of a direct message has its own conversation, and is
// answered in that thread; the top of the direct message has another.
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
	conversations := keyed(w, "conversation.yaml")
	if conversations["slack:D1:U1:assistant"] == nil || conversations["slack:D1/3.0:U1:assistant"] == nil {
		t.Errorf("the conversations are %v", conversations)
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
	write(t, dir, map[string]string{"agents/asker.yaml": asker("slack", `{ in: U1, text: "Restart?", choices: ["yes", "no"], lookup: people/find, approvers: messages:approve }`)})
	w.sync(dir)
	asking := w.start("agents/asker.yaml", nil)
	w.until("Slack connected", s.connected)
	w.until("the question", func() bool { return len(s.posted()) == 1 })
	yes := value(t, s.posted()[0], "yes")
	s.press("p1", "U1", yes)
	s.press("p2", "U1", yes)
	w.until("the asker done", func() bool { return w.show(asking).Status == "done" })
	time.Sleep(30 * interval)
	if got, _ := json.Marshal(w.show(asking).Output); string(got) != `{"by":"alice","choice":"yes"}` || len(s.changed()) != 1 {
		t.Fatalf("the question gave %s, and changed %d times", got, len(s.changed()))
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

// A message whose channel Slack fails to name once, with a 500 or with a
// refusal such as invalid_auth, is left unacknowledged, so Slack sends it
// again; the resend is answered, and a later resend of it is not answered
// again. A message in a channel Slack always refuses to name is never
// acknowledged. A press with no user, which lacks what the listener reads, is
// acknowledged and skipped.
func TestSlackListenerLeavesAFailedEnvelopeForSlackToSendAgain(t *testing.T) {
	s := newSlack(t)
	var mu sync.Mutex
	failed := map[string]bool{}
	override(s.Server, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/conversations.info" {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		channel := r.URL.Query().Get("channel")
		switch {
		case channel == "C7" && !failed[channel]:
			failed[channel] = true
			w.WriteHeader(500)
			fmt.Fprint(w, "upstream error")
			return true
		case channel == "C8" && !failed[channel]:
			failed[channel] = true
			fmt.Fprint(w, `{"ok": false, "error": "invalid_auth"}`)
			return true
		case channel == "C9":
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
	in := func(channel, ts string) map[string]any {
		e := inChannel("U1", ts, "", "<@UBOT> "+channel)
		e["channel"] = channel
		return e
	}
	s.says("a", in("C7", "4.0"))
	s.says("b", in("C8", "5.0"))
	s.says("c", in("C9", "6.0"))
	s.send(map[string]any{"envelope_id": "d", "type": "interactive", "payload": map[string]any{
		"type": "block_actions", "actions": []any{map[string]any{"action_id": "choice-0", "value": "20260101T000000.000Z-0123456789 yes"}},
	}})
	w.until("the press with no user acknowledged", func() bool { return acked("d") })
	time.Sleep(10 * interval)
	if acked("a") || acked("b") || acked("c") || len(s.posted()) != 0 {
		t.Fatalf("a failed envelope was acknowledged: %v %v %v, and the bot posted %v", acked("a"), acked("b"), acked("c"), s.posted())
	}
	s.says("a2", in("C7", "4.0"))
	s.says("b2", in("C8", "5.0"))
	s.says("c2", in("C9", "6.0"))
	w.until("the resent envelopes received", func() bool { return acked("a2") && acked("b2") })
	w.until("the answers", func() bool { return len(s.posted()) >= 2 })
	s.says("a3", in("C7", "4.0"))
	s.says("b3", in("C8", "5.0"))
	w.until("the second resends received", func() bool { return acked("a3") && acked("b3") })
	time.Sleep(20 * interval)
	var channels []string
	for _, p := range s.posted() {
		channels = append(channels, fmt.Sprint(p["channel"]))
	}
	slices.Sort(channels)
	if !reflect.DeepEqual(channels, []string{"C7", "C8"}) || acked("c2") {
		t.Fatalf("the bot posted in %v, and the always refused envelope was acknowledged: %v", channels, acked("c2"))
	}
}

// A message Telegram gives while getUpdates fails once, with a 500, is not
// lost: the listener asks again after its rest, and the message is answered
// once. A message with no chat, which lacks what the listener reads, is logged
// and passed over.
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
	dir := chatProject(t, w, tg, nil)
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

// An update that the listener fails on after getUpdates gave it is given
// again, and answered once. The stand-in gives update 1 first as a photo with
// no sizes, which the listener fails on, then as the message it holds.
func TestTelegramUpdateTheListenerFailsOnIsGivenAgain(t *testing.T) {
	tg := newTelegram(t)
	var mu sync.Mutex
	broken := false
	override(tg.Server, func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/getUpdates") {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		if !broken && len(tg.updates) > 0 {
			broken = true
			fmt.Fprint(w, `{"ok": true, "result": [{"update_id": 1, "message": {"message_id": 10, "date": 1700000000, "from": {"id": 1, "is_bot": false}, "chat": {"id": 1, "type": "private"}, "photo": []}}]}`)
			return true
		}
		return false
	})
	w := newWorld(t)
	dir := chatProject(t, w, tg, nil)
	tg.dm(1, "given again")
	w.sync(dir)
	w.until("the message answered", func() bool { return len(tg.texts("1")) > 0 })
	time.Sleep(20 * interval)
	if got, l := tg.texts("1"), logged(w, "telegram"); len(got) != 1 || !strings.Contains(got[0], "given again") ||
		!slices.ContainsFunc(l, func(s string) bool { return strings.HasPrefix(s, "an update failed, and is given again: ") }) {
		t.Fatalf("the bot said %q; logged: %q", got, l)
	}
}

// An update the listener fails on each time, a photo with no sizes, is given
// again, then logged and passed over after its third failure. The message
// after it is answered once.
func TestTelegramUpdateThatAlwaysFailsIsPassedOver(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, nil)
	tg.dm(1, "", map[string]any{"photo": []any{}})
	tg.dm(1, "after it")
	w.sync(dir)
	w.until("the message after it answered", func() bool { return len(tg.texts("1")) > 0 })
	time.Sleep(20 * interval)
	var again, passed int
	for _, l := range logged(w, "telegram") {
		if strings.HasPrefix(l, "an update failed, and is given again: ") {
			again++
		}
		if strings.HasPrefix(l, "an update failed three times and is passed over: ") {
			passed++
		}
	}
	if got := tg.texts("1"); again != 2 || passed != 1 || len(got) != 1 || !strings.Contains(got[0], "after it") {
		t.Fatalf("the bad update was logged as given again %d times and as passed over %d times, and the bot said %q; logged: %q",
			again, passed, got, logged(w, "telegram"))
	}
}

// historyAgent stands in for the agent at agents/assistant: it replies with
// the history its message carries, each entry's line and files, whether that
// history is complete, and the message's text.
var historyAgent = map[string]string{
	"agents/assistant.yaml": `
		flow: assistant
		input: { messages: list, medium: string, asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
		stages:
		  turn: { job: { script: ./history.js, input: { message: "${input.messages[0]}" } } }
		output: ${stages.turn.output}
	`,
	"agents/history.js": `
		const m = input.message
		const lines = Array.from(blobs.items(m.history)).map((e) => e.line + e.attachments.map((a) => " +" + a.name).join(""))
		return { reply: (m.complete ? "complete" : "partial") + " [" + lines.join(" | ") + "] " + m.text }
	`,
}

// Telegram's listener keeps what it heard in each chat, and sends it with each
// message as its history: a blob of entries, oldest first, each with its files.
// A chat whose /start the listener heard has a complete history; another
// chat's, begun before the listener heard it, is not.
func TestTelegramHistoryIsKeptForEachChat(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, historyAgent)
	tg.files["P1"] = "a cat"
	tg.dm(1, "/start")
	tg.dm(1, "one")
	tg.dm(2, "hello")
	tg.dm(1, "", map[string]any{"caption": "this", "photo": photo("P1", 5)})
	tg.dm(1, "two")
	w.sync(dir)
	tg.waitFor(w, "1", "] two")
	tg.waitFor(w, "2", "] hello")
	want := map[string][]string{
		"1": {"complete [] one", "complete [Alice: one] this", "complete [Alice: one | Alice: this +photo.jpg] two"},
		"2": {"partial [] hello"},
	}
	for chat, said := range want {
		if got := tg.texts(chat); !slices.Equal(got, said) {
			t.Errorf("chat %s was told %q", chat, got)
		}
	}
}

// A chat's history holds at most 20 entries and 8000 bytes: past either,
// its oldest entries are dropped, and it is no longer complete.
func TestTelegramHistoryIsBounded(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, historyAgent)
	tg.dm(1, "/start")
	var lines []string
	for i := 1; i <= 22; i++ {
		tg.dm(1, fmt.Sprintf("m%d", i))
		lines = append(lines, fmt.Sprintf("Alice: m%d", i))
	}
	long := strings.Repeat("x", 4000)
	tg.dm(2, "/start")
	tg.dm(2, long)
	tg.dm(2, long+"y")
	tg.dm(2, "last")
	w.sync(dir)
	tg.waitFor(w, "1", "] m22")
	tg.waitFor(w, "2", "] last")
	said := tg.texts("1")
	if want := "complete [" + strings.Join(lines[:20], " | ") + "] m21"; said[20] != want {
		t.Errorf("the 21st message was given %q", said[20])
	}
	if want := "partial [" + strings.Join(lines[1:21], " | ") + "] m22"; said[21] != want {
		t.Errorf("the 22nd message was given %q", said[21])
	}
	if got := tg.texts("2"); got[len(got)-1] != "partial [Bob: "+long+"y] last" {
		t.Errorf("the message after two long ones was given %.80q", got[len(got)-1])
	}
}

// A chat with no message for a day is dropped from the listener's history:
// the next message in it has none, and it is not complete.
func TestTelegramHistoryIsDroppedAfterADay(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, historyAgent)
	tg.dm(1, "/start")
	tg.dm(1, "one")
	tg.dm(1, "two", map[string]any{"date": 1700000000 + 24*60*60})
	w.sync(dir)
	tg.waitFor(w, "1", "] two")
	if got := tg.texts("1"); !slices.Equal(got, []string{"complete [] one", "partial [] two"}) {
		t.Errorf("alice was told %q", got)
	}
}

// The listener keeps at most 64 KiB of history in all, counted in UTF-8 bytes,
// so its output stays well under the 256 KiB Corbie takes as a job's result:
// past that, the chat quiet longest is dropped. Six chats of a 4096-character
// message, as long as Telegram sends, of 3-byte characters pass the bound, and
// the listener goes on hearing after it.
func TestTelegramHistoryIsBoundedInAll(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, historyAgent)
	long := strings.Repeat("字", 4096)
	for chat := 100; chat < 106; chat++ {
		tg.dm(chat, "/start", map[string]any{"date": 1700000000 + chat})
		tg.dm(chat, long, map[string]any{"date": 1700000000 + chat})
	}
	tg.dm(100, "again", map[string]any{"date": 1700000200})
	tg.dm(105, "again", map[string]any{"date": 1700000200})
	tg.dm(103, "still", map[string]any{"date": 1700000300})
	w.sync(dir)
	tg.waitFor(w, "103", "] still")
	if got := tg.texts("100"); got[len(got)-1] != "partial [] again" {
		t.Errorf("the chat quiet longest was given %.80q", got[len(got)-1])
	}
	// A reply past 4096 characters is posted in parts.
	for chat, last := range map[string]string{"105": "again", "103": "still"} {
		if got := strings.Join(tg.texts(chat), ""); !strings.HasSuffix(got, "complete ["+chat+": "+long+"] "+last) {
			t.Errorf("chat %s was given %.80q", chat, got[len(got)-80:])
		}
	}
	for _, run := range listenerRuns(w, "telegram") {
		for _, e := range w.record(run.ID) {
			for _, ev := range e.Events {
				if ev.Name == "failed" {
					t.Errorf("the listener failed: %v", ev.Data)
				}
			}
		}
	}
}

// The Bot API keeps nothing a bot can read, so a new run of the listener, as a
// changed router starts, has no history: the next message in a chat it had
// heard in full has none, and it is not complete.
func TestTelegramHistoryIsLostWithTheListener(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, historyAgent)
	tg.dm(1, "/start")
	tg.dm(1, "one")
	w.sync(dir)
	tg.waitFor(w, "1", "complete [] one")
	setSite(t, dir, router("telegram"), "description: The router of gromit on Telegram.", "description: The router of gromit on Telegram, changed.")
	w.sync(dir)
	w.until("a new listener", func() bool {
		return slices.ContainsFunc(listenerRuns(w, "telegram"), func(r *client.Run) bool { return r.HasEnded() })
	})
	tg.dm(1, "two")
	tg.waitFor(w, "1", "] two")
	if got := tg.texts("1"); !slices.Equal(got, []string{"complete [] one", "partial [] two"}) {
		t.Errorf("alice was told %q", got)
	}
}

// GITLAB_SITE and JIRA_SITE given with https:// fail the listener, saying
// each is a host name.
func TestSiteWithASchemeIsExplained(t *testing.T) {
	t.Skip("R5 and R6 revive this: GitLab and Jira")
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
	for _, m := range emitted(w, "slack", "message") {
		if strings.Contains(fmt.Sprint(m), elsewhere.URL) {
			t.Errorf("the message names the other host's file: %v", m)
		}
	}
}

// A message that is only the bot's name is answered, with nothing said.
func TestSlackNameAloneIsHeard(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	write(t, dir, map[string]string{"agents/assistant.yaml": `
		statechart: assistant
		input: { messages: list, medium: string, asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
		initial: e
		states:
		  e: { end: done, output: { reply: "said [${input.messages[0].text}]" } }
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

// slackMessages is a toolkit whose one tool posts on Slack, and waits for a
// yes in #approvals from someone who holds messages:approve.
const slackMessages = `
	description: Messages to people on Slack.
	instructions: Write to a person only when the asker asks you to.
	ask:
	  machine: .rook/approval.yaml
	  input: { ask: gromit/slack/chat/ask, lookup: people/find, approvers: messages:approve, in: "#approvals", asker: "${input.asker.email}" }
	tools:
	  tell: { call: gromit/slack/chat/post, approve: true }
`

// bob asks on Slack for a held tool. The approval asks in #approvals with
// Slack's buttons, alice's press lets the tool run, and the message can be
// followed: the router, the conversation, the agent, its approval and the
// question, which names the approval that called it, and the press the router
// sent to it.
func TestSlackApprovalByButtonCanBeFollowed(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir, model := slackAgentProject(t, w, s)
	write(t, dir, map[string]string{"toolkits/messages.yaml": slackMessages})
	replies := []llmstandin.Reply{
		{Calls: []llmstandin.ToolCall{{ID: "c1", Name: "open", Args: `{"toolkit": "messages"}`}}},
		{Calls: []llmstandin.ToolCall{{ID: "c2", Name: "tell", Args: `{"in": "U2", "text": "hello bob"}`}}},
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
	s.says("a", dm("U2", "3.0", "tell me hello"))
	var question map[string]any
	w.until("the question", func() bool {
		for _, p := range s.posted() {
			if p["blocks"] != nil {
				question = p
			}
		}
		return question != nil
	})
	if question["channel"] != "C5" {
		t.Errorf("the question went to %v", question["channel"])
	}
	if blocks, _ := json.Marshal(question["blocks"]); !strings.Contains(string(blocks), "tell") || !strings.Contains(string(blocks), "hello bob") {
		t.Errorf("the question does not show the held call: %s", blocks)
	}
	s.press("p1", "U1", value(t, question, "yes"))
	w.until("bob told and answered", func() bool {
		var hello, told bool
		for _, p := range s.posted() {
			hello = hello || p["markdown_text"] == "hello bob" && p["channel"] == "D9"
			told = told || p["markdown_text"] == "told" && p["channel"] == "D1"
		}
		return hello && told
	})

	sent := func(from, kind, at string) []instance.Ask {
		var out []instance.Ask
		for _, ask := range w.asked(from, kind) {
			if ask.Location == at {
				out = append(out, ask)
			}
		}
		return out
	}
	router := w.kept(router("slack"))
	conversations := sent(router, instance.AskEvent, location("conversation"))
	if len(conversations) != 1 || conversations[0].Key != "slack:D1:U2:assistant" {
		t.Fatalf("the router sent %+v", conversations)
	}
	conversation := keyed(w, "conversation.yaml")["slack:D1:U2:assistant"][0].ID
	agents := sent(conversation, instance.AskCall, location("agents/assistant"))
	if len(agents) != 1 {
		t.Fatalf("the conversation called %+v", agents)
	}
	runs, err := w.List(context.Background(), client.Filter{Parent: agents[0].Run, Finished: true})
	if err != nil {
		t.Fatal(err)
	}
	var approval string
	for _, run := range runs {
		if run.Machine == "approval" {
			approval = run.ID
		}
	}
	if approval == "" || len(sent(agents[0].Run, instance.AskCall, location(bot+"/slack/chat/post"))) != 1 {
		t.Fatalf("the agent's runs are %v, and it called %+v", runs, w.asked(agents[0].Run, instance.AskCall))
	}
	asked := sent(approval, instance.AskCall, location(bot+"/slack/chat/ask"))
	if len(asked) != 1 || w.show(asked[0].Run).StartedBy != approval {
		t.Fatalf("the approval's record names the questions %+v", asked)
	}
	pressed := false
	for _, ask := range w.asked(router, instance.AskEvent) {
		pressed = pressed || ask.To == asked[0].Run
	}
	if !pressed {
		t.Errorf("the router's record does not name the question its press went to")
	}
}

// A comment that ends a sentence with the bot's name, as "can you look @bot.",
// names the bot, as GitLab itself takes it, and is answered.
func TestGitLabNameAtTheEndOfASentenceIsHeard(t *testing.T) {
	t.Skip("R5 revives this: GitLab")
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
	t.Skip("R5 revives this: GitLab")
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
	setSite(t, dir, router("gitlab"), "projects: [group/project]", "projects: [group/project, group/other]")
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
	t.Skip("R5 revives this: GitLab")
	gl := newGitLab(t)
	w := newWorld(t)
	w.secret("GITLAB_TOKEN", "gl-token")
	dir := pointed(t, "gitlab", gl.Server)
	w.value("GITLAB_SITE", strings.TrimPrefix(gl.URL, "http://"))
	gl.note(1, 2, "al", "before")
	w.sync(dir)
	w.until("the first ask", func() bool { return gl.asked() > 0 })
	// A comment on merge request 4 is in no issue's notes, so the stand-in holds none.
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
	t.Skip("R5 revives this: GitLab")
	gl := newGitLab(t)
	w := newWorld(t)
	w.secret("GITLAB_TOKEN", "gl-token")
	dir := pointed(t, "gitlab", gl.Server)
	w.value("GITLAB_SITE", strings.TrimPrefix(gl.URL, "http://"))
	setSite(t, dir, router("gitlab"), "projects: [group/project] }", "projects: [group/project], overhear: true }")
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
	t.Skip("R5 revives this: GitLab")
	gl := newGitLab(t)
	w := newWorld(t)
	w.secret("GITLAB_TOKEN", "gl-token")
	dir := pointed(t, "gitlab", gl.Server)
	w.sync(dir)
	w.kept(router("gitlab"))
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
	bridge := w.kept(router("gitlab"))
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
	t.Skip("R5 revives this: GitLab")
	gl := newGitLab(t)
	w := newWorld(t)
	dir := pointed(t, "gitlab", gl.Server)
	w.value("GITLAB_SITE", strings.TrimPrefix(gl.URL, "http://"))
	w.sync(dir)
	w.kept(router("gitlab"))
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
	t.Skip("R5 revives this: GitLab")
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
	t.Skip("R6 revives this: Jira")
	j := newJira(t)
	w := newWorld(t)
	w.secret("JIRA_TOKEN", "jira-token")
	dir := pointed(t, "jira", j.Server)
	w.value("JIRA_SITE", strings.TrimPrefix(j.URL, "http://"))
	w.sync(dir)
	w.kept(router("jira"))
	w.until("the listener fails naming JIRA_EMAIL", func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return strings.Contains(strings.Join(failures(w), "\n"), "JIRA_EMAIL")
	})
}

// Two Jira projects are asked about in one search, and a comment on each is its
// own chat, with the project's key as its place.
func TestJiraTwoProjectsAreAsked(t *testing.T) {
	t.Skip("R6 revives this: Jira")
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
	setSite(t, dir, router("jira"), "projects: [OPS]", "projects: [OPS, DEV]")
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
	t.Skip("R6 revives this: Jira")
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
	t.Skip("R6 revives this: Jira")
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
	t.Skip("R7 revives this: conversation, agent and starter")
	s, gl, j := newSlack(t), newGitLab(t), newJira(t)
	sh, gh, jh := &headers{}, &headers{}, &headers{}
	sh.wrap(s.Server)
	gh.wrap(gl.Server)
	jh.wrap(j.Server)
	w := newWorld(t)
	dir := everyService(t, w, s, gl, j)
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
	t.Skip("R7 revives this: conversation, agent and starter")
	s, gl, j := newSlack(t), newGitLab(t), newJira(t)
	w := newWorld(t)
	dir := everyService(t, w, s, gl, j)
	w.secret("TEST_KEY", "sk-test")
	replies := []llmstandin.Reply{
		{Calls: []llmstandin.ToolCall{{ID: "c1", Name: "open", Args: `{"toolkit": "messages"}`}}},
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
	w.kept(router("slack"))
	w.until("the listener fails naming SLACK_APP_TOKEN", func() bool {
		return strings.Contains(strings.Join(failures(w), "\n"), "SLACK_APP_TOKEN is not set")
	})
	if got := seen.all(); len(got) != 0 {
		t.Errorf("Slack was asked %v", got)
	}
}

// With overhear on Jira, a comment that does not name the bot goes to attend,
// and one that attend says yes to is answered on the issue.
func TestJiraOverheardCommentGoesToAttend(t *testing.T) {
	t.Skip("R6 revives this: Jira")
	j := newJira(t)
	w := newWorld(t)
	w.secret("JIRA_TOKEN", "jira-token")
	dir := pointed(t, "jira", j.Server)
	jiraAt(w, j)
	setSite(t, dir, router("jira"), "projects: [OPS] }", "projects: [OPS], overhear: true }")
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

// A question whose post Telegram refuses fails with Telegram's error, given
// once, and so does the machine that called it.
func TestQuestionOnAFailingServiceFailsWithItsError(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
		".rook/telegram/telegram.js": `if (input.do === "ask") throw new Error("Telegram is down")
			return {}`,
		"agents/asker.yaml": telegramAsker,
	})
	removeListener(t, dir)
	w.sync(dir)
	id := w.start("agents/asker.yaml", nil)
	w.until("the asker ends", func() bool { return w.show(id).HasEnded() })
	if run := w.show(id); run.Status != "failed" || !strings.Contains(run.Error, "Telegram is down") {
		t.Fatalf("the asker is %s: %s", run.Status, run.Error)
	}
	if got := w.show(id).Error; strings.Count(got, "Telegram is down") != 1 || strings.Count(got, " at line ") > 1 {
		t.Errorf("the asker's error repeats what failed: %s", got)
	}
}
