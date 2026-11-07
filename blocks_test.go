package rook_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zummed/corbie/client"
)

// replying puts in an agent at agents/assistant that gives one reply to
// everything.
func replying(t *testing.T, dir, reply string) {
	t.Helper()
	quoted, _ := json.Marshal(reply)
	stub := "statechart: assistant\ninput: { messages: list, medium: string, format: \"any?\", asker: map, thread: \"any?\", attachments: \"any?\", conversation: \"any?\" }\ninitial: e\nstates:\n  e: { end: done, output: { reply: " + string(quoted) + " } }\n"
	if err := os.WriteFile(filepath.Join(dir, "agents/assistant.yaml"), []byte(stub), 0o644); err != nil {
		t.Fatal(err)
	}
}

const charts = "Here.\n\n" +
	"```chart\n{\"type\": \"bar\", \"title\": \"Tickets\", \"labels\": [\"Jan\", \"Feb\"], \"series\": [{\"name\": \"Email\", \"values\": [1, 2]}]}\n```\n\n" +
	"```chart\n{\"type\": \"pie\", \"title\": \"Mix\", \"labels\": [\"A\", \"B\"], \"series\": [{\"values\": [3, 4]}]}\n```\n\n" +
	"```chart\n{\"type\": \"line\", \"title\": \"Long\", \"labels\": [\"a label far longer than twenty\"], \"series\": [{\"values\": [5]}]}\n```\n\n" +
	"```mermaid\ngraph LR; A-->B\n```"

// Slack draws a chart it can, and shows one it cannot as a table. A message Slack
// refuses is posted again as markdown, each chart as its table.
func TestSlackDrawsCharts(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(fmt.Sprint("refused ", refuse), func(t *testing.T) {
			s := newSlack(t)
			s.refuse = refuse
			w := newWorld(t)
			dir := slackProject(t, w, s)
			replying(t, dir, charts)
			w.sync(dir)
			w.until("Slack connected", s.connected)
			s.says("a", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "3.0", "text": "how many?"})
			w.until("the answer", func() bool { return len(s.posted()) >= 1 })
			post := s.posted()[0]
			if refuse {
				text := fmt.Sprint(post["markdown_text"])
				for _, want := range []string{"Here.", "**Tickets**", "| Jan | 1 |", "| A | 3 |", "| a label far longer than twenty | 5 |"} {
					if !strings.Contains(text, want) {
						t.Errorf("%q is not in %q", want, text)
					}
				}
				return
			}
			blocks, _ := json.Marshal(post["blocks"])
			for _, want := range []string{
				`{"text":"Here.","type":"markdown"}`,
				`"axis_config":{"categories":["Jan","Feb"]}`,
				`{"data":[{"label":"Jan","value":1},{"label":"Feb","value":2}],"name":"Email"}`,
				`"segments":[{"label":"A","value":3},{"label":"B","value":4}]`,
				`| a label far longer than twenty | 5 |`,
			} {
				if !strings.Contains(string(blocks), want) {
					t.Errorf("%s is not in %s", want, blocks)
				}
			}
			if n := strings.Count(string(blocks), "data_visualization"); n != 2 {
				t.Errorf("%d charts in %s", n, blocks)
			}
		})
	}
}

// A status shows under a thread while its message is answered, and is cleared
// once the reply is posted, when the answer says nothing, and when the answer
// fails and the thread is told so.
func TestSlackShowsAStatusWhileAnswering(t *testing.T) {
	for name, agent := range map[string]string{
		"reply":   "Done.",
		"nothing": "",
		"failure": "",
	} {
		t.Run(name, func(t *testing.T) {
			s := newSlack(t)
			w := newWorld(t)
			dir := slackProject(t, w, s)
			replying(t, dir, agent)
			if name == "failure" {
				write(t, dir, map[string]string{"agents/assistant.yaml": "flow: assistant\nstages:\n  fail: { job: { script: ./fail.js } }\n", "agents/fail.js": `throw new Error("no answer")`})
			}
			w.sync(dir)
			w.until("Slack connected", s.connected)
			s.says("a", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "thread_ts": "4.0", "ts": "5.0", "text": "hello"})
			w.until("two statuses", func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				return len(s.status) >= 2
			})
			quiet()
			s.mu.Lock()
			got, _ := json.Marshal(s.status)
			s.mu.Unlock()
			if string(got) != `[{"channel_id":"D1","status":"processing","thread_ts":"4.0"},{"channel_id":"D1","status":"active","thread_ts":"4.0"}]` {
				t.Fatalf("the statuses are %s", got)
			}
			want := map[string][]string{"reply": {"Done."}, "nothing": nil, "failure": {"That could not be answered."}}[name]
			var posted []string
			for _, p := range s.posted() {
				posted = append(posted, fmt.Sprint(p["markdown_text"]))
			}
			if !slices.Equal(posted, want) {
				t.Errorf("the bot posted %q", posted)
			}
		})
	}
}

