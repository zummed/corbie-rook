package rook_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	rook "github.com/zummed/corbie-rook"
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

// A comment on an issue shows each chart as its table.
func TestGitLabSaysAChartAsATable(t *testing.T) {
	gl := newGitLab(t)
	w := newWorld(t)
	w.secret("GITLAB_TOKEN", "gl-token")
	dir := pointed(t, "gitlab", gl.Server)
	setSite(t, dir, "bridges/gitlab.yaml", "gitlab.example.com", strings.TrimPrefix(gl.URL, "http://"))
	replying(t, dir, charts)
	gl.note(1, 2, "al", "before")
	w.sync(dir)
	w.until("the first ask", func() bool { return gl.asked() > 0 })
	gl.note(2, 2, "al", "@bot how many?")
	w.until("the answer", func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return len(gl.said()) > 0
	})
	got := gl.said()[0]
	for _, want := range []string{"Here.", "**Tickets**", "| Jan | 1 |", "| A | 3 |"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is not in %q", want, got)
		}
	}
	if strings.Contains(got, "```chart") {
		t.Errorf("a chart is left in %q", got)
	}
	if !strings.Contains(got, "```mermaid\ngraph LR; A-->B\n```") {
		t.Errorf("the diagram is not in %q", got)
	}
}

// A status shows under a thread while its message is answered, and is cleared
// when the answer says nothing.
func TestSlackShowsAStatusWhileAnswering(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	replying(t, dir, "")
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "thread_ts": "4.0", "ts": "5.0", "text": "hello"})
	w.until("two statuses", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.status) >= 2
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	got, _ := json.Marshal(s.status)
	if string(got) != `[{"channel_id":"D1","status":"Working on it.","thread_ts":"4.0"},{"channel_id":"D1","status":"","thread_ts":"4.0"}]` {
		t.Fatalf("the statuses are %s", got)
	}
}

// attendStub stands in for attend.yaml: it answers a message holding "please".
const attendStub = `
	statechart: attend-stub
	input: { said: string, asker: string, address: map, history: "any?" }
	initial: e
	states:
	  e: { end: done, output: { answer: "${input.said.includes('please')}" } }
`

