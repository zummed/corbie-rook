package rook_test

// Slack's listener and calls against the stand-in: who sent a message, what its
// thread held, reactions, stops, and the question with its buttons.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// emitted gives what the runs of a service's listener emitted under a name, in
// order.
func emitted(w *world, service, name string) []map[string]any {
	var out []map[string]any
	for _, run := range listenerRuns(w, service) {
		for _, e := range w.record(run.ID) {
			if e.Name == name {
				if m, ok := e.Data.(map[string]any); ok {
					out = append(out, m)
				}
			}
		}
	}
	return out
}

// press sends a press of a question's button with a value, by a person.
func (s *slack) press(id, user, value string) {
	s.send(map[string]any{"envelope_id": id, "type": "interactive", "payload": map[string]any{
		"type": "block_actions", "user": map[string]any{"id": user}, "actions": []any{map[string]any{"action_id": "choice-0", "value": value}},
		"channel": map[string]any{"id": "D9"}, "message": map[string]any{"ts": "1.1"},
	}})
}

// changed gives each message the bot changed, in order.
func (s *slack) changed() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.updates)
}

// value gives the value of the button of a choice in a post.
func value(t *testing.T, post map[string]any, choice string) string {
	t.Helper()
	blocks, _ := json.Marshal(post["blocks"])
	for _, part := range strings.Split(string(blocks), `"value":"`)[1:] {
		if v := strings.Split(part, `"`)[0]; strings.HasSuffix(v, " "+choice) {
			return v
		}
	}
	t.Fatalf("no button %s in %s", choice, blocks)
	return ""
}

// telling stands in for the agent at agents/assistant: it replies with what it
// was given of the last message's sender and the asker, as JSON.
const telling = `
	statechart: assistant
	input: { messages: list, medium: string, asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
	initial: e
	states:
	  e:
	    end: done
	    output:
	      reply: "${JSON.stringify({ user: input.messages[0].user, name: input.messages[0].name, email: input.messages[0].email, asker: input.asker })}"
`

// A message carries its sender's member ID, name and email, which Slack's
// users.info gives once a day for each person, and the lookup gives the asker
// by that email. A person Slack gives no name for is named by their member ID,
// has no email, and is answered.
func TestSlackMessageCarriesItsPerson(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	write(t, dir, map[string]string{"agents/assistant.yaml": telling})
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", dm("U1", "3.0", "first"))
	w.until("the first answer", func() bool { return len(s.posted()) >= 1 })
	s.says("b", dm("U1", "4.0", "second"))
	s.says("c", dm("U7", "5.0", "from elsewhere"))
	w.until("three answers", func() bool { return len(s.posted()) >= 3 })
	var got []string
	for _, p := range s.posted() {
		got = append(got, fmt.Sprint(p["markdown_text"]))
	}
	slices.Sort(got)
	want := []string{
		`{"user":"U1","name":"Alice","email":"alice@example.com","asker":{"email":"alice@example.com","name":"alice","scopes":["messages","messages:approve"]}}`,
		`{"user":"U1","name":"Alice","email":"alice@example.com","asker":{"email":"alice@example.com","name":"alice","scopes":["messages","messages:approve"]}}`,
		`{"user":"U7","name":"U7","email":"","asker":{"email":"","name":"U7","scopes":[]}}`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("the agent was given\n%s", strings.Join(got, "\n"))
	}
	if looked := s.looked(); !slices.Equal(looked, []string{"U1", "U7"}) {
		t.Errorf("users.info was asked for %v", looked)
	}
	if l := logged(w, "slack"); !slices.Contains(l, "Slack gives no name for U7: user_not_found") {
		t.Errorf("the listener logged %q", l)
	}
}

// An emoji a person adds to a message is sent as a message with change:
// reaction, in the thread of the message reacted to, and the bot's own is not.
// The starter's router sends no change to the conversation.
func TestSlackReactionIsAChange(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	s.thread = []map[string]any{
		{"ts": "4.0", "thread_ts": "4.0", "user": "U1", "text": "<@UBOT> start"},
		{"ts": "5.0", "thread_ts": "4.0", "user": "U2", "text": "a reply"},
	}
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.reacts("a", "UBOT", "robot_face", "C1", "5.0")
	s.reacts("b", "U2", "eyes", "C1", "5.0")
	w.until("the reaction", func() bool { return len(emitted(w, "slack", "message")) == 1 })
	time.Sleep(10 * interval)
	got, _ := json.Marshal(emitted(w, "slack", "message"))
	if want := `[{"at":"5.0","change":"reaction","chat":"C1/4.0","email":"bob@example.com","name":"Bob","place":"#help","service":"slack","text":":eyes:","user":"U2"}]`; string(got) != want {
		t.Fatalf("the listener sent %s", got)
	}
	if runs := w.at("conversation.yaml"); len(runs) != 0 || s.acked() != 2 {
		t.Errorf("the reaction started %d conversations, and %d envelopes were received", len(runs), s.acked())
	}
}