// A status shows "Working..." under a thread for any text, and an empty text
// clears it. Outside a thread Slack keeps no status, and nothing is asked.
func TestSlackStatusShowsAndClears(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	write(t, dir, map[string]string{"on/status.yaml": `
		flow: status
		allow: [send:gromit/slack/assistant/status]
		stages:
		  outside: { job: { call: { at: gromit/slack/assistant/status }, input: { in: D1, text: Working on it. } } }
		  show: { follows: [outside], job: { call: { at: gromit/slack/assistant/status }, input: { in: D1/4.0, text: Working on it. } } }
		  clear: { follows: [show], job: { call: { at: gromit/slack/assistant/status }, input: { in: D1/4.0, text: "" } } }
	`})
	w.sync(dir)
	id := w.start("on/status.yaml", nil)
	w.until("the flow done", func() bool { return w.show(id).Status == "done" })
	s.mu.Lock()
	got, _ := json.Marshal(s.status)
	s.mu.Unlock()
	if string(got) != `[{"channel_id":"D1","status":"processing","thread_ts":"4.0"},{"channel_id":"D1","status":"active","thread_ts":"4.0"}]` {
		t.Fatalf("the statuses are %s", got)
	}
}

// With overhear, the listener sends a reply in a thread the bot has said
// something in, with what the thread held before it, and a channel's message
// outside a thread, with an empty history, each marked unaddressed; a reply in another thread is not
// sent. The starter's router answers none of them.
func TestSlackOverhearsAChannelAndAThreadItSpokeIn(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	setSite(t, dir, router("slack"), "job: { machine: .rook/slack/listen.yaml,", "job: { machine: .rook/slack/listen.yaml, input: { overhear: true },")
	s.thread = []map[string]any{
		{"ts": "1.0", "user": "U1", "text": "<@UBOT> start"},
		{"ts": "2.0", "user": "UBOT", "text": "my earlier reply"},
	}
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", inChannel("U1", "5.0", "1.0", "please help"))
	w.until("the reply received", func() bool { return s.acked() == 1 })
	s.mu.Lock()
	s.thread = []map[string]any{{"ts": "3.0", "user": "U2", "text": "between people"}}
	s.mu.Unlock()
	s.says("b", inChannel("U2", "6.0", "3.0", "just chatting"))
	s.says("c", inChannel("U1", "8.0", "", "please, not in a thread"))
	w.until("every envelope received", func() bool { return s.acked() == 3 })
	time.Sleep(20 * interval)
	sent := emitted(w, "slack", "message")
	if len(sent) != 2 || sent[0]["text"] != "please help" || sent[0]["chat"] != "C1/1.0" || sent[0]["unaddressed"] != true ||
		sent[1]["text"] != "please, not in a thread" || sent[1]["chat"] != "C1/8.0" || sent[1]["unaddressed"] != true {
		t.Fatalf("the listener sent %v", sent)
	}
	if got := [][]string{heardLines(t, w, sent[0]), heardLines(t, w, sent[1])}; !reflect.DeepEqual(got, [][]string{{"Alice: you start", "you: my earlier reply"}, {}}) {
		t.Errorf("the histories are %q", got)
	}
	if got := s.posted(); len(got) != 0 || len(w.at("conversation.yaml")) != 0 {
		t.Errorf("the bot posted %v", got)
	}
}

// stubs gives the runs of a machine.
func stubs(t *testing.T, w *world, machine string) []*client.Run {
	t.Helper()
	runs, err := w.List(context.Background(), client.Filter{Finished: true})
	if err != nil {
		t.Fatal(err)
	}
	var out []*client.Run
	for _, run := range runs {
		if run.Machine == machine {
			out = append(out, run)
		}
	}
	return out
}

