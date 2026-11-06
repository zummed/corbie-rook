package rook_test

// A message followed from its chat to the conversation, the agent, each
// tool's run and the question an approval asks, as corbie run show --record
// and the client's API read them; and a project changed while a conversation
// waits on an approval.

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/zummed/corbie/client"
	"github.com/zummed/corbie/instance"
	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// toolModel is a stand-in model that, after a person's message, calls each of
// these tools in turn, one a turn, and then says text. Which tool comes next is
// read from the last message's tool result, so a conversation's next message
// starts again from the first.
func toolModel(t *testing.T, text string, calls ...llmstandin.ToolCall) (*llmstandin.Server, string) {
	for i := range calls {
		calls[i].ID = fmt.Sprintf("s%d", i)
	}
	model := &llmstandin.Server{Answer: func(c llmstandin.Call) llmstandin.Reply {
		last := fmt.Sprint(c.Messages[len(c.Messages)-1])
		for i := range calls {
			if strings.Contains(last, "tool_use_id:s"+fmt.Sprint(i)+" ") || strings.Contains(last, "tool_use_id:s"+fmt.Sprint(i)+"]") {
				if i+1 < len(calls) {
					return llmstandin.Reply{Calls: []llmstandin.ToolCall{calls[i+1]}}
				}
				return llmstandin.Reply{Text: text}
			}
		}
		return llmstandin.Reply{Calls: []llmstandin.ToolCall{calls[0]}}
	}}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	return model, web.URL
}

// traceHelper is an agent with a model at an address, which opens the part
// messages: the tool clock, a script with no approval, and tell, held for
// alice.
func traceHelper(address, system string) map[string]string {
	return map[string]string{
		"agents/helper.yaml": `
			agent: helper
			include: .rook/agents/base.yaml
			model: { name: test-model, service: anthropic, address: "` + address + `", secret: TEST_KEY }
			system: |
			  ` + system + `

			  ${input.format}
			open:
			  messages: { include: parts/messages/part.yaml }
		`,
		"parts/messages/part.yaml": `
			does: Messages to the people this bot knows, and the time.
			ask: { machine: .rook/blocks/approval.yaml, input: { part: messages, people: [alice], request: "${input.request}" } }
			tools:
			  clock:
			    does: Give the time in a zone.
			    args: { zone: string }
			    job: { script: ./clock.js, input: { zone: "${args.zone}" } }
			  tell:
			    does: Say a text to a person, by their name in people.yaml.
			    args: { person: string, text: string }
			    job: { machine: .rook/blocks/tell.yaml, input: { person: "${args.person}", text: "${args.text}" }, approve: true }
		`,
		"parts/messages/clock.js": `return { zone: input.zone, time: "12:00" }`,
	}
}

// openClockTell are the tool calls of a model that opens messages, reads the
// clock and tells bob.
func openClockTell() []llmstandin.ToolCall {
	return []llmstandin.ToolCall{
		{Name: "open", Args: `{"part": "messages"}`},
		{Name: "clock", Args: `{"zone": "UTC"}`},
		{Name: "tell", Args: `{"person": "bob", "text": "the build is green"}`},
	}
}

// traceProject is the starter with Telegram's stand-in, hearing these
// messages, and the helper above asking a stand-in model.
func traceProject(t *testing.T, w *world, messages ...map[string]any) (string, *llmstandin.Server) {
	t.Helper()
	w.secret("TEST_KEY", "sk-test")
	dir := copyStarter(t)
	model, address := toolModel(t, "done: told bob", openClockTell()...)
	files := traceHelper(address, "You help alice.")
	files[".rook/bridges/telegram/telegram.js"] = heard(messages...)
	write(t, dir, files)
	return dir, model
}

// sentFrom gives the runs a run's own record names for what it sent or called
// to a location, children's entries left out.
func (w *world) sentFrom(id, kind, at string) []string {
	var out []string
	for _, e := range w.record(id) {
		if e.ID != id {
			continue
		}
		for _, ask := range e.Asks {
			if ask.Kind == kind && ask.Location == at {
				out = append(out, cmp.Or(ask.Run, ask.To))
			}
		}
	}
	return out
}

