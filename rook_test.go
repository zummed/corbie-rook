package rook_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	rook "github.com/zummed/corbie-rook"
	"github.com/zummed/corbie/client"
	"github.com/zummed/corbie/instance"
	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
	"go.yaml.in/yaml/v3"
)

// telegramScript stands in for telegram.js. It hears three messages from one
// person and one from a stranger, and then fails, which rests the listener.
const telegramScript = `
	if (input.do === "hear") {
	  if (input.cursor !== null) throw new Error("nothing more")
	  emit("message", { bridge: "telegram", text: "one", chat: "1", from: "1", place: "dm", answers: "1:10" })
	  emit("message", { bridge: "telegram", text: "two", chat: "1", from: "1", place: "dm", answers: "1:11" })
	  emit("message", { bridge: "telegram", text: "three", chat: "1", from: "1", place: "dm", answers: "1:12" })
	  emit("message", { bridge: "telegram", text: "hello", chat: "9", from: "9", place: "dm", answers: "9:13" })
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

// exampleProject makes a project as rook.Init does, and puts testdata's files in
// it: two people, the agents helper and team, and the part messages.
func exampleProject(dir string, bridges []string) error {
	if err := rook.Init(dir, bridges); err != nil {
		return err
	}
	return filepath.WalkDir("testdata", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		text, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		to := filepath.Join(dir, strings.TrimPrefix(name, "testdata/"))
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		return os.WriteFile(to, text, 0o644)
	})
}

// realAgent puts testdata's helper back in a project, with a stand-in model
// at an address.
func realAgent(t *testing.T, dir, address string) {
	t.Helper()
	for _, name := range []string{"agents/helper.yaml"} {
		text, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		written := strings.Replace(string(text), "model: { name: test-model, service: anthropic, secret: TEST_KEY }",
			"model: { name: test-model, service: anthropic, address: "+address+", secret: TEST_KEY }", 1)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(written), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// write writes files into a project, each dedented.
func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(dedent(text)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// copyStarter makes a project with Telegram, with its bridge's script and its
// helper standing in for what needs a network or a model.
func copyStarter(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project")
	if err := exampleProject(dir, []string{"telegram"}); err != nil {
		t.Fatal(err)
	}
	write(t, dir, map[string]string{
		".rook/bridges/telegram/telegram.js": telegramScript,
		"agents/helper.yaml":                 helperStub,
	})
	// The stand-in script uses no secret, and the test's controller holds none.
	for _, name := range []string{"listen", "say", "ask", "status", "close"} {
		full := filepath.Join(dir, ".rook/bridges/telegram", name+".yaml")
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

// heard stands in for telegram.js: its listener hears these once, then rests.
// One with a question is a tap, and any other a message. A question gives
// the message it posted, so a close has something to close.
func heard(events ...map[string]any) string {
	var emits []string
	for _, e := range events {
		data, _ := json.Marshal(e)
		name := "message"
		if _, ok := e["question"]; ok {
			name = "answer"
		}
		emits = append(emits, "emit(\""+name+"\", "+string(data)+")")
	}
	return `
		if (input.do === "hear") {
		  if (input.cursor !== null) throw new Error("nothing more")
		  ` + strings.Join(emits, "\n\t\t  ") + `
		  return { cursor: 1 }
		}
		if (input.do === "ask") return { sent: [{ chat: input.to, id: 101 }] }
		if (input.do === "send") return { sent: input.to + ":1" }
		if (input.do === "close" || input.do === "status") return {}
		return { sent: [input.answers || ""] }
	`
}

// from is a message from a person in their own chat with the bot, the n-th
// of that chat.
func from(id, text string, n int) map[string]any {
	return map[string]any{"bridge": "telegram", "text": text, "chat": id, "from": id, "place": "dm", "answers": fmt.Sprintf("%s:%d", id, n)}
}

// heardIn is a message from a person in a chat, which is the place given.
func heardIn(chat, from, place, text string) map[string]any {
	return map[string]any{"bridge": "telegram", "text": text, "chat": chat, "from": from, "place": place, "answers": chat + ":10"}
}

// removeListener removes the one machine the project keeps running.
func removeListener(t *testing.T, dir string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, "bridges/telegram.yaml")); err != nil {
		t.Fatal(err)
	}
}

// keyed gives the runs of a keyed location of the project by their key.
func keyed(w *world, path string) map[string][]*client.Run {
	out := map[string][]*client.Run{}
	for _, run := range w.at(path) {
		if run.Parent == "" {
			out[run.Key] = append(out[run.Key], run)
		}
	}
	return out
}

// questions gives the questions waiting on people: the runs of .rook/blocks/ask
// that wait for a tap.
func questions(w *world) []*client.Run {
	var out []*client.Run
	for _, run := range w.at(".rook/blocks/ask.yaml") {
		if run.Status == "running" && run.Timers == 1 {
			out = append(out, run)
		}
	}
	return out
}

// question waits for a question that is not among those seen, and gives it.
func (w *world) question(seen map[string]bool) *client.Run {
	w.t.Helper()
	var found *client.Run
	w.until("a question", func() bool {
		for _, run := range questions(w) {
			if !seen[run.ID] {
				found = run
			}
		}
		return found != nil
	})
	seen[found.ID] = true
	return found
}

// tap answers a question as a tap on Telegram by someone with an id.
func (w *world) tap(question, from, choice string) {
	w.t.Helper()
	w.send(question, "answer", map[string]any{"bridge": "telegram", "from": from, "choice": choice})
}

// The starter's machines: three messages from one person go to one chat, and
// to the conversation of that chat and their agent, which answers them one at
// a time and in order, each carrying on the one before. A stranger is told the
// bot is not theirs, and has no conversation.
func TestChatAnswersOneMessageAtATime(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	w.sync(dir)
	w.kept("bridges/telegram.yaml")
	w.until("two chats", func() bool {
		chats := keyed(w, ".rook/blocks/chat.yaml")
		return len(chats["telegram:1"]) == 1 && len(chats["telegram:9"]) == 1
	})
	var conversation *client.Run
	w.until("one conversation", func() bool {
		all := keyed(w, ".rook/blocks/conversation.yaml")
		if len(all["telegram:1:helper"]) == 1 {
			conversation = all["telegram:1:helper"][0]
		}
		return conversation != nil
	})
	answered := map[string]any{}
	w.until("three messages answered", func() bool {
		runs, err := w.List(context.Background(), client.Filter{Parent: conversation.ID, Finished: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, run := range runs {
			if run.Machine == "message" && run.Status == "done" {
				shown := w.show(run.ID)
				answered[shown.Input.(map[string]any)["message"].(map[string]any)["text"].(string)] = shown.Output.(map[string]any)["conversation"]
			}
		}
		return len(answered) == 3 && w.show(conversation.ID).Timers == 1
	})
	if want := map[string]any{"one": 1.0, "two": 2.0, "three": 3.0}; !reflect.DeepEqual(answered, want) {
		t.Fatalf("the conversations are %v", answered)
	}
	w.until("the stranger told", func() bool { return sayings(t, w)["This bot answers only the people it belongs to."] != nil })
	for key := range keyed(w, ".rook/blocks/conversation.yaml") {
		if key != "telegram:1:helper" {
			t.Errorf("a conversation %s", key)
		}
	}
	w.titled(map[string][]string{
		"telegram-bridge": {"Telegram bridge"},
		"telegram-listen": {"Telegram listener"},
		"chat":            {"dm: hello", "dm: one"},
		"route":           {"hello", "one", "three", "two"},
		"conversation":    {"helper in dm: one"},
		"message":         {"one", "three", "two"},
	})
}

// A message goes to the agent who chose: two people in one chat with two
// agents have two conversations, and one agent is never given the other's
// conversation. Each agent here is a machine with no model.
func TestTwoAgentsInOneChatAreTwoConversations(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	stub := `
		flow: NAME
		input: { said: string, asker: string, conversation: "any?" }
		stages:
		  turn: { job: { script: ./turn.js, input: { said: "${input.said}", conversation: "${input.conversation}" } } }
		output: ${stages.turn.output}
	`
	write(t, dir, map[string]string{
		"agents/helper.yaml": strings.ReplaceAll(stub, "NAME", "helper"),
		"agents/team.yaml":   strings.ReplaceAll(stub, "NAME", "team"),
		// Each agent's conversation is the list of what it was said.
		"agents/turn.js": `return { reply: "", conversation: [...(input.conversation || []), input.said] }`,
		".rook/bridges/telegram/telegram.js": `
			if (input.do === "hear") {
			  if (input.cursor !== null) throw new Error("nothing more")
			  emit("message", { bridge: "telegram", text: "alice one", chat: "5", from: "1", place: "dm", answers: "5:1" })
			  emit("message", { bridge: "telegram", text: "bob one", chat: "5", from: "2", place: "dm", answers: "5:2" })
			  emit("message", { bridge: "telegram", text: "alice two", chat: "5", from: "1", place: "dm", answers: "5:3" })
			  emit("message", { bridge: "telegram", text: "bob two", chat: "5", from: "2", place: "dm", answers: "5:4" })
			  return { cursor: 1 }
			}
			return { sent: [input.answers || ""] }
		`,
	})
	w.sync(dir)
	given := map[string]any{}
	w.until("four answers", func() bool {
		for _, at := range []string{"agents/helper.yaml", "agents/team.yaml"} {
			for _, run := range w.at(at) {
				if run.Status == "done" {
					shown := w.show(run.ID)
					given[shown.Input.(map[string]any)["said"].(string)] = shown.Input.(map[string]any)["conversation"]
				}
			}
		}
		return len(given) == 4
	})
	want := map[string]any{"alice one": nil, "bob one": nil, "alice two": []any{"alice one"}, "bob two": []any{"bob one"}}
	if !reflect.DeepEqual(given, want) {
		t.Fatalf("the agents were given %v", given)
	}
	keys := slices.Sorted(maps.Keys(keyed(w, ".rook/blocks/conversation.yaml")))
	if !reflect.DeepEqual(keys, []string{"telegram:5:helper", "telegram:5:team"}) {
		t.Fatalf("the conversations are %v", keys)
	}
}

// An agent for anyone answers a stranger, who is named by their bridge and id.
func TestStrangerGetsAnAgentForAnyone(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
		"rules.yaml": `
			places: {}
			agents:
			  - { name: helper, for: [anyone] }
		`,
		"agents/echo.js": "return input",
		"agents/helper.yaml": `
			flow: helper
			input: { said: string, asker: string, request: string }
			stages:
			  echo: { job: { script: ./echo.js, input: "${input}" } }
			output: { reply: "${stages.echo.output.asker} asked ${stages.echo.output.request}" }
		`,
	})
	w.sync(dir)
	w.until("the stranger answered", func() bool { return sayings(t, w)["telegram user 9 asked telegram user 9: hello"] != nil })
}

// Each agent rules.yaml lists has its file in agents/: with none, a message to
// it would fail.
func TestEveryAgentHasAFile(t *testing.T) {
	people, err := os.ReadFile("testdata/rules.yaml")
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
		if _, err := os.Stat(filepath.Join("testdata/agents", agent.Name+".yaml")); err != nil {
			t.Errorf("the agent %s has no file: %v", agent.Name, err)
		}
	}
	if len(table.Agents) == 0 {
		t.Error("rules.yaml lists no agent")
	}
}

// An answer that fails is told so and starts the conversation again, so one the
// agent cannot take does not fail every later message of the chat.
func TestChatStartsAfreshAfterAFailedAnswer(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
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
	})
	w.sync(dir)
	w.kept("bridges/telegram.yaml")
	got := map[string]any{}
	w.until("four messages answered", func() bool {
		for _, run := range w.at(".rook/blocks/conversation.yaml") {
			if run.Machine == "message" && run.Status == "done" {
				shown := w.show(run.ID)
				got[shown.Input.(map[string]any)["message"].(map[string]any)["text"].(string)] = shown.Output.(map[string]any)["conversation"]
			}
		}
		return len(got) == 4
	})
	if want := map[string]any{"one": 1.0, "two": nil, "three": 1.0, "four": 2.0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the conversations are %v", got)
	}
	if said := sayings(t, w); said["That could not be answered."] == nil {
		t.Errorf("the failure was not told: %v", said)
	}
}

// A tool marked approve: true waits for its part's approvers, and a no comes back
// to the model with nothing done. The tree's own client starts the agent at its
// location with no message, and the approver is shown the request.
func TestHeldCallWaitsForItsApprovers(t *testing.T) {
	w := newWorld(t)
	w.secret("TEST_KEY", "sk-test")
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
	w.sync(dir)
	id := w.start("agents/helper.yaml", map[string]any{"said": "q", "asker": "alice", "request": "alice: before\nalice: q", "home": dir})
	question := w.question(map[string]bool{})
	if text := fmt.Sprint(w.show(question.ID).Input); !strings.Contains(text, "Request, the latest message last:\n> alice: before\n> alice: q") {
		t.Errorf("the approver was shown %s", text)
	}
	w.tap(question.ID, "1", "no")
	w.until("the helper to end", func() bool { return w.show(id).HasEnded() })
	if run := w.show(id); run.Status != "done" {
		t.Fatalf("the helper ended %s: %v", run.Status, run.Error)
	}
	said := model.Calls[2].Messages
	text, _ := json.Marshal(said[len(said)-1])
	if !strings.Contains(string(text), "alice said no.") {
		t.Errorf("the held call came back as %s", text)
	}
	if runs := stubs(t, w, "tell"); len(runs) != 0 {
		t.Errorf("the message was sent after a no: %v", runs)
	}
	w.titled(map[string][]string{"helper": {"alice: q"}, "approval": {"Approve tell: alice: q"}, "ask": {"An agent wants to run tell."}})
}

// workPeople is a team, platform, for the rules rook-design shows: a public
// channel, a channel of the team's own, and an agent for anyone.
const workPeople = `
	people:
	  sam: { slack: U1 }
	  ana: { slack: U2 }
	  cara: { slack: U3 }
	groups:
	  platform: [sam, ana]
