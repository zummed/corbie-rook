package rook_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zummed/corbie/client"
	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// replying puts in an agent that gives one reply to everything.
func replying(t *testing.T, dir, reply string) {
	t.Helper()
	quoted, _ := json.Marshal(reply)
	stub := "statechart: helper\ninput: { said: string, asker: string, home: string, conversation: \"any?\" }\ninitial: e\nstates:\n  e: { end: done, output: { reply: " + string(quoted) + " } }\n"
	if err := os.WriteFile(filepath.Join(dir, "agents/helper.yaml"), []byte(stub), 0o644); err != nil {
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
	t.Skip("R3 revives this: Slack")
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
	t.Skip("R3 revives this: Slack")
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
				write(t, dir, map[string]string{"agents/helper.yaml": "flow: helper\nstages:\n  fail: { job: { script: ./fail.js } }\n", "agents/fail.js": `throw new Error("no answer")`})
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

// attendStub stands in for attend.yaml: it answers a message holding "please".
const attendStub = `
	statechart: attend-stub
	input: { said: string, asker: string, address: map, history: "any?" }
	initial: e
	states:
	  e: { end: done, output: { reply: { answer: "${input.said.includes('please')}" } } }
`

// With overhear, a reply in a thread the bot has said something in, and a
// channel's message outside a thread, go to attend.yaml, and are answered only
// if it says so. A stranger's is not answered and not told anything.
func TestSlackOverhearsAChannelAndAThreadItSpokeIn(t *testing.T) {
	t.Skip("R3 revives this: Slack")
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
		{"ts": "2.0", "user": "UBOT", "text": "my earlier reply"},
	}
	w.sync(dir)
	w.until("Slack connected", s.connected)
	in := func(id, user, ts, thread, text string) {
		e := map[string]any{"type": "message", "channel": "C1", "channel_type": "channel", "user": user, "ts": ts, "text": text}
		if thread != "" {
			e["thread_ts"] = thread
		}
		s.says(id, e)
	}
	in("a", "U1", "5.0", "1.0", "please help")
	in("b", "U1", "6.0", "1.0", "just chatting")
	in("c", "U9", "7.0", "1.0", "please too")
	in("d", "U1", "8.0", "", "please, not in a thread")
	w.until("the answers", func() bool { return len(s.posted()) >= 2 })
	w.until("all three asked", func() bool { return len(stubs(t, w, "attend-stub")) == 3 })
	time.Sleep(20 * interval)
	var said []string
	for _, p := range s.posted() {
		said = append(said, fmt.Sprint(p["markdown_text"]))
	}
	if got := strings.Join(said, "\n"); len(said) != 2 || !strings.Contains(got, "please help") || !strings.Contains(got, "please, not in a thread") {
		t.Fatalf("the bot posted %q", said)
	}
	for _, run := range stubs(t, w, "attend-stub") {
		input := w.show(run.ID).Input.(map[string]any)
		if history := fmt.Sprint(input["history"]); input["said"] != "please, not in a thread" && !strings.Contains(history, "you: my earlier reply") {
			t.Errorf("attend was given %q for %q", history, input["said"])
		}
	}
	if n := len(stubs(t, w, "attend-stub")); n != 3 {
		t.Fatalf("attend ran %d times", n)
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

// attend.yaml written as an agent that includes .rook/attend/agent.yaml asks
// its model, shown the thread and the message and what the bot is for, and
// gives its answer as reply.
func TestAttendAsksAModel(t *testing.T) {
	t.Skip("R2 revives this: approval, lookup and question")
	w := newWorld(t)
	w.secret("TEST_KEY", "sk-test")
	dir := filepath.Join(t.TempDir(), "project")
	if err := exampleProject(dir, nil); err != nil {
		t.Fatal(err)
	}
	model := &llmstandin.Server{Answer: func(llmstandin.Call) llmstandin.Reply { return llmstandin.Reply{Text: `{"answer": true}`} }}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	write(t, dir, map[string]string{"attend.yaml": `
		agent: attend
		include: .rook/attend/agent.yaml
		model: { name: test-model, service: anthropic, address: ` + web.URL + `, secret: TEST_KEY }
		system:
		  - { include: .rook/attend/agent.yaml#system }
		  - The bot answers questions about the build.
	`})
	w.sync(dir)
	id := w.start("attend.yaml", map[string]any{"said": "please look", "asker": "alice", "address": map[string]any{"bridge": "slack", "id": "C1/1.0"}, "history": "you: earlier words"})
	w.until("the answer", func() bool { return w.show(id).HasEnded() })
	if run := w.show(id); run.Status != "done" || fmt.Sprint(run.Output.(map[string]any)["reply"]) != "map[answer:true]" {
		t.Fatalf("attend ended %s with %v: %s", run.Status, run.Output, run.Error)
	}
	shown := fmt.Sprint(model.Calls[0].Messages)
	for _, want := range []string{"you: earlier words", "from alice", "please look"} {
		if !strings.Contains(shown, want) {
			t.Errorf("%q is not in %s", want, shown)
		}
	}
	if system := model.Calls[0].System; !strings.Contains(system, "meant for the") || !strings.Contains(system, "about the build") {
		t.Errorf("the system text is %q", system)
	}
}

// With overhear and no attend.yaml, a message that does not name the bot is not
// answered: the call to attend fails, which the chat's record shows, and a
// message that names it is answered.
func TestOverheardWithNoAttendIsNotAnswered(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	echoing(t, dir)
	setSite(t, dir, router("slack"), "      restart: { when: always }\n", "      restart: { when: always }\n      input: { overhear: true }\n")
	setSite(t, dir, "rules.yaml", "  dm: { readers: [asker] }\n", "  dm: { readers: [asker] }\n  \"#help\": { readers: [asker] }\n")
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
	failed := 0
	for _, run := range stubs(t, w, "route") {
		for _, e := range w.record(run.ID) {
			for _, ev := range e.Events {
				if ev.Name == "failed" && ev.From == "attend" && strings.Contains(fmt.Sprint(ev.Data), "/project/attend") {
					failed++
				}
			}
		}
	}
	if failed != 1 {
		t.Errorf("the chat's record shows %d failed calls of attend", failed)
	}
}

// A question in a Slack channel is posted there, and the first tap from someone in
// the group it names answers it; the message then shows who answered, with no
// buttons. A part's ask names such approvers as people and in.
func TestQuestionInAChannelCountsTheGroup(t *testing.T) {
	t.Skip("R2 revives this: approval, lookup and question")
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	write(t, dir, map[string]string{
		"people.yaml": "people:\n  alice: { slack: U1 }\n  bob: { slack: U2 }\ngroups:\n  team: [bob]\n",
		"rules.yaml":  "places: {}\nagents:\n  - { name: helper, for: [alice] }\n",
		"on/asker.yaml": `
			flow: asker
			allow: [send:.rook/blocks/ask]
			stages:
			  asking: { job: { call: { at: .rook/blocks/ask }, input: { people: [team], in: "#approvals", text: "Deploy?", choices: ["yes", "no"] } } }
			output: ${stages.asking.output.choice}
		`,
	})
	w.sync(dir)
	asker := w.start("on/asker.yaml", nil)
	w.until("Slack connected", s.connected)
	w.until("the question", func() bool { return len(s.posted()) == 1 })
	post := s.posted()[0]
	if post["channel"] != "C5" {
		t.Fatalf("the question went to %v", post)
	}
	blocks, _ := json.Marshal(post["blocks"])
	value := strings.Split(strings.Split(string(blocks), `"value":"`)[1], " ")[0]
	press := func(id, user, choice string) {
		s.send(map[string]any{"envelope_id": id, "type": "interactive", "payload": map[string]any{
			"type": "block_actions", "user": map[string]any{"id": user}, "actions": []any{map[string]any{"action_id": "choice-0", "value": value + " " + choice}},
			"channel": map[string]any{"id": "C5"}, "message": map[string]any{"ts": "1.1", "text": "Deploy?"},
		}})
	}
	press("a", "U1", "no")
	w.until("the press received", func() bool { return s.acked() == 1 })
	time.Sleep(20 * interval)
	if run := w.show(asker); run.Status != "running" {
		t.Fatalf("a press by someone outside the group ended the question: %+v", run)
	}
	s.mu.Lock()
	changed := len(s.updates)
	s.mu.Unlock()
	if changed != 0 {
		t.Fatalf("a press that did not count changed the question")
	}
	press("b", "U2", "yes")
	w.until("the asker done", func() bool { return w.show(asker).Status == "done" })
	if got := w.show(asker).Output; got != "yes" {
		t.Fatalf("the choice was %v", got)
	}
	w.until("the question closed", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.updates) == 1
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if got := s.updates[0]; got["channel"] != "C5" || got["text"] != "Deploy?\n\nbob said yes." {
		t.Fatalf("the question was changed to %v", got)
	}
}

// A changed agent answers the next message of a conversation already under way,
// and a changed bridge file restarts its listener and starts no new chat or
// conversation.
func TestChangedAgentAnswersTheNextMessage(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	echoing(t, dir)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "3.0", "text": "first"})
	w.until("the first answer", func() bool { return len(s.posted()) >= 1 })
	setSite(t, dir, "agents/helper.yaml", "${'[' + input.history + '] ' + input.said}", "${'changed: ' + input.said}")
	w.sync(dir)
	s.says("b", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "4.0", "text": "second"})
	w.until("the second answer", func() bool { return len(s.posted()) >= 2 })
	if got := fmt.Sprint(s.posted()[1]["markdown_text"]); got != "changed: second" {
		t.Fatalf("the bot said %q", got)
	}
	listener := w.kept(router("slack"))
	setSite(t, dir, router("slack"), "title: Slack bridge\n", "title: Slack bridge\nmeta: { changed: true }\n")
	s.mu.Lock()
	s.socket = nil
	s.mu.Unlock()
	w.sync(dir)
	if w.kept(router("slack")) == listener {
		t.Fatal("the changed bridge file did not restart its listener")
	}
	w.until("Slack connected again", s.connected)
	s.says("c", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "5.0", "text": "third"})
	w.until("the third answer", func() bool { return len(s.posted()) >= 3 })
	for _, path := range []string{".rook/blocks/chat.yaml", ".rook/blocks/conversation.yaml"} {
		if runs := keyed(w, path); len(runs) != 1 || len(slices.Collect(maps.Values(runs))[0]) != 1 {
			t.Errorf("%s has the runs %v", path, runs)
		}
	}
}

// A machine says a text in a Slack channel named as #name.
func TestSlackSaysInAChannelByName(t *testing.T) {
	t.Skip("R3 revives this: Slack")
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	if err := os.WriteFile(filepath.Join(dir, "on/log.yaml"), []byte(dedent(`
		flow: log
		stages:
		  say: { job: { machine: .rook/blocks/say.yaml, input: { address: { bridge: slack, id: "#approvals" }, text: Moved. } } }
	`)), 0o644); err != nil {
		t.Fatal(err)
	}
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
	t.Skip("R3 revives this: Slack")
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