// own gives the entries of a run's own record.
func (w *world) own(id string) []client.Entry {
	var out []client.Entry
	for _, e := range w.record(id) {
		if e.ID == id {
			out = append(out, e)
		}
	}
	return out
}

// arrived gives the data of each event of a name from a job in a run's own
// record.
func (w *world) arrived(id, name, job string) []any {
	var out []any
	for _, e := range w.own(id) {
		for _, ev := range e.Events {
			if ev.Name == name && ev.From == job {
				out = append(out, ev.Data)
			}
		}
	}
	return out
}

// ownAsks gives what a run's own steps asked for, of one kind.
func (w *world) ownAsks(id, kind string) []instance.Ask {
	var out []instance.Ask
	for _, e := range w.own(id) {
		for _, a := range e.Asks {
			if a.Kind == kind {
				out = append(out, a)
			}
		}
	}
	return out
}

// running gives the running runs of a machine.
func running(t *testing.T, w *world, machine string) []*client.Run {
	var out []*client.Run
	for _, run := range stubs(t, w, machine) {
		if run.Status == "running" {
			out = append(out, run)
		}
	}
	return out
}

// lastOf fails the test with no run, and gives the last.
func lastOf(t *testing.T, what string, runs []string) string {
	t.Helper()
	if len(runs) == 0 {
		t.Fatalf("no %s", what)
	}
	return runs[len(runs)-1]
}

// A project with no model: the agent is a flow. It says something at the
// message's address, calls a question for alice, and replies with her answer.
// The submit names no secret that is missing, and the trail goes from the
// conversation to the flow and from the flow to the question.
func TestNoModelFlowAgentIsFollowed(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
		".rook/bridges/telegram/telegram.js": heard(from("2", "deploy now", 12)),
		"rules.yaml": `
			places:
			  dm: { readers: [asker] }
			agents:
			  - { name: desk, for: [bob] }
		`,
		"agents/desk.yaml": `
			flow: desk
			input: { said: string, asker: string, address: map }
			allow: [send:.rook/blocks/ask]
			stages:
			  ack:
			    job: { machine: .rook/blocks/say.yaml, input: { address: "${input.address}", text: "asking alice about ${input.said}" } }
			  question:
			    job:
			      call: { at: .rook/blocks/ask }
			      input: { people: [alice], text: "bob asks: ${input.said}. Allow?", choices: ["yes", "no"] }
			output: { reply: "alice said ${stages.question.output.choice}" }
		`,
	})
	for _, name := range []string{"agents/helper.yaml", "agents/team.yaml", "parts/messages/part.yaml"} {
		os.Remove(filepath.Join(dir, name))
	}
	receipt := w.syncAt(w.Client, dir, project)
	if !receipt.Taken {
		t.Fatalf("refused: %s", receipt.Refused)
	}
	// The harness's stand-in leaves TELEGRAM_TOKEN in the bridge's send and
	// attachments; that is the bridge's own secret, and no model's.
	for _, missing := range receipt.Tree.Missing {
		if !strings.Contains(missing, "TELEGRAM_TOKEN") {
			t.Errorf("a project with no model is missing %v", missing)
		}
	}
	question := w.question(map[string]bool{})
	w.tap(question.ID, "1", "yes")
	w.until("the reply said", func() bool { return sayings(t, w)["alice said yes"] != nil })
	if to := sayings(t, w)["asking alice about deploy now"]; to["id"] != "2" {
		t.Errorf("the flow said at %v", to)
	}
	desk := stubs(t, w, "desk")
	if len(desk) != 1 {
		t.Fatalf("the desk ran %d times", len(desk))
	}
	if asked := lastOf(t, "question", w.sentFrom(desk[0].ID, instance.AskCall, location(".rook/blocks/ask"))); asked != question.ID || w.show(asked).StartedBy != desk[0].ID {
		t.Errorf("the desk called %s, the question is %s", asked, question.ID)
	}
	if w.show(w.show(desk[0].ID).StartedBy).Machine != "message" {
		t.Errorf("the desk was not started by a conversation's message")
	}
}