`

// pageRules is the rules.yaml rook-design shows.
func pageRules(t *testing.T) string {
	for _, ex := range examples(t, "rook-design") {
		if ex.path == "rules.yaml" {
			return ex.text
		}
	}
	t.Fatal("rook-design shows no rules.yaml")
	return ""
}

// try writes the flow on/try.yaml into a project with stages, and gives its
// output once it has run, failing the test if it failed.
func try(t *testing.T, w *world, dir, stages string, output string) any {
	t.Helper()
	write(t, dir, map[string]string{"on/try.yaml": "flow: try\nallow: [send:lookup, send:.rook/blocks/ask]\nstages:\n" + stages + "output: " + output + "\n"})
	w.sync(dir)
	id := w.start("on/try.yaml", nil)
	run, err := w.Wait(context.Background(), id, interval)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "done" {
		t.Fatalf("the flow ended %s: %v", run.Status, run.Error)
	}
	return w.show(id).Output
}

// who gives the first agent the asker and everyone who reads the answer may each
// use, and no agent when there is none, with the entry of rules.yaml that
// chose it. An empty table is the starter's own.
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
			{"gitlab", "someone", "group/app", "reviewer"},
			{"slack", "U1", "group/app", "reviewer"},
			{"gitlab", "someone", "group/other", "support"},
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
			// The rules are the example rook-design shows.
			write(t, dir, map[string]string{"people.yaml": table, "rules.yaml": pageRules(t)})
		}
		var stages strings.Builder
		for i, r := range rows {
			fmt.Fprintf(&stages, "  r%d: { job: { machine: .rook/blocks/who.yaml, input: { bridge: %s, id: %q, place: %q } } }\n", i, r.bridge, r.id, r.place)
		}
		out := try(t, w, dir, stages.String(), `"${stages}"`).(map[string]any)
		for i, r := range rows {
			got := out[fmt.Sprintf("r%d", i)].(map[string]any)["output"].(map[string]any)
			agent, _ := got["agent"].(string)
			if agent != r.agent {
				t.Errorf("%s, %s in %s: the agent %q, and not %q", table, r.id, r.place, agent, r.agent)
			}
			if rule, _ := got["rule"].(map[string]any); (agent == "") != (rule == nil) || rule != nil && rule["name"] != agent {
				t.Errorf("%s, %s in %s: the rule %v", table, r.id, r.place, got["rule"])
			}
		}
	}
}

// review stands in for review.yaml: it says what the held job's input names.
const review = `
	statechart: review
	input: { context: string }
	initial: e
	states:
	  e: { end: done, output: { reply: { says: "${input.context.match(/yes|refuse|refer/)[0]}", note: why } } }
