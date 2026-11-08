package test

// The Slack, GitLab and Jira services against their stand-ins: threads,
// mentions, envelopes Slack sends again, files, hosts as values, questions
// answered by Slack's buttons, comments as chats, and several services in one
// project.

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
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
	write(t, dir, map[string]string{"agents/asker.yaml": asker("slack", `{ in: U1, text: "Restart?", choices: ["yes", "no"], approvers: messages:approve }`)})
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

// historyAgent stands in for the agent at agents/assistant: it replies with a
// line for each message of the turn, giving the history the message carries,
// each entry's line and files, whether that history is complete, and the
// message's text.
var historyAgent = map[string]string{
	"agents/assistant.yaml": `
		flow: assistant
		input: { messages: list, medium: string, format: "any?", asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
		stages:
		  turn: { job: { script: ./history.js, input: { messages: "${input.messages}" } } }
		output: ${stages.turn.output}
	`,
	"agents/history.js": `
		const said = (m) => {
		  const lines = Array.from(blobs.items(m.history)).map((e) => e.line + e.attachments.map((a) => " +" + a.name).join(""))
		  return (m.complete ? "complete" : "partial") + " [" + lines.join(" | ") + "] " + m.text
		}
		return { reply: input.messages.map(said).join("\n") }
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
		if got := tg.lines(chat); !slices.Equal(got, said) {
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
	said := tg.lines("1")
	if want := "complete [" + strings.Join(lines[:20], " | ") + "] m21"; said[20] != want {
		t.Errorf("the 21st message was given %q", said[20])
	}
	if want := "partial [" + strings.Join(lines[1:21], " | ") + "] m22"; said[21] != want {
		t.Errorf("the 22nd message was given %q", said[21])
	}
	if got := tg.lines("2"); got[len(got)-1] != "partial [Bob: "+long+"y] last" {
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
	if got := tg.lines("1"); !slices.Equal(got, []string{"complete [] one", "partial [] two"}) {
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
	tg.waitFor(w, "100", "] again")
	tg.waitFor(w, "105", "] again")
	if got := tg.lines("100"); got[len(got)-1] != "partial [] again" {
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
	for _, c := range []struct{ service, value, want string }{
		{"gitlab", "https://gitlab.example.com", "GITLAB_SITE is a host name, such as gitlab.example.com, with no https:// and no path: it is https://gitlab.example.com"},
		{"jira", "https://example.atlassian.net/", "JIRA_SITE is a host name, such as example.atlassian.net, with no https:// and no path: it is https://example.atlassian.net/"},
	} {
		t.Run(c.service, func(t *testing.T) {
			w := newWorld(t)
			srv := httptest.NewServer(http.NotFoundHandler())
			t.Cleanup(srv.Close)
			dir := pointed(t, c.service, srv)
			w.secret(strings.ToUpper(c.service)+"_TOKEN", "token")
			w.value("JIRA_EMAIL", "bot@example.com")
			w.value(strings.ToUpper(c.service)+"_SITE", c.value)
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
		input: { messages: list, medium: string, format: "any?", asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
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
	  input: { ask: [{ at: gromit/slack/chat/ask, in: "#approvals" }], approvers: messages:approve, asker: "${input.asker.email}" }
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
	places, err := w.List(context.Background(), client.Filter{Parent: approval, Finished: true})
	if err != nil || len(places) != 1 || places[0].Machine != "place" {
		t.Fatalf("the approval's runs are %v: %v", places, err)
	}
	place := places[0].ID
	asked := sent(place, instance.AskCall, location(bot+"/slack/chat/ask"))
	if len(asked) != 1 || w.show(asked[0].Run).StartedBy != place {
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

// One project runs Slack, GitLab and Jira at once. alice is answered on each
// where she asked, in a conversation of its own, and each service sees only
// its own token.
func TestEveryServiceAtOnce(t *testing.T) {
	s, gl, j := newSlack(t), newGitLab(t), newJira(t)
	sh, gh, jh := &headers{}, &headers{}, &headers{}
	sh.wrap(s.Server)
	gh.wrap(gl.Server)
	jh.wrap(j.Server)
	w := newWorld(t)
	dir := everyService(t, w, s, gl, j)
	gl.old("group/project", 7)
	w.sync(dir)
	for _, service := range []string{"slack", "gitlab", "jira"} {
		w.kept(router(service))
	}
	w.until("Slack connected", s.connected)
	gl.idle(w)
	tick(w, j.asked)
	s.says("a", dm("U1", "3.0", "on slack"))
	gl.comment("group/project", 7, 2, "@bot on gitlab")
	j.comment("1", "acc-1", "[~accountid:acc-bot] on jira", time.Now().Add(time.Second))
	w.until("each answered", func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return len(s.posted()) >= 1 && len(gl.said()) >= 1 && len(j.saying()) >= 1
	})
	if s.posted()[0]["markdown_text"] != "on slack" || gl.said()[0] != "on gitlab" || j.saying()[0] != "on jira" {
		t.Errorf("Slack was told %v, GitLab %q, Jira %q", s.posted()[0]["markdown_text"], gl.said(), j.saying())
	}
	conversations := keyed(w, "conversation.yaml")
	for _, key := range []string{"slack:D1:U1:assistant", "gitlab:group/project#7:2:assistant", "jira:OPS-12:acc-1:assistant"} {
		if conversations[key] == nil {
			t.Errorf("no conversation %s among %v", key, slices.Collect(maps.Keys(conversations)))
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

// slackApprovedToolkit is a toolkit whose tell posts on Slack, held for a yes
// on Slack, in alice's direct message, from someone who holds
// messages:approve.
const slackApprovedToolkit = `
	description: Messages to people on Slack.
	ask:
	  machine: .rook/approval.yaml
	  input: { ask: [{ at: gromit/slack/chat/ask, in: U1 }], approvers: "messages:approve", request: "${input.messages.map((m) => m.text).join('\\n')}" }
	tools:
	  tell:
	    description: Write a text to a person on Slack, by their member ID.
	    args: { to: string, text: string }
	    job:
	      call: { at: gromit/slack/chat/post }
	      input: { in: "${args.to}", text: "${args.text}" }
	      allow: [send:gromit/slack/chat/post]
	      approve: true
`

// Al asks on a GitLab issue for a held tool. GitLab has no buttons, so the
// toolkit's ask asks on Slack, and alice's press there lets the tool tell bob
// on Slack. The answer goes back to the issue.
func TestGitLabRequestApprovedOnSlack(t *testing.T) {
	s, gl, j := newSlack(t), newGitLab(t), newJira(t)
	w := newWorld(t)
	dir := everyService(t, w, s, gl, j)
	gl.old("group/project", 7)
	_, url := standin(t, w, func(c llmstandin.Call) llmstandin.Reply {
		switch toolUses(c) {
		case 0:
			return calls("open", `{"toolkit": "slack"}`)
		case 1:
			return calls("tell", `{"to": "U2", "text": "hello bob"}`)
		}
		return llmstandin.Reply{Text: "told"}
	})
	write(t, dir, map[string]string{
		"agents/assistant.yaml": modelAgent("assistant", url, "toolkits:\n  slack: { include: toolkits/slack.yaml }\n"),
		"toolkits/slack.yaml":   slackApprovedToolkit,
	})
	w.sync(dir)
	w.until("Slack connected", s.connected)
	gl.idle(w)
	gl.comment("group/project", 7, 2, "@bot tell bob hello")
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
	w.until("told on GitLab", func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return len(gl.said()) >= 1
	})
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
