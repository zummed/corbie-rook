package rook_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	rook "github.com/zummed/corbie-rook"
	"github.com/zummed/corbie/client"
	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
	"go.yaml.in/yaml/v3"
)

// telegramScript stands in for telegram.js. It hears two messages from one
// person and one from a stranger, and then fails, which rests the listener.
const telegramScript = `
	if (input.do === "hear") {
	  if (input.cursor !== null) throw new Error("nothing more")
	  emit("message", { bridge: "telegram", text: "one", chat: "1", from: "1", place: "dm", answers: "1:10" })
	  emit("message", { bridge: "telegram", text: "two", chat: "1", from: "1", place: "dm", answers: "1:11" })
	  emit("message", { bridge: "telegram", text: "hello", chat: "9", from: "9", place: "dm", answers: "9:12" })
	  return { cursor: 1 }
	}
	if (input.do === "ask") return {}
	return { sent: [input.answers || ""] }
`

// helperStub stands in for the agent: it counts the turns of its conversation
// in its reply.
const helperStub = `
	statechart: helper
	input: { said: string, asker: string, home: string, conversation: "any?" }
	initial: e
	states:
	  e: { end: done, output: { reply: "${input.said}", conversation: "${(input.conversation || 0) + 1}" } }
`

// stubReviewer stands in for the reviewer, and leaves every call to a person.
const stubReviewer = "statechart: reviewer\ninput: { context: string }\ninitial: e\nstates:\n  e: { end: done, output: { reply: { says: refer, note: why } } }\n"

