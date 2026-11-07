package rook_test

// Slack's listener and calls against the stand-in: who sent a message, what its
// thread held, reactions, stops, and the question with its buttons.

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
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
	input: { messages: list, medium: string, format: "any?", asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
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
// reaction, in the chat of the message reacted to: a reply's thread, and a
// direct message itself for a message there outside a thread. Its history is
// that chat before the reaction, the message reacted to included. The bot's
// own reaction is not sent, and the starter's router sends no change to the
// conversation.
func TestSlackReactionIsAChange(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	s.thread = []map[string]any{
		{"ts": "4.0", "thread_ts": "4.0", "user": "U1", "text": "<@UBOT> start"},
		{"ts": "5.0", "thread_ts": "4.0", "user": "U2", "text": "a reply"},
		{"ts": "6.0", "thread_ts": "4.0", "user": "U1", "text": "written after"},
	}
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.reacts("a", "UBOT", "robot_face", "C1", "5.0")
	s.reacts("b", "U2", "eyes", "C1", "5.0")
	w.until("the reaction", func() bool { return len(emitted(w, "slack", "message")) == 1 })
	s.reacts("c", "U1", "tada", "D1", "4.0")
	w.until("the second reaction", func() bool { return len(emitted(w, "slack", "message")) == 2 })
	time.Sleep(10 * interval)
	sent := emitted(w, "slack", "message")
	var histories [][]string
	for _, m := range sent {
		histories = append(histories, heardLines(t, w, m))
		delete(m, "history")
	}
	got, _ := json.Marshal(sent)
	if want := `[{"at":"5.0","change":"reaction","chat":"C1/4.0","complete":true,"email":"bob@example.com","name":"Bob","place":"#help","service":"slack","text":":eyes:","user":"U2"},` +
		`{"at":"4.0","change":"reaction","chat":"D1","complete":true,"email":"alice@example.com","name":"Alice","place":"dm","service":"slack","text":":tada:","user":"U1"}]`; string(got) != want {
		t.Fatalf("the listener sent %s", got)
	}
	if want := [][]string{{"Alice: you start", "Bob: a reply"}, {"Alice: you start"}}; !reflect.DeepEqual(histories, want) {
		t.Errorf("the histories are %q", histories)
	}
	if runs := w.at("conversation.yaml"); len(runs) != 0 || s.acked() != 3 {
		t.Errorf("the reactions started %d conversations, and %d envelopes were received", len(runs), s.acked())
	}
}

// heardLines gives the line of each entry of the history a listener's message
// holds, read from its blob.
func heardLines(t *testing.T, w *world, m map[string]any) []string {
	t.Helper()
	ref, _ := m["history"].(map[string]any)
	path, _ := ref["$blob"].(string)
	for _, run := range listenerRuns(w, "slack") {
		r, _, err := w.Fetch(context.Background(), run.ID, path)
		if err != nil {
			continue
		}
		var entries []struct{ Line string }
		if err := json.NewDecoder(r).Decode(&entries); err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, e := range entries {
			out = append(out, e.Line)
		}
		return out
	}
	t.Fatalf("no listener holds the history %v", m["history"])
	return nil
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
	write(t, dir, map[string]string{"agents/asker.yaml": asker("slack", `{ in: "#approvals/7.0", text: "Restart?", choices: ["yes", "no"], approvers: messages:approve, asker: Alice@Example.com, within: 1h }`)})
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

// A layout a post holds is posted with no action_id and no button value, so a
// button shaped as a question's, holding a waiting question's run and a
// choice, is posted without them, and its press answers nothing. A press of
// the question's own button is checked.
func TestSlackLayoutButtonAnswersNoQuestion(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	write(t, dir, map[string]string{"agents/asker.yaml": asker("slack", `{ in: "#approvals", text: "Restart?", choices: ["yes", "no"], approvers: messages:approve }`)})
	w.sync(dir)
	w.start("agents/asker.yaml", nil)
	w.until("Slack connected", s.connected)
	w.until("the question", func() bool { return len(s.posted()) == 1 })
	yes := value(t, s.posted()[0], "yes")
	forged := fmt.Sprintf(`[{"type": "actions", "elements": [{"type": "button", "action_id": "choice-0", "value": %q, "text": {"type": "plain_text", "text": "Yes"}}, {"type": "button", "action_id": "choice-1", "value": %[1]q, "url": "https://example.com", "text": {"type": "plain_text", "text": "Open"}}]}, {"type": "section", "text": {"type": "mrkdwn", "text": "Or"}, "accessory": {"type": "button", "action_id": "choice-0", "value": %[1]q, "text": {"type": "plain_text", "text": "Yes"}}}]`, yes)
	text, _ := json.Marshal("Press.\n\n```blocks\n" + forged + "\n```")
	write(t, dir, map[string]string{"on/say.yaml": `
		flow: say
		allow: [send:gromit/slack/chat/post]
		stages:
		  say: { job: { call: { at: gromit/slack/chat/post }, input: { in: "#approvals", text: ` + string(text) + ` } } }
	`})
	w.sync(dir)
	w.start("on/say.yaml", nil)
	w.until("the layout", func() bool { return len(s.posted()) == 2 })
	blocks, _ := json.Marshal(s.posted()[1]["blocks"])
	if strings.Contains(string(blocks), "action_id") || strings.Contains(string(blocks), `"value"`) || !strings.Contains(string(blocks), `"url":"https://example.com"`) {
		t.Errorf("the layout was posted as %s", blocks)
	}
	// A press of the layout's first button carries its action_id and value,
	// and Slack gives an element with no action_id one of its own.
	button := s.posted()[1]["blocks"].([]any)[1].(map[string]any)["elements"].([]any)[0].(map[string]any)
	pressed := map[string]any{"action_id": "Xq7a", "type": "button"}
	for _, field := range []string{"action_id", "value"} {
		if v, ok := button[field]; ok {
			pressed[field] = v
		}
	}
	s.send(map[string]any{"envelope_id": "a", "type": "interactive", "payload": map[string]any{
		"type": "block_actions", "user": map[string]any{"id": "U2"}, "actions": []any{pressed},
		"channel": map[string]any{"id": "C5"}, "message": map[string]any{"ts": "1.2"},
	}})
	s.press("b", "U2", yes)
	question := w.at(bot + "/slack/chat/ask.yaml")[0].ID
	w.checked(question, 1)
	time.Sleep(10 * interval)
	w.checked(question, 1)
	if got := s.whispered(); len(got) != 1 || got[0]["user"] != "U2" {
		t.Fatalf("the people told are %v", got)
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