`

// A part's approval with review: a refusal ends it, the review's yes ends it for
// a tool in alone, and the part's people decide everything else.
func TestApprovalGoesToTheReviewThenAPerson(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	write(t, dir, map[string]string{"review.yaml": review})
	write(t, dir, map[string]string{"on/try.yaml": `
		flow: try
		stages:
		  alone_yes: { job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, people: [alice], review: true, alone: [mend], jobs: [{ name: mend, input: { r: yes } }] } } }
		  alone_no: { job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, people: [alice], review: true, alone: [mend], jobs: [{ name: mend, input: { r: refuse } }] } } }
		  held_no: { job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, people: [alice], review: true, jobs: [{ name: mend, input: { r: refuse } }] } } }
		  alone_unsure:
		    job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, people: [alice], review: true, alone: [mend], jobs: [{ name: mend, input: { r: refer } }] } }
		  held_yes:
		    follows: [alone_unsure]
		    job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, people: [alice], review: true, jobs: [{ name: mend, input: { r: yes } }] } }
		  other_yes:
		    follows: [held_yes]
		    job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, people: [alice], review: true, alone: [mend], jobs: [{ name: mend, input: { r: yes } }, { name: burn, input: {} }] } }
		  again_yes:
		    job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, people: [alice], review: true, alone: [mend], jobs: [{ name: "mend#2", input: { r: yes } }, { name: mend.1, input: { r: yes } }] } }
		output:
		  alone_yes: "${stages.alone_yes.output.approve}"
		  alone_no: "${stages.alone_no.output.approve}"
		  held_no: "${stages.held_no.output.approve}"
		  alone_unsure: "${stages.alone_unsure.output.approve}"
		  held_yes: "${stages.held_yes.output.approve}"
		  other_yes: "${stages.other_yes.output.approve}"
		  again_yes: "${stages.again_yes.output.approve}"
	`})
	w.sync(dir)
	id := w.start("on/try.yaml", nil)
	seen := map[string]bool{}
	for _, choice := range []string{"yes", "no", "yes"} {
		question := w.question(seen)
		if text := fmt.Sprint(w.show(question.ID).Input); !strings.Contains(text, "Review: ") {
			t.Errorf("the person was not shown the review: %s", text)
		}
		w.tap(question.ID, "1", choice)
	}
	w.until("the flow to end", func() bool { return w.show(id).HasEnded() })
	want := map[string]any{"alone_yes": []any{"mend"}, "alone_no": []any{}, "held_no": []any{}, "alone_unsure": []any{"mend"}, "held_yes": []any{}, "other_yes": []any{"mend", "burn"}, "again_yes": []any{"mend#2", "mend.1"}}
	if got := w.show(id).Output; !reflect.DeepEqual(got, want) {
		t.Fatalf("the approvals were %v: %s", got, w.show(id).Error)
	}
}

// A part that asks for a review in a project with no review.yaml asks its
// people, shown that the review failed, and a review's yes with no review
// approves nothing alone.
func TestApprovalWithNoReviewAsksThePeople(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	write(t, dir, map[string]string{"on/try.yaml": `
		flow: try
		stages:
		  held: { job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, people: [alice], review: true, alone: [tell], jobs: [{ name: tell, input: {} }] } } }
		output: ${stages.held.output}
	`})
	w.sync(dir)
	id := w.start("on/try.yaml", nil)
	question := w.question(map[string]bool{})
	if text := fmt.Sprint(w.show(question.ID).Input); !strings.Contains(text, "Review failed: ") {
		t.Errorf("the person was shown %s", text)
	}
	w.tap(question.ID, "1", "yes")
	w.until("the approval", func() bool { return w.show(id).HasEnded() })
	if got := fmt.Sprint(w.show(id).Output); got != "map[approve:[tell] note:alice said yes.]" {
		t.Fatalf("it gave %s", got)
	}
}

// A held call goes to the part's own people: only their answer counts, and
// nobody else is asked. A part that names nobody approves nothing, and says so.
func TestApprovalAsksThePartsApprovers(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	write(t, dir, map[string]string{"on/try.yaml": `
		flow: try
		stages:
		  held: { job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: messages, people: [bob], jobs: [{ name: tell, input: {} }] } } }
		  nobody: { job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: deploys, people: [], jobs: [{ name: tell, input: {} }] } } }
		  unnamed: { job: { machine: .rook/blocks/approval.yaml, input: { asking: {}, part: deploys, jobs: [{ name: tell, input: {} }] } } }
		output: { held: "${stages.held.output.approve}", nobody: "${stages.nobody.output}", unnamed: "${stages.unnamed.output}" }
	`})
	w.sync(dir)
	try := w.start("on/try.yaml", nil)
	question := w.question(map[string]bool{})
	if got := w.show(question.ID).Input.(map[string]any)["people"]; !reflect.DeepEqual(got, []any{"bob"}) {
		t.Fatalf("it asked %v", got)
	}
	for _, from := range []string{"1", "2"} {
		w.tap(question.ID, from, "yes")
		time.Sleep(10 * interval)
		if ended := w.show(question.ID).Status != "running"; ended != (from == "2") {
			t.Fatalf("after an answer from %s the question is %s", from, w.show(question.ID).Status)
		}
	}
	w.until("the approval", func() bool { return w.show(try).HasEnded() })
	if got := fmt.Sprint(w.show(try).Output); got != "map[held:[tell] nobody:map[approve:[] note:The part deploys names no one in people to approve its calls.] unnamed:map[approve:[] note:The part deploys names no one in people to approve its calls.]]" {
		t.Fatalf("it gave %s", got)
	}
	if n := len(keyed(w, ".rook/blocks/ask.yaml")[""]); n != 1 {
		t.Errorf("%d questions were asked", n)
	}
}

// An id counts only on its own bridge: someone with another person's id on another
// service is not that person.
func TestAnIdCountsOnlyOnItsBridge(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	write(t, dir, map[string]string{"people.yaml": "people:\n  alice: { telegram: \"1\", slack: U1 }\ngroups: {}\n"})
	got := try(t, w, dir, `
  same: { job: { machine: .rook/blocks/who.yaml, input: { bridge: slack, id: U1, place: dm } } }
  other: { job: { machine: .rook/blocks/who.yaml, input: { bridge: slack, id: "1", place: dm } } }
  none: { job: { machine: .rook/blocks/who.yaml, input: { bridge: gitlab, id: "undefined", place: dm } } }