// realAgent puts the starter's helper back in a project, with a stand-in model
// at an address.
func realAgent(t *testing.T, dir, address string) {
	t.Helper()
	for _, name := range []string{"agents/helper.yaml"} {
		text, err := os.ReadFile(filepath.Join("starter", name))
		if err != nil {
			t.Fatal(err)
		}
		written := strings.Replace(string(text), "model: { name: claude-sonnet-5-5, secret: ANTHROPIC_KEY }",
			"model: { name: test-model, service: anthropic, address: "+address+", secret: TEST_KEY }", 1)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(written), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// copyStarter makes a project with Telegram, with its bridge's script and its
// helper standing in for what needs a network or a model.
func copyStarter(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project")
	if err := rook.Init(dir, []string{"telegram"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "on"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		".rook/bridges/telegram/telegram.js": dedent(telegramScript),
		"agents/helper.yaml":                 dedent(helperStub),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The stand-in script uses no secret, and the test's controller holds none.
	for _, name := range []string{".rook/bridges/telegram/listen.yaml", ".rook/bridges/telegram/say.yaml", ".rook/bridges/telegram/ask.yaml"} {
		full := filepath.Join(dir, name)
		text, err := os.ReadFile(full)
		if err != nil {
			t.Fatal(err)
		}
		cut := strings.ReplaceAll(string(text), `, "secret:TELEGRAM_TOKEN@net:api.telegram.org"`, "")
		if err := os.WriteFile(full, []byte(cut), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The starter's machines: two messages from one person go to one chat, one
// at a time, the second carrying on the first's conversation, and a stranger
// is told the bot is not theirs.
func TestChatAnswersOneMessageAtATime(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	w.sync(dir)
	w.kept("bridges/telegram.yaml")
	var chat *client.Run
	w.until("two chats", func() bool {
		chats := 0
		for _, run := range w.roots(nil) {
			if strings.HasPrefix(run.Key, "telegram:") && strings.HasSuffix(run.Key, ":9") {
				chats++
			}
			if strings.HasPrefix(run.Key, "telegram:") && strings.HasSuffix(run.Key, ":1") {
				chats++
				chat = run
			}
		}
		return chats == 2
	})
	var answers []*client.Run
	w.until("both messages answered", func() bool {
		answers = nil
		runs, err := w.List(context.Background(), client.Filter{Finished: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if run.Machine == "message" && run.Parent == chat.ID && run.Status == "done" {
				answers = append(answers, run)
			}
		}
		return len(answers) == 2 && w.show(chat.ID).Timers == 1
	})
	replies := map[any]bool{}
	for _, run := range answers {
		replies[w.show(run.ID).Output.(map[string]any)["conversation"]] = true
	}
	if !replies[1.0] || !replies[2.0] {
		t.Fatalf("the conversations are %v", replies)
	}
	w.until("the stranger told", func() bool {
		runs, err := w.List(context.Background(), client.Filter{Finished: true})
		if err != nil {
			t.Fatal(err)
		}
		told := false
		for _, run := range runs {
			if run.Machine == "answer" && w.show(run.Parent).Input.(map[string]any)["event"].(map[string]any)["from"] == "9" {
				t.Fatalf("the stranger's message was answered by %v", run)
			}
			if run.Machine == "say" && strings.Contains(fmt.Sprint(w.show(run.ID).Input), "only the people") {
				told = true
			}
		}
		return told
	})
}

// A message goes to the agent who chose and to no other, and an agent with no
// stage answers nothing.
func TestAnswerStartsTheAgentWhoChose(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	stub := "statechart: NAME\ninitial: e\nstates:\n  e: { end: done, output: { reply: NAME } }\n"
	for _, name := range []string{"helper", "team"} {
		if err := os.WriteFile(filepath.Join(dir, "agents", name+".yaml"), []byte(strings.ReplaceAll(stub, "NAME", name)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	try := `
		flow: try
		stages:
		  helper: { job: { machine: answer.yaml, input: { agent: helper, message: {} } } }
		  team: { job: { machine: answer.yaml, input: { agent: team, message: {} } } }
		  nobody: { job: { machine: answer.yaml, input: { agent: nobody, message: {} } } }
		output: { helper: "${stages.helper.output.reply}", team: "${stages.team.output.reply}", nobody: "${stages.nobody.output ? stages.nobody.output.reply : 'none'}" }
	`
	if err := os.WriteFile(filepath.Join(dir, "on/try.yaml"), []byte(dedent(try)), 0o644); err != nil {
		t.Fatal(err)
	}
	w.sync(dir)
	id := w.started("on/try.yaml")
	w.until("the answers", func() bool { return w.show(id).Status != "running" })
	run := w.show(id)
	if got := fmt.Sprint(run.Status, run.Output); got != "donemap[helper:helper nobody:none team:team]" {
		t.Errorf("the answers were %s, %v", got, run.Error)
	}
}

// Every agent rules.yaml lists has a stage that starts it: with none, a message
// to it would get no answer.
func TestEveryAgentCanBeStarted(t *testing.T) {
	people, err := os.ReadFile("starter/rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	answer, err := os.ReadFile("starter/answer.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Agents []struct{ Name string } `yaml:"agents"`
	}
	if err := yaml.Unmarshal(people, &table); err != nil {
		t.Fatal(err)
	}
	for _, agent := range table.Agents {
		if !strings.Contains(string(answer), fmt.Sprintf("input.agent === '%s'", agent.Name)) {
			t.Errorf("no stage starts the agent %s", agent.Name)
		}
	}
	if len(table.Agents) == 0 {
		t.Error("rules.yaml lists no agent")
	}
}

// A part's approvers are the ones rules.yaml names for it, each a person or a
// group.
func TestApproversAreThoseOfThePart(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	rules, err := os.ReadFile(filepath.Join(dir, "rules.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	table := strings.Replace(string(rules), "  messages: [alice]\n", "  messages: [bob]\n  deploys: [team]\n  net: [alice]\n", 1)
	try := `
		flow: try
		stages:
		  net: { job: { machine: .rook/blocks/approvers.yaml, input: { part: net } } }
		  messages: { job: { machine: .rook/blocks/approvers.yaml, input: { part: messages } } }
		  deploys: { job: { machine: .rook/blocks/approvers.yaml, input: { part: deploys } } }
		output: { net: "${stages.net.output}", messages: "${stages.messages.output}", deploys: "${stages.deploys.output}" }
	`
	for name, text := range map[string]string{"rules.yaml": table, "on/try.yaml": dedent(try)} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.sync(dir)
	id := w.started("on/try.yaml")
	w.until("the approvers", func() bool { return w.show(id).Status != "running" })
	if got := fmt.Sprint(w.show(id).Output); got != "map[deploys:map[in: people:[team]] messages:map[in: people:[bob]] net:map[in: people:[alice]]]" {
		t.Errorf("the approvers were %s", got)
	}
}

// An answer that fails ends the conversation, so one the model cannot take does
// not fail every later message of the chat.
func TestChatStartsAfreshAfterAFailedAnswer(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	for name, text := range map[string]string{
		".rook/bridges/telegram/telegram.js": `
			if (input.do === "hear") {
			  if (input.cursor !== null) throw new Error("nothing more")
			  emit("message", { bridge: "telegram", text: "one", chat: "1", from: "1", place: "dm", answers: "1:10" })
			  emit("message", { bridge: "telegram", text: "two", chat: "1", from: "1", place: "dm", answers: "1:11" })
			  emit("message", { bridge: "telegram", text: "three", chat: "1", from: "1", place: "dm", answers: "1:12" })
			  emit("message", { bridge: "telegram", text: "four", chat: "1", from: "1", place: "dm", answers: "1:13" })
			  return { cursor: 1 }
			}
			return { sent: [input.answers || ""] }
		`,
		"boom.js": `if (input.said === "two") throw new Error("the model cannot take this")
			return {}`,
		"agents/helper.yaml": `
			statechart: helper
			input: { said: string, asker: string, home: string, conversation: "any?" }
			initial: work
			states:
			  work:
			    job: { script: boom.js, input: { said: "${input.said}" } }
			    on: { done: e }
			  e: { end: done, output: { reply: "${input.said}", conversation: "${(input.conversation || 0) + 1}" } }
		`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(dedent(text)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.sync(dir)
	w.kept("bridges/telegram.yaml")
	got := map[string]any{}
	w.until("four messages answered", func() bool {
		runs, err := w.List(context.Background(), client.Filter{Finished: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if run.Machine == "message" && run.Status == "done" {
				shown := w.show(run.ID)
				event := shown.Input.(map[string]any)["event"].(map[string]any)
				got[event["text"].(string)] = shown.Output.(map[string]any)["conversation"]
			}
		}
		return len(got) == 4
	})
	if want := map[string]any{"one": 1.0, "two": nil, "three": 1.0, "four": 2.0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the conversations are %v", got)
	}
}

// removeListener removes the one machine the project keeps running.
func removeListener(t *testing.T, dir string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, "bridges/telegram.yaml")); err != nil {
		t.Fatal(err)
	}
}

// A tool marked approve: true waits for its part's approvers, and a no comes back
// to the model with nothing done.
func TestHeldCallWaitsForItsApprovers(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	replies := []llmstandin.Reply{
		{Calls: []llmstandin.ToolCall{{ID: "c1", Name: "open", Args: `{"part": "messages"}`}}},
		{Calls: []llmstandin.ToolCall{{ID: "c2", Name: "tell", Args: `{"person": "bob", "text": "hello"}`}}},
		{Text: "done"},
	}
	model := &llmstandin.Server{Answer: func(c llmstandin.Call) llmstandin.Reply { return replies[c.Turn-1] }}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	if err := os.WriteFile(filepath.Join(dir, "agents/reviewer.yaml"), []byte(stubReviewer), 0o644); err != nil {
		t.Fatal(err)
	}
	draft, _, err := client.ReadMachine(dir, "agents/helper.yaml")
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.Start(context.Background(), draft, client.Start{
		Input:   map[string]any{"said": "q", "asker": "alice", "home": dir},
		Secrets: map[string]string{"TEST_KEY": "sk-test"}})
	if err != nil {
		t.Fatal(err)
	}
	var question string
	w.until("the approver asked", func() bool {
		runs, err := w.List(context.Background(), client.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if strings.HasPrefix(run.Key, "question:") && run.Timers == 1 {
				question = run.Key
				if text := fmt.Sprint(w.show(run.ID).Input); !strings.Contains(text, "Request, from alice: q") {
					t.Errorf("the approver was shown %s", text)
				}
			}
		}
		return question != ""
	})
	if _, err := w.SendToKey(context.Background(), question, "answer", map[string]any{"bridge": "telegram", "from": "1", "choice": "no"}, 0); err != nil {
		t.Fatal(err)
	}
	w.until("the helper to end", func() bool { return w.show(id).Status != "running" })
	if run := w.show(id); run.Status != "done" {
		t.Fatalf("the helper ended %s: %v", run.Status, run.Error)
	}
	said := model.Calls[2].Messages
	text, _ := json.Marshal(said[len(said)-1])
	if !strings.Contains(string(text), "The approver said no") {
		t.Errorf("the held call came back as %s", text)
	}
	runs, err := w.List(context.Background(), client.Filter{Finished: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		if run.Machine == "tell" {
			t.Errorf("the message was sent after a no: %v", run)
		}
	}
}

// workPeople and workRules are a team with a public channel, a channel of its
// own, and an agent for anyone.
const workPeople = `
	people:
	  sam: { slack: U1 }
	  ana: { slack: U2 }
	  cara: { slack: U3 }
	groups:
	  platform: [sam, ana]
`

const workRules = `
	places:
	  "#help": { readers: [anyone] }
	  "#platform": { readers: [platform] }
	  dm: { readers: [asker] }
	agents:
	  - { name: team, for: [platform] }
	  - { name: support, for: [anyone] }
`

// who gives the first agent the asker and everyone who reads the answer may each
// use, and no agent when there is none. An empty table is the starter's own.
func TestWhoGivesAnAgentTheAskerAndTheReadersMayUse(t *testing.T) {
	type row struct{ bridge, id, place, agent string }
	for table, rows := range map[string][]row{
		workPeople: {
			{"slack", "U3", "#help", "support"},
			{"slack", "U1", "#help", "support"},
			{"slack", "U3", "dm", "support"},
			{"slack", "U1", "dm", "team"},
			{"slack", "U1", "#platform", "team"},
			{"slack", "U3", "#platform", "support"},
			{"slack", "U1", "#not-listed", "support"},
			{"slack", "U9", "#help", "support"},
			{"slack", "U9", "dm", "support"},
		},
		"": {
			{"telegram", "1", "dm", "helper"},
			{"telegram", "2", "dm", "team"},
			{"telegram", "1", "group", ""},
			{"telegram", "9", "dm", ""},
		},
	} {
		w := newWorld(t)
		dir := copyStarter(t)
		removeListener(t, dir)
		if table != "" {
			for name, text := range map[string]string{"people.yaml": table, "rules.yaml": workRules} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(dedent(text)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		var stages strings.Builder
		for i, r := range rows {
			fmt.Fprintf(&stages, "  r%d: { job: { machine: .rook/blocks/who.yaml, input: { bridge: %s, id: %q, place: %q } } }\n", i, r.bridge, r.id, r.place)
		}
		try := "flow: try\nstages:\n" + stages.String() + "output: \"${stages}\"\n"
		if err := os.WriteFile(filepath.Join(dir, "on/try.yaml"), []byte(try), 0o644); err != nil {
			t.Fatal(err)
		}
		w.sync(dir)
		id := w.started("on/try.yaml")
		w.until("the rows", func() bool { return w.show(id).Status != "running" })
		run := w.show(id)
		if run.Status != "done" {
			t.Fatalf("%s: the flow ended %s: %v", table, run.Status, run.Error)
		}
		out := run.Output.(map[string]any)
		for i, r := range rows {
			got := out[fmt.Sprintf("r%d", i)].(map[string]any)["output"].(map[string]any)
			agent, _ := got["agent"].(string)
			if agent != r.agent {
				t.Errorf("%s, %s in %s: the agent %q, and not %q", table, r.id, r.place, agent, r.agent)
			}
		}
	}
}

// A part's approval: a refusal ends it, the reviewer's yes ends it for a tool in
// alone, and the part's approver decides everything else.
func TestApprovalGoesToTheReviewerThenAPerson(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	people, err := os.ReadFile(filepath.Join(dir, "people.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"people.yaml": strings.ReplaceAll(string(people), "alice: { telegram: \"1\" }", "alice: { telegram: \"1\" }\n  carol: { telegram: \"3\" }"),
		"agents/reviewer.yaml": `
			statechart: reviewer
			input: { context: string }
			initial: e
			states:
			  e: { end: done, output: { reply: { says: "${input.context.match(/yes|refuse|refer/)[0]}", note: why } } }
		`,
		"on/try.yaml": `
			flow: try
			stages:
			  alone_yes: { job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, alone: [mend], jobs: [{ name: mend, input: { r: yes } }] } } }
			  alone_no: { job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, alone: [mend], jobs: [{ name: mend, input: { r: refuse } }] } } }
			  held_no: { job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, jobs: [{ name: mend, input: { r: refuse } }] } } }
			  alone_unsure:
			    job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, alone: [mend], jobs: [{ name: mend, input: { r: refer } }] } }
			  held_yes:
			    follows: [alone_unsure]
			    job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, jobs: [{ name: mend, input: { r: yes } }] } }
			  other_yes:
			    follows: [held_yes]
			    job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, alone: [mend], jobs: [{ name: mend, input: { r: yes } }, { name: burn, input: {} }] } }
			  again_yes:
			    job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, alone: [mend], jobs: [{ name: "mend#2", input: { r: yes } }, { name: mend.1, input: { r: yes } }] } }
			output:
			  alone_yes: "${stages.alone_yes.output.approve}"
			  alone_no: "${stages.alone_no.output.approve}"
			  held_no: "${stages.held_no.output.approve}"
			  alone_unsure: "${stages.alone_unsure.output.approve}"
			  held_yes: "${stages.held_yes.output.approve}"
			  other_yes: "${stages.other_yes.output.approve}"
			  again_yes: "${stages.again_yes.output.approve}"
		`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(dedent(text)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.sync(dir)
	try := w.kept("on/try.yaml")
	asked := map[string]bool{}
	for _, choice := range []string{"yes", "no", "yes"} {
		var question string
		w.until("the approver asked", func() bool {
			runs, err := w.List(context.Background(), client.Filter{})
			if err != nil {
				t.Fatal(err)
			}
			for _, run := range runs {
				if strings.HasPrefix(run.Key, "question:") && run.Timers == 1 && !asked[run.Key] {
					question = run.Key
				}
			}
			return question != ""
		})
		asked[question] = true
		if _, err := w.SendToKey(context.Background(), question, "answer", map[string]any{"bridge": "telegram", "from": "1", "choice": choice}, 0); err != nil {
			t.Fatal(err)
		}
	}
	w.until("the flow to end", func() bool { return w.show(try).Status != "running" })
	want := map[string]any{"alone_yes": []any{"mend"}, "alone_no": []any{}, "held_no": []any{}, "alone_unsure": []any{"mend"}, "held_yes": []any{}, "other_yes": []any{"mend", "burn"}, "again_yes": []any{"mend#2", "mend.1"}}
	if got := w.show(try).Output; !reflect.DeepEqual(got, want) {
		t.Fatalf("the approvals were %v", got)
	}
}

// A held call goes to the part's own approvers: only their answer counts, and
// nobody else is asked.
func TestApprovalAsksThePartsApprovers(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	rules, err := os.ReadFile(filepath.Join(dir, "rules.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"rules.yaml":           strings.Replace(string(rules), "  messages: [alice]\n", "  messages: [bob]\n", 1),
		"agents/reviewer.yaml": stubReviewer,
		"on/try.yaml": dedent(`
			flow: try
			stages:
			  held: { job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, jobs: [{ name: tell, input: {} }] } } }
			output: "${stages.held.output.approve}"
		`),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.sync(dir)
	try := w.started("on/try.yaml")
	var question *client.Run
	w.until("the approver asked", func() bool {
		runs, err := w.List(context.Background(), client.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if strings.HasPrefix(run.Key, "question:") && run.Timers == 1 {
				question = run
			}
		}
		return question != nil
	})
	want := []any{"bob"}
	if got := w.show(question.ID).Input.(map[string]any)["people"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("it asked %v", got)
	}
	for _, from := range []string{"1", "2"} {
		if _, err := w.SendToKey(context.Background(), question.Key, "answer", map[string]any{"bridge": "telegram", "from": from, "choice": "yes"}, 0); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * interval)
		if ended := w.show(question.ID).Status != "running"; ended != (from == "2") {
			t.Fatalf("after an answer from %s the question is %s", from, w.show(question.ID).Status)
		}
	}
	w.until("the approval", func() bool { return w.show(try).Status != "running" })
	if got := w.show(try).Output; !reflect.DeepEqual(got, []any{"tell"}) {
		t.Fatalf("it approved %v", got)
	}
}

// An id counts only on its own bridge: someone with another person's id on another
// service is not that person.
func TestAnIdCountsOnlyOnItsBridge(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	for name, text := range map[string]string{
		"people.yaml": "people:\n  alice: { telegram: \"1\", slack: U1 }\ngroups: {}\n",
		"on/try.yaml": dedent(`
			flow: try
			stages:
			  same: { job: { machine: .rook/blocks/who.yaml, input: { bridge: slack, id: U1, place: dm } } }
			  other: { job: { machine: .rook/blocks/who.yaml, input: { bridge: slack, id: "1", place: dm } } }
			  none: { job: { machine: .rook/blocks/who.yaml, input: { bridge: gitlab, id: "undefined", place: dm } } }
			output: { same: "${stages.same.output.person}", other: "${stages.other.output.person}", none: "${stages.none.output.person}" }
		`),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.sync(dir)
	id := w.started("on/try.yaml")
	w.until("the people", func() bool { return w.show(id).Status != "running" })
	if got := fmt.Sprint(w.show(id).Output); got != "map[none:<nil> other:<nil> same:alice]" {
		t.Fatalf("the people were %s, %v", got, w.show(id).Error)
	}
}

// A question goes to a run with a key its caller never names, and a tap by a
// person it names answers it, on any of their bridges. A tap by anyone else
// changes nothing.
func TestAskPersonIsAnsweredByATap(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	// The test's controller holds no secret, and this question is asked of no one.
	for name, text := range map[string]string{
		".rook/bridges/slack/ask.yaml": "flow: slack-ask\ninput: { to: list, text: string, choices: list, question: string }\nstages:\n  question: { job: { script: ./asked.js } }\n",
		".rook/bridges/slack/asked.js": "return {}",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "on/asker.yaml"), []byte(dedent(`
		statechart: asker
		initial: asking
		states:
		  asking:
		    job: { machine: .rook/blocks/ask.yaml, input: { people: [bob], text: "Well?", choices: ["yes", "no"] } }
		    on: { done: over }
		  over: { end: done, output: "${states.asking.output.choice}" }
	`)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "people.yaml"), []byte("people:\n  alice: { telegram: \"1\" }\n  bob: { telegram: \"2\", slack: \"5\" }\ngroups: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeListener(t, dir)
	w.sync(dir)
	asker := w.kept("on/asker.yaml")
	var question string
	w.until("the question waiting", func() bool {
		runs, err := w.List(context.Background(), client.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if strings.HasPrefix(run.Key, "question:") && run.Timers == 1 {
				question = run.Key
			}
		}
		return question != ""
	})
	if _, err := w.SendToKey(context.Background(), question, "answer", map[string]any{"bridge": "telegram", "from": "3", "choice": "yes"}, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * interval)
	if run := w.show(asker); run.Status != "running" {
		t.Fatalf("a tap by someone else ended the asker: %+v", run)
	}
	if _, err := w.SendToKey(context.Background(), question, "answer", map[string]any{"bridge": "telegram", "from": "1", "choice": "yes"}, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * interval)
	if run := w.show(asker); run.Status != "running" {
		t.Fatalf("a tap by someone not asked ended the asker: %+v", run)
	}
	if _, err := w.SendToKey(context.Background(), question, "answer", map[string]any{"bridge": "slack", "from": "5", "choice": "no"}, 0); err != nil {
		t.Fatal(err)
	}
	w.until("the asker done", func() bool { return w.show(asker).Status == "done" })
	if got := w.show(asker).Output; got != "no" {
		t.Fatalf("the choice was %v", got)
	}
}

// Every machine of a project with every bridge reads, and only the bridges' own
// files are kept running.
func TestProjectReads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := rook.Init(dir, rook.Bridges()); err != nil {
		t.Fatal(err)
	}
	found, err := client.ReadMachines(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range found {
		if m.Problem != nil {
			t.Errorf("%s: %v", m.Path, m.Problem)
		}
	}
	kept, err := client.ReadMachines(dir, "bridges")
	if err != nil {
		t.Fatal(err)
	}
	var always []string
	for _, m := range kept {
		always = append(always, m.Path)
	}
	if want := []string{"bridges/gitlab.yaml", "bridges/jira.yaml", "bridges/slack.yaml", "bridges/telegram.yaml"}; !reflect.DeepEqual(always, want) {
		t.Errorf("kept running: %v", always)
	}
}

// A project with no bridge reads, so one a project does not run costs it nothing.
func TestBridgesOnlyRunWhenChosen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := rook.Init(dir, []string{"slack", "gitlab"}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"bridges/slack.yaml": true, "bridges/gitlab.yaml": true, "bridges/telegram.yaml": false, "bridges/jira.yaml": false, ".rook/bridges/telegram/say.yaml": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != want {
			t.Errorf("%s there: %v, and not %v", name, err == nil, want)
		}
	}
}

// Init again rewrites .rook, and changes nothing of the person's.
func TestInitAgainKeepsThePersonsFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := rook.Init(dir, []string{"slack"}); err != nil {
		t.Fatal(err)
	}
	mine := "people:\n  me: { slack: U1 }\ngroups: {}\n"
	stale := filepath.Join(dir, ".rook/blocks/stale.yaml")
	for name, text := range map[string]string{"people.yaml": mine, "bridges/slack.yaml": "# mine\n", ".rook/blocks/tell.yaml": "changed", ".rook/blocks/stale.yaml": "old"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"agents/team.yaml", "attend.yaml"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := rook.Init(dir, []string{"slack"}); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"people.yaml": mine, "bridges/slack.yaml": "# mine\n"} {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != want {
			t.Errorf("%s is now %q", name, got)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "agents/team.yaml")); err == nil {
		t.Error("a starting file the person removed came back")
	}
	if _, err := os.Stat(filepath.Join(dir, "attend.yaml")); err != nil {
		t.Error("a file .rook reads by name was not written")
	}
	for _, name := range []string{"agents/team.yaml", "bridges/slack.yaml", "bridges/jira.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, ".rook/starter", name)); err != nil {
			t.Errorf("the starting file %s is not in .rook/starter", name)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".rook/blocks/tell.yaml")); string(got) == "changed" {
		t.Error(".rook was not rewritten")
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("a file Rook no longer ships stayed in .rook")
	}
}

func TestInitRefusesABridgeThereIsNot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := rook.Init(dir, []string{"carrier-pigeon"}); err == nil || !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Fatalf("Init gave %v", err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Error("it wrote a directory")
	}
}

// sayings gives the address of everything a project's bridge was told to say, by
// its text, with the `to` the bridge's own say was given.
func sayings(t *testing.T, w *world) map[string]map[string]any {
	t.Helper()
	runs, err := w.List(context.Background(), client.Filter{Finished: true})
	if err != nil {
		t.Fatal(err)
	}
	said := map[string]map[string]any{}
	for _, run := range runs {
		if run.Machine != "say" {
			continue
		}
		input := w.show(run.ID).Input.(map[string]any)
		text := input["text"].(string)
		if said[text] == nil {
			said[text] = map[string]any{}
		}
		if address, ok := input["address"].(map[string]any); ok {
			maps.Copy(said[text], address)
		}
		if to, ok := input["to"]; ok {
			said[text]["to"] = to
		}
	}
	return said
}

// A message that names a held tool waits for its approver through every level
// from the bridge down, and a yes tells the person it names.
func TestApprovalFromAChatGetsToTheApprover(t *testing.T) {
	w := newWorld(t)
	w.secret("TEST_KEY", "sk-test")
	dir := copyStarter(t)
	replies := []llmstandin.Reply{
		{Calls: []llmstandin.ToolCall{{ID: "c1", Name: "open", Args: `{"part": "messages"}`}}},
		{Calls: []llmstandin.ToolCall{{ID: "c2", Name: "tell", Args: `{"person": "bob", "text": "hello"}`}}},
		{Text: "told"},
	}
	turn := 0
	model := &llmstandin.Server{Answer: func(llmstandin.Call) llmstandin.Reply {
		turn++
		return replies[min(turn, len(replies))-1]
	}}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	for name, text := range map[string]string{
		".rook/bridges/telegram/telegram.js": dedent(oneMessage),
		"agents/reviewer.yaml":               stubReviewer,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w.sync(dir)
	w.kept("bridges/telegram.yaml")
	var question string
	w.until("the approver asked", func() bool {
		runs, err := w.List(context.Background(), client.Filter{})
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if strings.HasPrefix(run.Key, "question:") && run.Timers == 1 {
				question = run.Key
			}
		}
		return question != ""
	})
	if _, err := w.SendToKey(context.Background(), question, "answer", map[string]any{"bridge": "telegram", "from": "1", "choice": "yes"}, 0); err != nil {
		t.Fatal(err)
	}
	w.until("the reply said", func() bool { return sayings(t, w)["told"]["to"] != nil })
	said := sayings(t, w)
	if to := said["hello"]; to == nil || to["bridge"] != "telegram" || to["id"] != "2" || to["to"] != "2" {
		t.Errorf("the person was told at %v", to)
	}
	if to := said["told"]; to["id"] != "1" || to["to"] != "1" || to["answers"] != "1:10" {
		t.Errorf("the reply went to %v", to)
	}
}

// oneMessage stands in for telegram.js, and hears one message.
const oneMessage = `
	if (input.do === "hear") {
	  if (input.cursor !== null) throw new Error("nothing more")
	  emit("message", { bridge: "telegram", text: "one", chat: "1", from: "1", place: "dm", answers: "1:10" })
	  return { cursor: 1 }
	}
	return { sent: [input.answers || ""] }
`

// A machine that answers can say a text at the message's address while it works,
// and an empty reply or no output says nothing more and keeps the chat going.
func TestMachineSaysAsItWorksAndGivesNoReply(t *testing.T) {
	for name, output := range map[string]string{"an empty reply": `output: { reply: "" }`, "no output": ""} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			dir := copyStarter(t)
			for name, text := range map[string]string{
				".rook/bridges/telegram/telegram.js": `
					if (input.do === "hear") {
					  if (input.cursor !== null) throw new Error("nothing more")
					  emit("message", { bridge: "telegram", text: "one", chat: "1", from: "1", place: "dm", answers: "1:10" })
					  emit("message", { bridge: "telegram", text: "two", chat: "1", from: "1", place: "dm", answers: "1:11" })
					  return { cursor: 1 }
					}
					return { sent: [input.answers || ""] }
				`,
				"agents/helper.yaml": `
					flow: helper
					input: { said: string, address: map }
					stages:
					  progress:
					    job: { machine: .rook/blocks/say.yaml, input: { address: "${input.address}", text: "on ${input.said}" } }
				` + "\n" + output,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(dedent(text)), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			w.sync(dir)
			w.kept("bridges/telegram.yaml")
			var chat *client.Run
			w.until("both messages answered", func() bool {
				runs, err := w.List(context.Background(), client.Filter{Finished: true})
				if err != nil {
					t.Fatal(err)
				}
				done := 0
				for _, run := range runs {
					if strings.HasPrefix(run.Key, "telegram:") && strings.HasSuffix(run.Key, ":1") {
						chat = run
					}
					if run.Machine == "message" {
						if run.Status == "failed" {
							t.Fatalf("a message failed: %v", w.show(run.ID).Error)
						}
						if run.Status == "done" {
							done++
						}
					}
				}
				return done == 2
			})
			if run := w.show(chat.ID); run.Status != "running" {
				t.Fatalf("the chat ended %s", run.Status)
			}
			said := sayings(t, w)
			if len(said) != 2 || said["on one"]["answers"] != "1:10" || said["on two"]["answers"] != "1:11" {
				t.Fatalf("what was said is %v", said)
			}
		})
	}
}

// An agent's tool says a text at the message's address, and its model chooses only
// the text.
func TestAgentsToolSaysAtTheAddress(t *testing.T) {
	w := newWorld(t)
	w.secret("TEST_KEY", "sk-test")
	dir := copyStarter(t)
	replies := []llmstandin.Reply{
		{Calls: []llmstandin.ToolCall{{ID: "c1", Name: "progress", Args: `{"text": "on it"}`}}},
		{Text: ""},
	}
	turn := 0
	model := &llmstandin.Server{Answer: func(llmstandin.Call) llmstandin.Reply {
		turn++
		return replies[min(turn, len(replies))-1]
	}}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	helper, err := os.ReadFile(filepath.Join(dir, "agents/helper.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cut, _, _ := strings.Cut(string(helper), "open:")
	tool := dedent(`
		tools:
		  progress:
		    does: Say how the work is going.
		    args: { text: string }
		    job: { machine: .rook/blocks/say.yaml, input: { address: "${input.address}", text: "${args.text}" } }
	`)
	if err := os.WriteFile(filepath.Join(dir, "agents/helper.yaml"), []byte(cut+tool), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".rook/bridges/telegram/telegram.js"), []byte(dedent(oneMessage)), 0o644); err != nil {
		t.Fatal(err)
	}
	w.sync(dir)
	w.kept("bridges/telegram.yaml")
	w.until("the text said", func() bool { return sayings(t, w)["on it"] != nil })
	if to := sayings(t, w)["on it"]; to["bridge"] != "telegram" || to["id"] != "1" {
		t.Fatalf("the text went to %v", to)
	}
}

// A bridge gives its listener the project's directory, which every conversation
// reads as home.
func TestBridgeGivesItsListenerTheProject(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	w.sync(dir)
	w.kept("bridges/telegram.yaml")
	var given any
	w.until("the listener", func() bool {
		runs, err := w.List(context.Background(), client.Filter{Finished: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if run.Machine == "telegram" {
				given = w.show(run.ID).Input
				return true
			}
		}
		return false
	})
	if got := given.(map[string]any)["dir"]; got != dir {
		t.Fatalf("the listener was given %v", got)
	}
}