// With overhear, the listener sends a message that does not name the bot,
// marked unaddressed, and the starter's router drops it: it reaches no
// conversation. A message that names the bot is answered.
func TestOverheardIsDroppedByTheStarterRouter(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	echoing(t, dir)
	setSite(t, dir, router("slack"), "restart: { after: always } }", "restart: { after: always }, input: { overhear: true } }")
	s.thread = []map[string]any{{"ts": "1.0", "user": "UBOT", "text": "my earlier reply"}}
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "channel": "C1", "channel_type": "channel", "user": "U1", "ts": "5.0", "thread_ts": "1.0", "text": "please help"})
	s.says("b", map[string]any{"type": "message", "channel": "C1", "channel_type": "channel", "user": "U1", "ts": "6.0", "thread_ts": "1.0", "text": "<@UBOT> named"})
	w.until("the answer", func() bool { return len(s.posted()) >= 1 })
	time.Sleep(20 * interval)
	if got := s.posted(); len(got) != 1 || !strings.Contains(fmt.Sprint(got[0]["markdown_text"]), "named") {
		t.Fatalf("the bot posted %v", got)
	}
	if m := emitted(w, "slack", "message"); len(m) != 2 || m[0]["unaddressed"] != true {
		t.Errorf("the listener sent %v", m)
	}
	for _, run := range w.at("agents/assistant.yaml") {
		for _, text := range w.texts(run) {
			if text == "please help" {
				t.Errorf("the agent was given the overheard message")
			}
		}
	}
}

// A changed agent answers the next message of a conversation already under way,
// and a changed router restarts its listener, and the conversation goes on.
func TestChangedAgentAnswersTheNextMessage(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	echoing(t, dir)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "3.0", "text": "first"})
	w.until("the first answer", func() bool { return len(s.posted()) >= 1 })
	setSite(t, dir, "agents/assistant.yaml", `output: { reply: "${stages.echo.output}"`, `output: { reply: "${'changed: ' + input.messages.at(-1).text}"`)
	w.sync(dir)
	s.says("b", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "4.0", "text": "second"})
	w.until("the second answer", func() bool { return len(s.posted()) >= 2 })
	if got := fmt.Sprint(s.posted()[1]["markdown_text"]); got != "changed: second" {
		t.Fatalf("the bot said %q", got)
	}
	listener := w.kept(router("slack"))
	setSite(t, dir, router("slack"), "lifetime: singleton\n", "lifetime: singleton\nmeta: { changed: true }\n")
	s.mu.Lock()
	s.socket = nil
	s.mu.Unlock()
	w.sync(dir)
	if w.kept(router("slack")) == listener {
		t.Fatal("the changed router did not restart its listener")
	}
	w.until("Slack connected again", s.connected)
	s.says("c", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "5.0", "text": "third"})
	w.until("the third answer", func() bool { return len(s.posted()) >= 3 })
	if runs := keyed(w, "conversation.yaml"); len(runs) != 1 || len(runs["slack:D1:U1:assistant"]) != 1 {
		t.Errorf("the conversations are %v", runs)
	}
}

// A machine posts a text in a Slack channel named as #name.
func TestSlackSaysInAChannelByName(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	write(t, dir, map[string]string{"on/log.yaml": `
		flow: log
		allow: [send:gromit/slack/chat/post]
		stages:
		  say: { job: { call: { at: gromit/slack/chat/post }, input: { in: "#approvals", text: Moved. } } }
	`})
	w.sync(dir)
	w.start("on/log.yaml", nil)
	w.until("the text", func() bool { return len(s.posted()) == 1 })
	if post := s.posted()[0]; post["channel"] != "C5" || post["markdown_text"] != "Moved." {
		t.Fatalf("the bot posted %v", post)
	}
}

const layout = "Done.\n\n```blocks\n" +
	`[{"type": "header", "text": {"type": "plain_text", "text": "Release 4.2"}}, {"type": "section", "fields": [{"type": "mrkdwn", "text": "*Status*"}, {"type": "mrkdwn", "text": "Shipped"}]}]` +
	"\n```"

// Slack posts a layout's blocks as they are, and a message Slack refuses is posted
// again as markdown, each layout as its text.
func TestSlackPostsALayout(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(fmt.Sprint("refused ", refuse), func(t *testing.T) {
			s := newSlack(t)
			s.refuse = refuse
			w := newWorld(t)
			dir := slackProject(t, w, s)
			replying(t, dir, layout)
			w.sync(dir)
			w.until("Slack connected", s.connected)
			s.says("a", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "3.0", "text": "status?"})
			w.until("the answer", func() bool { return len(s.posted()) >= 1 })
			post := s.posted()[0]
			if refuse {
				if got := post["markdown_text"]; got != "Done.\n\nRelease 4.2\n*Status*\nShipped" {
					t.Fatalf("the bot posted %q", got)
				}
				return
			}
			blocks, _ := json.Marshal(post["blocks"])
			want := `[{"text":"Done.","type":"markdown"},{"text":{"text":"Release 4.2","type":"plain_text"},"type":"header"},{"fields":[{"text":"*Status*","type":"mrkdwn"},{"text":"Shipped","type":"mrkdwn"}],"type":"section"}]`
			if string(blocks) != want {
				t.Fatalf("the blocks are %s", blocks)
			}
		})
	}
}