// With overhear, a reply in a thread the bot has said something in goes to
// attend.yaml, and is answered only if it says so. A stranger's is not answered
// and not told anything, and a message outside a thread is not heard.
func TestSlackOverhearsAThreadItSpokeIn(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	echoing(t, dir)
	setSite(t, dir, "bridges/slack.yaml", `input: { dir: "${run.context}" }`, `input: { dir: "${run.context}", overhear: true }`)
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
	w.until("the answer", func() bool { return len(s.posted()) >= 1 })
	w.until("both asked", func() bool { return len(stubs(t, w, "attend-stub")) == 2 })
	time.Sleep(20 * interval)
	if got := s.posted(); len(got) != 1 || !strings.Contains(fmt.Sprint(got[0]["markdown_text"]), "please help") {
		t.Fatalf("the bot posted %v", got)
	}
	for _, run := range stubs(t, w, "attend-stub") {
		if history := fmt.Sprint(w.show(run.ID).Input.(map[string]any)["history"]); !strings.Contains(history, "you: my earlier reply") {
			t.Errorf("attend was given %q", history)
		}
	}
	if n := len(stubs(t, w, "attend-stub")); n != 2 {
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

// An opened issue, a work item here, is heard once nobody has changed it for
// settle, as it is then. A comment made while it waits is its history, and not
// heard on its own.
func TestGitLabHearsAnIssueOnceItSettles(t *testing.T) {
	gl := newGitLab(t)
	gl.text = "It fails. @bot help"
	gl.updated = time.Now().UTC().Format(time.RFC3339)
	w := newWorld(t)
	w.secret("GITLAB_TOKEN", "gl-token")
	dir := pointed(t, "gitlab", gl.Server)
	setSite(t, dir, "bridges/gitlab.yaml", "gitlab.example.com, projects: [group/project]", strings.TrimPrefix(gl.URL, "http://")+", projects: [group/project], settle: 2m")
	echoing(t, dir)
	gl.note(1, 2, "al", "before")
	w.sync(dir)
	w.until("the first ask", func() bool { return gl.asked() > 0 })
	again := func() {
		before := gl.asked()
		w.until("GitLab asked again", func() bool {
			w.set(w.now().Add(31 * time.Second))
			time.Sleep(5 * interval)
			return gl.asked() > before
		})
	}
	gl.happens(2, map[string]any{"author_id": 2, "author_username": "al", "target_type": "WorkItem", "action_name": "opened", "target_iid": 7})
	again()
	gl.note(3, 2, "al", "@bot also the tests")
	gl.mu.Lock()
	gl.thread = []map[string]any{{"id": 3, "body": "@bot also the tests", "author": map[string]any{"username": "al"}}}
	gl.mu.Unlock()
	again()
	again()
	time.Sleep(10 * interval)
	if got := gl.said(); len(got) != 0 {
		t.Fatalf("the bot said %v before the issue settled", got)
	}
	gl.mu.Lock()
	gl.text = "It fails on main. @bot help"
	gl.updated = time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	gl.mu.Unlock()
	again()
	w.until("the answer", func() bool { return len(gl.said()) >= 1 })
	time.Sleep(10 * interval)
	got := gl.said()
	if len(got) != 1 || !strings.Contains(got[0], "It fails on main.") || !strings.HasPrefix(got[0], "[al: @bot also the tests]") {
		t.Fatalf("the bot said %q", got)
	}
	w.titled(map[string][]string{
		"gitlab-bridge": {"GitLab bridge"},
		"gitlab-listen": {"GitLab listener"},
		"chat":          {"#7 Broken build"},
		"message":       {"#7 Broken build  It fails on main. @bot help"},
	})
}

// The ready-made attend asks a model, shown the thread and the message, and gives
// its answer.
func TestAttendAsksAModel(t *testing.T) {
	w := newWorld(t)
	dir := filepath.Join(t.TempDir(), "project")
	if err := rook.Init(dir, nil); err != nil {
		t.Fatal(err)
	}
	model := &llmstandin.Server{Answer: func(llmstandin.Call) llmstandin.Reply { return llmstandin.Reply{Text: `{"answer": true}`} }}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	setSite(t, dir, ".rook/attend/model.yaml", "model: { name: haiku, service: claude-code, secret: CLAUDE_TOKEN }",
		"model: { name: test-model, service: anthropic, address: "+web.URL+", secret: TEST_KEY }")
	draft, _, err := client.ReadMachine(dir, ".rook/attend/model.yaml")
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.Start(context.Background(), draft, client.Start{
		Input:   map[string]any{"said": "please look", "asker": "alice", "address": map[string]any{"bridge": "slack", "id": "C1/1.0"}, "history": "you: earlier words"},
		Secrets: map[string]string{"TEST_KEY": "sk-test"}})
	if err != nil {
		t.Fatal(err)
	}
	w.until("the answer", func() bool { return w.show(id).HasEnded() })
	if run := w.show(id); run.Status != "done" || fmt.Sprint(run.Output) != "map[answer:true]" {
		t.Fatalf("attend ended %s with %v: %s", run.Status, run.Output, run.Error)
	}
	shown := fmt.Sprint(model.Calls[0].Messages)
	for _, want := range []string{"you: earlier words", "from alice", "please look"} {
		if !strings.Contains(shown, want) {
			t.Errorf("%q is not in %s", want, shown)
		}
	}
}

// The starting attend.yaml answers no, and needs no model.
func TestStartingAttendSaysNo(t *testing.T) {
	w := newWorld(t)
	dir := filepath.Join(t.TempDir(), "project")
	if err := rook.Init(dir, nil); err != nil {
		t.Fatal(err)
	}
	draft, _, err := client.ReadMachine(dir, "attend.yaml")
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.Start(context.Background(), draft, client.Start{
		Input: map[string]any{"said": "please look", "asker": "alice", "address": map[string]any{"bridge": "slack", "id": "C1/1.0"}}})
	if err != nil {
		t.Fatal(err)
	}
	w.until("the answer", func() bool { return w.show(id).HasEnded() })
	if run := w.show(id); run.Status != "done" || fmt.Sprint(run.Output) != "map[answer:false]" {
		t.Fatalf("attend ended %s with %v: %s", run.Status, run.Output, run.Error)
	}
}

// A question in a Slack channel is posted there, and the first tap from someone in
// the group it names answers it; the message then shows who answered, with no
// buttons. rules.yaml names such approvers as { people, in }.
func TestQuestionInAChannelCountsTheGroup(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	for name, text := range map[string]string{
		"people.yaml": "people:\n  alice: { slack: U1 }\n  bob: { slack: U2 }\ngroups:\n  team: [bob]\n",
		"rules.yaml":  "places: {}\nagents:\n  - { name: helper, for: [alice] }\napprove:\n  deploys: { people: [team], in: \"#approvals\" }\n",
		"on/asker.yaml": dedent(`
			statechart: asker
			initial: who
			states:
			  who:
			    job: { machine: .rook/blocks/approvers.yaml, input: { part: deploys } }
			    on: { done: asking }
			  asking:
			    job: { machine: .rook/blocks/ask.yaml, input: { people: "${states.who.output.people}", in: "${states.who.output.in}", text: "Deploy?", choices: ["yes", "no"] } }
			    on: { done: over }
			  over: { end: done, output: "${states.asking.output.choice}" }
		`),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.sync(dir)
	asker := w.kept("on/asker.yaml")
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

// A message after a bridge's file changes starts a new conversation, with the
// machines as they are then, and a conversation already under way carries on.
func TestChangedBridgeStartsNewConversations(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	echoing(t, dir)
	w.sync(dir)
	w.until("Slack connected", s.connected)
	s.says("a", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "3.0", "text": "first"})
	w.until("the first answer", func() bool { return len(s.posted()) >= 1 })
	chats := func() []*client.Run {
		var out []*client.Run
		for _, run := range stubs(t, w, "chat") {
			if run.Status == "running" {
				out = append(out, run)
			}
		}
		return out
	}
	first := chats()
	if len(first) != 1 {
		t.Fatalf("%d conversations", len(first))
	}
	bridge := filepath.Join(dir, "bridges/slack.yaml")
	text, err := os.ReadFile(bridge)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bridge, append(text, []byte("# changed\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.socket = nil
	s.mu.Unlock()
	w.sync(dir)
	w.until("Slack connected again", s.connected)
	s.says("b", map[string]any{"type": "message", "channel": "D1", "channel_type": "im", "user": "U1", "ts": "4.0", "text": "second"})
	w.until("the second answer", func() bool { return len(s.posted()) >= 2 })
	now := chats()
	if len(now) != 2 || now[0].Key == now[1].Key {
		t.Fatalf("the conversations are %v", now)
	}
	if got := fmt.Sprint(s.posted()[1]["markdown_text"]); !strings.HasSuffix(got, "second") {
		t.Fatalf("the bot said %q", got)
	}
}

// A machine says a text in a Slack channel named as #name.
func TestSlackSaysInAChannelByName(t *testing.T) {
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

// A form is posted in the asker's thread with its fields, and a press of Send by
// the asker gives the fields' values. The message then shows them.
func TestFormIsAnsweredInTheThread(t *testing.T) {
	s := newSlack(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	if err := os.WriteFile(filepath.Join(dir, "on/former.yaml"), []byte(dedent(`
		flow: former
		stages:
		  form:
		    job:
		      machine: .rook/blocks/form.yaml
		      input:
		        address: { bridge: slack, id: "C1/4.0" }
		        asker: alice
		        text: "The bug?"
		        fields: [{ name: title, label: Title, kind: text }, { name: size, label: Size, kind: choice, choices: [small, big] }, { name: hours, label: Hours, kind: number }]
		output: ${stages.form.output}
	`)), 0o644); err != nil {
		t.Fatal(err)
	}
	w.sync(dir)
	former := w.kept("on/former.yaml")
	w.until("Slack connected", s.connected)
	w.until("the form", func() bool { return len(s.posted()) == 1 })
	post := s.posted()[0]
	if post["channel"] != "C1" || post["thread_ts"] != "4.0" {
		t.Fatalf("the form went to %v", post)
	}
	blocks, _ := json.Marshal(post["blocks"])
	for _, want := range []string{
		`"block_id":"title"`, `"type":"plain_text_input"`,
		`"options":[{"text":{"text":"small","type":"plain_text"},"value":"small"}`,
		`"type":"number_input"`, `"action_id":"choice-1"`,
	} {
		if !strings.Contains(string(blocks), want) {
			t.Errorf("%s is not in %s", want, blocks)
		}
	}
	value := strings.Split(strings.Split(string(blocks), `"value":"`)[3], " ")[0]
	s.send(map[string]any{"envelope_id": "a", "type": "interactive", "payload": map[string]any{
		"type": "block_actions", "user": map[string]any{"id": "U1"},
		"actions": []any{map[string]any{"action_id": "choice-0", "value": value + " send"}},
		"state": map[string]any{"values": map[string]any{
			"title": map[string]any{"title": map[string]any{"type": "plain_text_input", "value": "Login fails"}},
			"size":  map[string]any{"size": map[string]any{"type": "static_select", "selected_option": map[string]any{"value": "big"}}},
			"hours": map[string]any{"hours": map[string]any{"type": "number_input", "value": "2.5"}},
		}},
	}})
	w.until("the form done", func() bool { return w.show(former).Status == "done" })
	got, _ := json.Marshal(w.show(former).Output)
	if string(got) != `{"choice":"send","values":{"hours":2.5,"size":"big","title":"Login fails"}}` {
		t.Fatalf("the form gave %s", got)
	}
	w.until("the form closed", func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.updates) == 1
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if got := s.updates[0]["text"]; got != "The bug?\n\nalice said send.\nTitle: Login fails\nSize: big\nHours: 2.5" {
		t.Fatalf("the form was changed to %q", got)
	}
}

// A form on a bridge other than Slack is not asked, and gives a note saying so.
func TestFormOffSlackGivesANote(t *testing.T) {
	w := newWorld(t)
	dir := slackProject(t, w, newSlack(t))
	if err := os.WriteFile(filepath.Join(dir, "on/former.yaml"), []byte(dedent(`
		flow: former
		stages:
		  form: { job: { machine: .rook/blocks/form.yaml, input: { address: { bridge: gitlab, id: "1/2" }, asker: alice, text: "The bug?", fields: [{ name: title, label: Title, kind: text }] } } }
		output: ${stages.form.output}
	`)), 0o644); err != nil {
		t.Fatal(err)
	}
	w.sync(dir)
	former := w.started("on/former.yaml")
	w.until("the form done", func() bool { return w.show(former).Status == "done" })
	if got := fmt.Sprint(w.show(former).Output); !strings.Contains(got, "only on Slack") {
		t.Fatalf("the form gave %s", got)
	}
	w.titled(map[string][]string{"form": {"The bug?"}})
}