// A press of Slack's stop button under a thread is sent as stop, with who
// pressed it.
func TestSlackStopNamesWhoPressedIt(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "agent_session_stopped", "channel": "C1", "thread_ts": "4.0", "user": "U1", "event_ts": "6.0"})
	w.until("the stop", func() bool { return len(emitted(w, "slack", "stop")) == 1 })
	got, _ := json.Marshal(emitted(w, "slack", "stop"))
	if want := `[{"chat":"C1/4.0","email":"alice@example.com","name":"Alice","place":"#help","service":"slack","user":"U1"}]`; string(got) != want {
		t.Fatalf("the listener sent %s", got)
	}
}

// The person who asked for what a question asks about cannot answer it: their
// press is refused, they alone are told, and the buttons stay. With no press
// that counts within its time, the question is no, and shows that no one
// answered.
func TestSlackQuestionRefusesTheAsker(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	write(t, dir, map[string]string{"agents/asker.yaml": asker("slack", `{ in: "#approvals/7.0", text: "Restart?", choices: ["yes", "no"], lookup: people/find, approvers: messages:approve, asker: Alice@Example.com, within: 1h }`)})
	w.sync(dir)
	asking := w.start("agents/asker.yaml", nil)
	w.until("Slack connected", s.connected)
	w.until("the question", func() bool { return len(s.posted()) == 1 })
	if post := s.posted()[0]; post["channel"] != "C5" || post["thread_ts"] != "7.0" {
		t.Fatalf("the question went to %v", post)
	}
	s.press("a", "U1", value(t, s.posted()[0], "yes"))
	w.until("alice told", func() bool { return len(s.whispered()) == 1 })
	if got := s.whispered()[0]; got["channel"] != "C5" || got["thread_ts"] != "7.0" || got["user"] != "U1" || got["text"] != "You asked for this, so someone else must answer it." {
		t.Fatalf("alice was told %v", got)
	}
	time.Sleep(10 * interval)
	if run := w.show(asking); run.Status != "running" || len(s.changed()) != 0 {
		t.Fatalf("alice's press ended the asker %+v, or changed the question %v", run, s.changed())
	}
	w.set(w.now().Add(time.Hour + time.Minute))
	w.until("the asker done", func() bool { return w.show(asking).Status == "done" })
	if got, _ := json.Marshal(w.show(asking).Output); string(got) != `{"choice":"no"}` {
		t.Fatalf("the question gave %s", got)
	}
	w.until("the question closed", func() bool { return len(s.changed()) == 1 })
	if got := s.changed()[0]["text"]; got != "Restart?\n\nNo one answered in time, so the answer is no." {
		t.Fatalf("the question was changed to %q", got)
	}
}

// conversations/replies reads a whole thread before a message, past the most a
// message carries, each person by their name, and says it is the whole thread.
func TestSlackRepliesReadsAThread(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	for i := range 450 {
		s.thread = append(s.thread, map[string]any{"ts": fmt.Sprintf("%d.0", 100+i), "user": "U2", "text": fmt.Sprintf("word %d", i)})
	}
	write(t, dir, map[string]string{
		bot + "/slack/conversations/replies.yaml": "include: .rook/slack/conversations/replies.yaml",
		"on/read.yaml": `
			flow: read
			allow: [send:gromit/slack/conversations/replies]
			stages:
			  replies: { job: { call: { at: gromit/slack/conversations/replies }, input: { in: C1/100.0, at: "500.0" } } }
			  text: { job: { script: ./text.js, input: { replies: "${stages.replies.output}" } } }
			output: ${stages.text.output}
		`,
		"on/text.js": `return { complete: input.replies.complete, lines: blobs.read(input.replies.history).split("\n\n") }`,
	})
	w.sync(dir)
	id := w.start("on/read.yaml", nil)
	w.until("the thread read", func() bool { st := w.show(id).Status; return st == "done" || st == "failed" })
	out, _ := w.show(id).Output.(map[string]any)
	lines, _ := out["lines"].([]any)
	if out["complete"] != true || len(lines) != 400 || lines[0] != "Bob: word 0" || lines[399] != "Bob: word 399" {
		t.Fatalf("the thread read gave %v lines, complete %v: %s", len(lines), out["complete"], w.show(id).Error)
	}
}