// A changed agent's file, submitted while its run waits on an approval, leaves
// the waiting run, its approval and its question running; a yes finishes it,
// and the conversation's next message is answered by the new agent.
func TestChangedAgentWhileApprovalWaits(t *testing.T) {
	w := newWorld(t)
	dir, model := traceProject(t, w, from("1", "tell bob", 10))
	w.sync(dir)
	question := w.question(map[string]bool{})
	agent := stubs(t, w, "helper")[0].ID
	address := strings.TrimSuffix(strings.SplitN(strings.Split(readFile(t, dir, "agents/helper.yaml"), "address: \"")[1], "\"", 2)[0], "/")
	write(t, dir, map[string]string{"agents/helper.yaml": traceHelper(address, "You help alice, version two.")["agents/helper.yaml"]})
	receipt := w.syncAt(w.Client, dir, project)
	if len(receipt.Tree.Cancelled) > 0 {
		t.Errorf("the submit cancelled %v", receipt.Tree.Cancelled)
	}
	for _, id := range []string{agent, question.ID} {
		if run := w.show(id); run.Status != "running" {
			t.Errorf("%s %s is %s", run.Machine, id, run.Status)
		}
	}
	w.tap(question.ID, "1", "yes")
	w.until("the reply said", func() bool { return sayings(t, w)["done: told bob"] != nil })
	if run := w.show(agent); run.Status != "done" {
		t.Errorf("the agent ended %s", run.Status)
	}
	w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", from("1", "again", 11))
	w.until("a second question", func() bool { return len(questions(w)) == 1 })
	if system := model.Calls[len(model.Calls)-1].System; !strings.Contains(system, "version two") {
		t.Errorf("the next message's agent was told %q", system)
	}
}

func readFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A changed rules.yaml, submitted while a conversation waits on an approval,
// cancels the chat, as rook-project says, and leaves the conversation, the
// agent and the question running. A yes finishes the answer at the message's
// address, and the thread's next message starts a new chat whose record shows
// the new rule.
func TestChangedRulesWhileApprovalWaits(t *testing.T) {
	w := newWorld(t)
	dir, _ := traceProject(t, w, from("1", "tell bob", 10))
	w.sync(dir)
	question := w.question(map[string]bool{})
	chat := keyed(w, ".rook/blocks/chat.yaml")["telegram:1"][0].ID
	conversation := keyed(w, ".rook/blocks/conversation.yaml")["telegram:1:helper"][0].ID
	write(t, dir, map[string]string{"rules.yaml": `
		places:
		  dm: { readers: [asker] }
		agents:
		  - { name: helper, for: [team] }
	`})
	w.sync(dir)
	if run := w.show(chat); run.Status != "cancelled" {
		t.Errorf("the chat is %s", run.Status)
	}
	if run := w.show(conversation); run.Status != "running" {
		t.Errorf("the conversation is %s", run.Status)
	}
	w.tap(question.ID, "1", "yes")
	w.until("the reply said", func() bool { return sayings(t, w)["done: told bob"] != nil })
	if to := sayings(t, w)["done: told bob"]; to["id"] != "1" || to["answers"] != "1:10" {
		t.Errorf("the reply went to %v", to)
	}
	w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", from("1", "again", 11))
	w.until("the new chat's route", func() bool {
		for _, run := range stubs(t, w, "who") {
			if out, ok := w.show(run.ID).Output.(map[string]any); ok && fmt.Sprint(out["rule"]) == "map[for:[team] name:helper]" {
				return true
			}
		}
		return false
	})
	chats := keyed(w, ".rook/blocks/chat.yaml")["telegram:1"]
	if len(chats) != 2 {
		t.Errorf("the thread had %d chats", len(chats))
	}
}