`, `{ same: "${stages.same.output.person}", other: "${stages.other.output.person}", none: "${stages.none.output.person}" }`)
	if fmt.Sprint(got) != "map[none:<nil> other:<nil> same:alice]" {
		t.Fatalf("the people were %v", got)
	}
}

// A question is a run of .rook/blocks/ask that a machine calls, and a tap by a
// person it names answers it, on any of their bridges. A tap by anyone else
// changes nothing.
func TestAskPersonIsAnsweredByATap(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	// The test's controller holds no secret, and this question is asked of no one.
	write(t, dir, map[string]string{
		".rook/bridges/slack/ask.yaml": "flow: slack-ask\ninput: { to: string, text: string, choices: list, question: string }\nstages:\n  question: { job: { script: ./asked.js } }\n",
		".rook/bridges/slack/asked.js": "return {}",
		"on/asker.yaml": `
			flow: asker
			allow: [send:.rook/blocks/ask]
			stages:
			  asking: { job: { call: { at: .rook/blocks/ask }, input: { people: [bob], text: "Well?", choices: ["yes", "no"] } } }
			output: ${stages.asking.output.choice}
		`,
		"people.yaml": "people:\n  alice: { telegram: \"1\" }\n  bob: { telegram: \"2\", slack: \"5\" }\ngroups: {}\n",
	})
	removeListener(t, dir)
	w.sync(dir)
	asker := w.start("on/asker.yaml", nil)
	question := w.question(map[string]bool{})
	for _, from := range []string{"3", "1"} {
		w.tap(question.ID, from, "yes")
		time.Sleep(10 * interval)
		if run := w.show(asker); run.Status != "running" {
			t.Fatalf("a tap by %s ended the asker: %+v", from, run)
		}
	}
	w.send(question.ID, "answer", map[string]any{"bridge": "slack", "from": "5", "choice": "no"})
	w.until("the asker done", func() bool { return w.show(asker).Status == "done" })
	if got := w.show(asker).Output; got != "no" {
		t.Fatalf("the choice was %v", got)
	}
	if run := w.show(question.ID); run.StartedBy == "" {
		t.Errorf("the question does not name the run that called it: %+v", run)
	}
	w.titled(map[string][]string{"ask": {"Well?"}})
}

// Every machine of a project with every bridge reads, only the bridges' own
// files start when it is submitted, and chat and conversation are keyed.
func TestProjectReads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := rook.Init(dir, rook.Bridges()); err != nil {
		t.Fatal(err)
	}
	tree, err := client.ReadTree(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	lifetimes := map[string][]string{}
	for _, m := range tree.Machines {
		if m.Problem != nil {
			t.Errorf("%s: %v", m.Path, m.Problem)
			continue
		}
		life := m.Draft.Definition.Lifetime
		lifetimes[life] = append(lifetimes[life], m.Location)
	}
	want := map[string][]string{
		"singleton": {"/project/bridges/gitlab", "/project/bridges/jira", "/project/bridges/slack", "/project/bridges/telegram"},
		"keyed":     {"/project/.rook/blocks/chat", "/project/.rook/blocks/conversation"},
	}
	for life, locations := range want {
		slices.Sort(lifetimes[life])
		if !reflect.DeepEqual(lifetimes[life], locations) {
			t.Errorf("%s: %v", life, lifetimes[life])
		}
	}
	for _, piece := range []string{".rook/bridges/telegram/listen.yaml", ".rook/agents/base.yaml", ".rook/attend/agent.yaml", ".rook/review/agent.yaml", "rules.yaml", "people.yaml"} {
		if slices.ContainsFunc(tree.Machines, func(m *client.Machine) bool { return m.Path == piece }) {
			t.Errorf("%s has a location", piece)
		}
	}
}

// No file Rook writes into a project names a model, a model's service or a
// model's key: a project with no model works, and one with a model names it.
func TestNoModelInWhatRookWrites(t *testing.T) {
	for _, top := range []string{"shipped", "starter", "bridges"} {
		filepath.WalkDir(top, func(name string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			text, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			for n, line := range strings.Split(string(text), "\n") {
				code := !strings.HasPrefix(strings.TrimSpace(line), "#") && !strings.HasPrefix(strings.TrimSpace(line), "//")
				for _, word := range []string{"claude-code", "CLAUDE_TOKEN", "ANTHROPIC", "sonnet", "haiku", "opus"} {
					if strings.Contains(line, word) {
						t.Errorf("%s:%d names %s", name, n+1, word)
					}
				}
				for _, field := range []string{"model:", "service:"} {
					if code && strings.Contains(line, field) {
						t.Errorf("%s:%d writes %s", name, n+1, field)
					}
				}
			}
			return nil
		})
	}
}

// A project has the machines of the bridges it runs and of no other, so one it
// does not run costs it nothing: those named, and each whose file is in
// bridges/, added by hand or not. It reads with only those, and
// .rook/people/running.yaml lists the same bridges.
func TestBridgesOnlyRunWhenChosen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	there := func(want map[string]bool) {
		t.Helper()
		for name, want := range want {
			if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != want {
				t.Errorf("%s there: %v, and not %v", name, err == nil, want)
			}
		}
	}
	listed := func(want ...string) {
		t.Helper()
		text, err := os.ReadFile(filepath.Join(dir, ".rook/people/running.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var got struct{ Running []string }
		if err := yaml.Unmarshal(text, &got); err != nil {
			t.Fatal(err)
		}
		var machines []string
		entries, _ := os.ReadDir(filepath.Join(dir, ".rook/bridges"))
		for _, e := range entries {
			machines = append(machines, e.Name())
		}
		if !reflect.DeepEqual(got.Running, want) || !reflect.DeepEqual(machines, want) {
			t.Errorf("running.yaml lists %v and .rook/bridges holds %v, and not %v", got.Running, machines, want)
		}
	}
	if err := rook.Init(dir, []string{"slack", "gitlab"}); err != nil {
		t.Fatal(err)
	}
	there(map[string]bool{"bridges/slack.yaml": true, "bridges/gitlab.yaml": true, "bridges/telegram.yaml": false, "bridges/jira.yaml": false,
		".rook/bridges/slack/say.yaml": true, ".rook/bridges/gitlab/listen.yaml": true, ".rook/bridges/telegram": false, ".rook/bridges/jira": false})
	listed("gitlab", "slack")
	tree, err := client.ReadTree(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range tree.Machines {
		if m.Problem != nil {
			t.Errorf("%s: %v", m.Path, m.Problem)
		}
	}
	text, err := os.ReadFile("bridges/telegram.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bridges/telegram.yaml"), text, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "bridges/gitlab.yaml")); err != nil {
		t.Fatal(err)
	}
	if err := rook.Init(dir, nil); err != nil {
		t.Fatal(err)
	}
	there(map[string]bool{".rook/bridges/telegram/say.yaml": true, ".rook/bridges/slack/say.yaml": true, ".rook/bridges/gitlab": false})
	listed("slack", "telegram")
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
	for _, name := range []string{"lookup.yaml", "AGENTS.md"} {
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
	for _, name := range []string{"lookup.yaml", "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s, which init writes when it is not there, was not written", name)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".rook/AGENTS.md")); !strings.HasPrefix(string(got), "Rook: ") {
		t.Errorf(".rook/AGENTS.md is %q, and not the page rook", got)
	}
	if differ, err := rook.Differ(dir); err != nil || !reflect.DeepEqual(differ, []string{"bridges/slack.yaml", "people.yaml"}) {
		t.Errorf("the files not as Rook starts them are %v, %v", differ, err)
	}
	if text := rook.Pages()[3].Text; !strings.Contains(text, "\nrules.yaml\n----------\n\n    # ") || !strings.Contains(text, "\nbridges/jira.yaml\n") {
		t.Error("the page rook-starter lacks a starting file")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".rook/blocks/tell.yaml")); string(got) == "changed" {
		t.Error(".rook was not rewritten")
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("a file Rook no longer ships stayed in .rook")
	}
	// A starting file no machine reads, such as one for a model's tools, stays
	// removed.
	removed := []string{"people.yaml", "CLAUDE.md", ".mcp.json"}
	for _, name := range removed {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := rook.Init(dir, []string{"slack"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range removed {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s, a starting file the person removed, came back", name)
		}
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

// approving makes a project whose bridge hears what the script says, and whose
// helper asks a stand-in model, which opens the part messages and tells bob
// hello, and then says told.
func approving(t *testing.T, w *world, script string) (string, *llmstandin.Server) {
	t.Helper()
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
	write(t, dir, map[string]string{".rook/bridges/telegram/telegram.js": script})
	return dir, model
}

// A message that names a held tool waits for its approver through every level
// from the bridge down, within the depth every machine has by default, and a yes
// tells the person it names.
func TestApprovalFromAChatGetsToTheApprover(t *testing.T) {
	w := newWorld(t)
	dir, _ := approving(t, w, oneMessage)
	w.sync(dir)
	w.kept("bridges/telegram.yaml")
	w.tap(w.question(map[string]bool{}).ID, "1", "yes")
	w.until("the reply said", func() bool { return sayings(t, w)["told"]["to"] != nil })
	said := sayings(t, w)
	if to := said["hello"]; to == nil || to["to"] != "2" {
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
			write(t, dir, map[string]string{
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
			})
			w.sync(dir)
			w.kept("bridges/telegram.yaml")
			w.until("both messages answered", func() bool {
				done := 0
				for _, run := range w.at(".rook/blocks/conversation.yaml") {
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
			for key, runs := range keyed(w, ".rook/blocks/chat.yaml") {
				if run := w.show(runs[0].ID); run.Status != "running" {
					t.Fatalf("the chat %s ended %s", key, run.Status)
				}
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
	write(t, dir, map[string]string{".rook/bridges/telegram/telegram.js": oneMessage})
	w.sync(dir)
	w.kept("bridges/telegram.yaml")
	w.until("the text said", func() bool { return sayings(t, w)["on it"] != nil })
	if to := sayings(t, w)["on it"]; to["bridge"] != "telegram" || to["id"] != "1" {
		t.Fatalf("the text went to %v", to)
	}
}

// A conversation gives the agent the project's directory as home.
func TestAgentIsGivenTheProject(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{".rook/bridges/telegram/telegram.js": oneMessage})
	w.sync(dir)
	var given any
	w.until("the agent", func() bool {
		for _, run := range w.at("agents/helper.yaml") {
			given = w.show(run.ID).Input
			return true
		}
		return false
	})
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := given.(map[string]any)["home"]; got != real {
		t.Fatalf("the agent was given %v", got)
	}
}

// undelivered gives what a run's record notes of the events it sent that were
// not delivered.
func undelivered(w *world, id string) []string {
	var out []string
	for _, ask := range w.asked(id, instance.AskNote) {
		if strings.Contains(ask.Text, "was not delivered") {
			out = append(out, ask.Text)
		}
	}
	return out
}