// A flow agent deleted while its question waits: the submit cancels the agent
// and, with it, the question it called. The asker is told the answer failed,
// and asked to ask again, though rules.yaml still names the agent, which has no
// file, so every later message fails the same way.
func TestDeletedAgentWhileQuestionWaits(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
		".rook/bridges/telegram/telegram.js": heard(from("2", "deploy now", 12)),
		"rules.yaml":                         "places:\n  dm: { readers: [asker] }\nagents:\n  - { name: desk, for: [bob] }\n",
		"agents/desk.yaml": `
			flow: desk
			input: { said: string, address: map }
			allow: [send:.rook/blocks/ask]
			stages:
			  question:
			    job:
			      call: { at: .rook/blocks/ask }
			      input: { people: [alice], text: "bob asks: ${input.said}. Allow?", choices: ["yes", "no"] }
			output: { reply: "alice said ${stages.question.output.choice}" }
		`,
	})
	w.sync(dir)
	question := w.question(map[string]bool{})
	desk := stubs(t, w, "desk")[0].ID
	if err := os.Remove(filepath.Join(dir, "agents/desk.yaml")); err != nil {
		t.Fatal(err)
	}
	w.sync(dir)
	w.until("the question to end", func() bool { return w.show(question.ID).HasEnded() })
	if run := w.show(desk); run.Status != "cancelled" {
		t.Errorf("the desk is %s", run.Status)
	}
	if run := w.show(question.ID); run.Status != "cancelled" {
		t.Errorf("the question is %s", run.Status)
	}
	sorry := "That could not be answered. Ask again in a while."
	w.until("the asker told", func() bool { return sayings(t, w)[sorry] != nil })
	// Each later message fails the same way, with the same advice.
	w.sendTo(".rook/blocks/chat.yaml", "telegram:2", "message", from("2", "again", 13))
	w.until("the second failure", func() bool {
		n := 0
		for _, run := range stubs(t, w, "say") {
			if in, ok := w.show(run.ID).Input.(map[string]any); ok && in["text"] == sorry {
				n++
			}
		}
		return n >= 4 // the block's say and the bridge's say, twice
	})
	var failure string
	for _, run := range stubs(t, w, "message") {
		for _, a := range w.ownAsks(run.ID, instance.AskFail) {
			failure = a.Error.Error
		}
	}
	if !strings.Contains(failure, "agents/desk") {
		t.Errorf("the record does not say the agent has no file: %q", failure)
	}
}

// A tool that fails is followed: the agent's record shows the tool's failure
// with its error, and the model is given it.
func TestFailedToolIsFollowed(t *testing.T) {
	w := newWorld(t)
	dir, model := traceProject(t, w, from("1", "what time is it", 10))
	write(t, dir, map[string]string{"parts/messages/clock.js": `throw new Error("the clock is broken")`})
	w.sync(dir)
	w.until("a question", func() bool { return len(questions(w)) == 1 })
	agent := stubs(t, w, "helper")[0].ID
	failed := fmt.Sprint(w.arrived(agent, "failed", "clock"))
	if !strings.Contains(failed, "the clock is broken") {
		t.Errorf("the record shows the clock's failure as %s", failed)
	}
	text, _ := json.Marshal(model.Calls[2].Messages)
	if !strings.Contains(string(text), "the clock is broken") {
		t.Errorf("the model was not told the clock failed: %s", text)
	}
}

// A tap by someone the question does not name does not answer it, and the
// question's record shows the tap, who it was from, and that it did not count.
func TestTapByAStrangerIsShownAndIgnored(t *testing.T) {
	w := newWorld(t)
	dir, _ := traceProject(t, w, from("1", "tell bob", 10))
	w.sync(dir)
	question := w.question(map[string]bool{})
	w.tap(question.ID, "2", "yes")
	w.until("the tap checked", func() bool {
		for _, run := range stubs(t, w, "tap") {
			if run.Parent == question.ID && run.Status == "done" {
				return true
			}
		}
		return false
	})
	if run := w.show(question.ID); run.Status != "running" {
		t.Fatalf("bob's tap ended the question: %s", run.Status)
	}
	var out any
	for _, run := range stubs(t, w, "tap") {
		out = w.show(run.ID).Output
	}
	if m, _ := out.(map[string]any); m["person"] != "bob" || m["counts"] != false {
		t.Errorf("the tap's record says %v", out)
	}
	w.tap(question.ID, "1", "yes")
	w.until("the reply said", func() bool { return sayings(t, w)["done: told bob"] != nil })
}

// A conversation's next message, sent while the approval waits, waits its
// turn: the chat takes it, the conversation holds it, and once the yes is given
// a second agent run answers it, given both messages as the request.
func TestSecondMessageWaitsForTheApproval(t *testing.T) {
	w := newWorld(t)
	dir, model := traceProject(t, w, from("1", "tell bob", 10))
	w.sync(dir)
	question := w.question(map[string]bool{})
	w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", from("1", "and the time?", 11))
	w.until("the chat to route it", func() bool {
		n := 0
		for _, run := range stubs(t, w, "route") {
			if run.Status == "done" {
				n++
			}
		}
		return n == 2
	})
	if n := len(stubs(t, w, "helper")); n != 1 {
		t.Errorf("%d agent runs while the first waits", n)
	}
	w.tap(question.ID, "1", "yes")
	second := w.question(map[string]bool{question.ID: true})
	agents := stubs(t, w, "helper")
	if len(agents) != 2 {
		t.Fatalf("%d agent runs", len(agents))
	}
	var request any
	for _, run := range agents {
		if in := w.show(run.ID).Input.(map[string]any); in["said"] == "and the time?" {
			request = in["request"]
		}
	}
	if request != "alice: tell bob\nalice: and the time?" {
		t.Errorf("the second agent was given the request %q", request)
	}
	w.tap(second.ID, "1", "no")
	_ = model
}

// The bridge's file removed and put back while a question waits: the bridge
// stops and starts, the conversation, agent and question go on, and a tap
// after it answers the question.
func TestBridgeRestartedWhileApprovalWaits(t *testing.T) {
	w := newWorld(t)
	dir, _ := traceProject(t, w, from("1", "tell bob", 10))
	w.sync(dir)
	question := w.question(map[string]bool{})
	bridge := readFile(t, dir, "bridges/telegram.yaml")
	removeListener(t, dir)
	w.sync(dir)
	if run := w.show(question.ID); run.Status != "running" {
		t.Fatalf("the question is %s", run.Status)
	}
	if err := os.WriteFile(filepath.Join(dir, "bridges/telegram.yaml"), []byte(bridge), 0o644); err != nil {
		t.Fatal(err)
	}
	w.sync(dir)
	w.tap(question.ID, "1", "yes")
	w.until("the reply said", func() bool { return sayings(t, w)["done: told bob"] != nil })
}

// Every run a message led to is listed with its location, and each but the
// listener's chat names the run that started it, so a person with only run ls
// and run show can walk the trail backwards from the question to the message.
func TestTrailWalksBackwardsFromTheQuestion(t *testing.T) {
	w := newWorld(t)
	dir, _ := traceProject(t, w, from("1", "tell bob", 10))
	w.sync(dir)
	question := w.question(map[string]bool{})
	var chain []string
	for id := question.ID; id != ""; {
		run := w.show(id)
		chain = append(chain, run.Machine)
		next := run.StartedBy
		if next == "" {
			next = run.Parent
		}
		id = next
	}
	want := []string{"ask", "approval", "helper", "message", "conversation", "chat", "telegram-listen", "telegram-bridge"}
	if !slices.Equal(chain, want) {
		t.Errorf("walking back from the question gives %v", chain)
	}
}

// An agent whose model's secret is not set: the submit names the secret as
// missing, the asker is told the answer failed, and the agent's run says why.
func TestMissingModelSecretIsNamed(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	_, address := toolModel(t, "hi", openClockTell()...)
	files := traceHelper(address, "You help alice.")
	files[".rook/bridges/telegram/telegram.js"] = heard(from("1", "hello", 10))
	write(t, dir, files)
	receipt := w.syncAt(w.Client, dir, project)
	if !strings.Contains(fmt.Sprint(receipt.Tree.Missing), "TEST_KEY") {
		t.Errorf("the submit names as missing %v", receipt.Tree.Missing)
	}
	w.until("the asker told", func() bool { return sayings(t, w)["That could not be answered. Ask again in a while."] != nil })
	agents := stubs(t, w, "helper")
	if len(agents) != 1 {
		t.Fatalf("%d agent runs", len(agents))
	}
	if run := w.show(agents[0].ID); !strings.Contains(run.Error+run.Refused, "TEST_KEY") {
		t.Errorf("the agent's run says %q %q", run.Error, run.Refused)
	}
}
