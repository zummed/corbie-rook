package rook_test

// Chats on Telegram, run against the stand-in for Telegram's Bot API: people,
// agents with and without a model, approvals by a tap, filters, files,
// failures, and the pages' examples.

import (
	"context"
	"encoding/json"
	"fmt"
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

// scenarioPeople: alice, bob and carol, each with the scope house, and alice
// with house:approve. A stranger holds no scope.
const scenarioPeople = `
	alice@example.com: { name: alice, scopes: [house, house:approve] }
	bob@example.com: { name: bob, scopes: [house] }
	carol@example.com: { name: carol, scopes: [house] }
`

// telegramFind is a project's own lookup for Telegram, which gives no email:
// it finds the email of each of the stand-in's people by their Telegram ID,
// and then reads people/scopes.yaml as Rook's lookup does.
const telegramFind = `
	include: .rook/people/file.yaml
	stages:
	  find:
	    job:
	      input: { email: "${input.email || ({ '1': 'alice@example.com', '2': 'bob@example.com', '3': 'carol@example.com' })[input.user] || ''}" }
`

// chatProject makes a Telegram project reaching the stand-in, with the
// scenario's people, a lookup that knows them by their Telegram IDs, and the
// files given (dedented), and sets the token.
func chatProject(t *testing.T, w *world, tg *telegram, files map[string]string) string {
	t.Helper()
	w.secret("TELEGRAM_TOKEN", "123:abc")
	dir := pointed(t, "telegram", tg.Server, "api.telegram.org")
	write(t, dir, map[string]string{"people/scopes.yaml": scenarioPeople, "people/find.yaml": telegramFind})
	write(t, dir, files)
	return dir
}

// echoFlow is an agent that is a flow with no model: it replies with who asked
// and what, and counts the turns of its conversation.
const echoFlow = `
	flow: NAME
	input: { messages: list, medium: string, asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
	stages:
	  turn: { job: { script: ./turn.js, input: { name: NAME, said: "${input.messages.map((m) => m.text).join(' ')}", asker: "${input.asker.name}", conversation: "${input.conversation}" } } }
	output: ${stages.turn.output}
`

const turnJS = `return { reply: input.name + " to " + input.asker + " (" + ((input.conversation || 0) + 1) + "): " + input.said, conversation: (input.conversation || 0) + 1 }`

func echoAgent(name string) string { return strings.ReplaceAll(echoFlow, "NAME", name) }

// modelAgent is an agent with a stand-in model at an address, which may open
// the toolkits extra gives.
func modelAgent(name, address, extra string) string {
	return "agent: " + name + "\n" +
		"include: .rook/agent.yaml\n" +
		"model: { name: test-model, service: anthropic, address: " + address + ", secret: TEST_KEY }\n" +
		"system: You help.\n" + extra
}

// standin starts a stand-in model that answers with the function given.
func standin(t *testing.T, w *world, answer func(llmstandin.Call) llmstandin.Reply) (*llmstandin.Server, string) {
	t.Helper()
	w.secret("TEST_KEY", "sk-test")
	model := &llmstandin.Server{Answer: answer}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	return model, web.URL
}

// last gives the last message of a model call, as text.
func last(c llmstandin.Call) string {
	if len(c.Messages) == 0 {
		return ""
	}
	b, _ := json.Marshal(c.Messages[len(c.Messages)-1])
	return string(b)
}

// toolResult says whether the last message of a call is a tool's result.
func toolResult(c llmstandin.Call) bool { return strings.Contains(last(c), `"tool_result"`) }

// calls is a reply that calls one tool.
func calls(name, args string) llmstandin.Reply {
	return llmstandin.Reply{Calls: []llmstandin.ToolCall{{ID: fmt.Sprintf("t%d", time.Now().UnixNano()), Name: name, Args: args}}}
}

// quiet waits a while, so that anything wrongly on its way can arrive.
func quiet() { time.Sleep(30 * interval) }

// failedRuns gives each failed run, as machine: error.
func failedRuns(t *testing.T, w *world) []string {
	runs, err := w.List(context.Background(), client.Filter{Finished: true})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range runs {
		if r.Status == "failed" {
			out = append(out, r.Machine+": "+w.show(r.ID).Error)
		}
	}
	return out
}

// A person writes to the bot in their own chat; an agent that is a flow with no
// model answers, in reply to their message, and "typing" is shown first. No
// model secret is set, and none is needed.
func TestOnePersonFlowAgent(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": echoAgent("helper"),
		"agents/turn.js":     turnJS,
	})
	tg.dm(1, "hello")
	w.sync(dir)
	tg.waitFor(w, "1", "helper to alice (1): hello")
	tg.dm(1, "again")
	tg.waitFor(w, "1", "helper to alice (2): again")
	tg.mu.Lock()
	first := tg.sent[0]
	typing := slices.Clone(tg.typing)
	tg.mu.Unlock()
	if rp, _ := first["reply_parameters"].(map[string]any); rp == nil || rp["message_id"] != 10.0 {
		t.Errorf("the reply does not answer the message: %v", first)
	}
	if len(typing) < 2 || typing[0] != "1" {
		t.Errorf("typing was shown in %v", typing)
	}
	if f := failedRuns(t, w); len(f) > 0 {
		t.Errorf("runs failed: %v", f)
	}
}

// A fresh project, as init writes it, has no agent and no model: anyone who
// writes is told the bot is not theirs, and nothing fails.
func TestStarterAnswersNobody(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	w.secret("TELEGRAM_TOKEN", "123:abc")
	dir := pointed(t, "telegram", tg.Server, "api.telegram.org")
	// Put back the starter's own people and rules, and remove testdata's agents.
	for _, name := range []string{"people.yaml", "rules.yaml"} {
		text, err := os.ReadFile(filepath.Join("starter", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), text, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.RemoveAll(filepath.Join(dir, "agents"))
	os.RemoveAll(filepath.Join(dir, "parts"))
	tg.dm(1, "hi")
	tg.dm(9, "hello?")
	w.sync(dir)
	tg.waitFor(w, "1", "This bot answers only the people it belongs to.")
	tg.waitFor(w, "9", "This bot answers only the people it belongs to.")
	if f := failedRuns(t, w); len(f) > 0 {
		t.Errorf("runs failed: %v", f)
	}
	if runs := w.at(".rook/blocks/conversation.yaml"); len(runs) != 0 {
		t.Errorf("a conversation started: %v", runs)
	}
}

// A stranger is refused, each time they write, and the refusal answers their
// message; nothing of theirs reaches an agent.
func TestStrangerIsRefusedEachTime(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": echoAgent("helper"),
		"agents/turn.js":     turnJS,
	})
	tg.dm(9, "one")
	tg.dm(9, "two")
	w.sync(dir)
	w.until("two refusals", func() bool { return len(tg.texts("9")) == 2 })
	for _, s := range tg.texts("9") {
		if s != "This bot answers only the people it belongs to." {
			t.Errorf("the stranger was told %q", s)
		}
	}
	if runs := w.at("agents/helper.yaml"); len(runs) != 0 {
		t.Errorf("the agent ran for a stranger")
	}
}

// Three people write at once in their own chats: each gets the agent rules.yaml
// gives them, in a conversation of their own, and carol, in no group, is
// told the bot does not answer her here.
func TestSeveralPeopleGetTheirOwnAgents(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": echoAgent("helper"),
		"agents/team.yaml":   echoAgent("team"),
		"agents/turn.js":     turnJS,
	})
	tg.dm(1, "a1")
	tg.dm(2, "b1")
	tg.dm(3, "c1")
	tg.dm(1, "a2")
	tg.dm(2, "b2")
	w.sync(dir)
	tg.waitFor(w, "1", "helper to alice (2): a2")
	tg.waitFor(w, "2", "team to bob (2): b2")
	tg.waitFor(w, "3", "This bot does not answer you here.")
	if got := tg.texts("1"); !slices.Equal(got, []string{"helper to alice (1): a1", "helper to alice (2): a2"}) {
		t.Errorf("alice was told %q", got)
	}
	if got := tg.texts("2"); !slices.Equal(got, []string{"team to bob (1): b1", "team to bob (2): b2"}) {
		t.Errorf("bob was told %q", got)
	}
	keys := keyed(w, ".rook/blocks/conversation.yaml")
	if len(keys) != 2 || keys["telegram:1:helper"] == nil || keys["telegram:2:team"] == nil {
		t.Errorf("the conversations are %v", keys)
	}
}

// A message in a Telegram group is not heard, even when it names the bot:
// Telegram's listener hears only direct messages, as rook-design says, and
// logs that. Nothing is said in the group, and no conversation starts for it.
func TestGroupMessagesAreNotHeard(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, nil)
	tg.group(-100, 1, "@rook_bot hello from the group")
	tg.group(-100, 2, "and me")
	tg.dm(1, "direct")
	w.sync(dir)
	tg.waitFor(w, "1", "direct")
	quiet()
	if got := tg.texts("-100"); len(got) != 0 {
		t.Errorf("the bot said in the group %q", got)
	}
	for key := range keyed(w, "conversation.yaml") {
		if key != "telegram:1:1:assistant" {
			t.Errorf("a conversation %s", key)
		}
	}
	if got := logged(w, "telegram"); !slices.Contains(got, "a message in a group is not heard: the Telegram listener hears only direct messages") {
		t.Errorf("the listener logged %q", got)
	}
}

// The /start Telegram sends when a person opens the bot is logged and not
// passed on: the agent answers only the message after it.
func TestTelegramStartIsNotPassedOn(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, nil)
	tg.dm(1, "/start")
	tg.dm(1, "hello")
	w.sync(dir)
	tg.waitFor(w, "1", "hello")
	quiet()
	if got := tg.texts("1"); len(got) != 1 || strings.Contains(got[0], "/start") {
		t.Errorf("the bot said %q", got)
	}
	if got := logged(w, "telegram"); !slices.Contains(got, "a /start is not passed on") {
		t.Errorf("the listener logged %q", got)
	}
}

// Telegram's listener takes no input: overhear given in the router fails the
// listener, naming it, and is not passed over.
func TestTelegramOverhearIsRefused(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, nil)
	setSite(t, dir, router("telegram"), "restart: { after: always }", "restart: { after: always }, input: { overhear: true }")
	w.sync(dir)
	w.until("the listener refuses overhear", func() bool {
		return slices.ContainsFunc(failures(w), func(s string) bool { return strings.Contains(s, "additional properties 'overhear' not allowed") })
	})
}

// To choose by words, a project writes its own agent: a flow at agents/router
// that calls another agent by what was said. rook-project says a machine of your
// own at agents/<name> answers a message with its own steps.
func TestRouterAgentChoosesByWords(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": echoAgent("helper"),
		"agents/deploy.yaml": echoAgent("deploy"),
		"agents/turn.js":     turnJS,
		"agents/router.yaml": `
			flow: router
			input: { said: string, asker: string, conversation: "any?" }
			allow: [send:agents/deploy, send:agents/helper]
			stages:
			  deploy:
			    when: "${/\\bdeploy\\b/i.test(input.said)}"
			    job: { call: { at: agents/deploy }, input: { said: "${input.said}", asker: "${input.asker}", conversation: "${input.conversation}" } }
			  other:
			    when: "${!/\\bdeploy\\b/i.test(input.said)}"
			    job: { call: { at: agents/helper }, input: { said: "${input.said}", asker: "${input.asker}", conversation: "${input.conversation}" } }
			output: "${stages.deploy.skipped ? stages.other.output : stages.deploy.output}"
		`,
		"rules.yaml": `
			places:
			  dm: { readers: [asker] }
			agents:
			  - { name: router, for: [alice] }
		`,
	})
	tg.dm(1, "please deploy the site")
	tg.dm(1, "how are you")
	w.sync(dir)
	tg.waitFor(w, "1", "deploy to alice (1): please deploy the site")
	tg.waitFor(w, "1", "helper to alice (2): how are you")
}

// rules.yaml names an agent that has no file. The person is told only that
// the message could not be answered, with no name of the project's files, and
// the record names the agent's location.
func TestRuleNamesAMissingAgent(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"rules.yaml": `
			places:
			  dm: { readers: [asker] }
			agents:
			  - { name: ghost, for: [alice] }
		`,
	})
	tg.dm(1, "hello")
	w.sync(dir)
	tg.waitFor(w, "1", "That could not be answered.")
	if said := strings.Join(tg.texts("1"), "\n"); strings.Contains(said, "ghost") || strings.Contains(said, "yaml") {
		t.Errorf("the person was told %q", said)
	}
	if f := failures(w); !slices.ContainsFunc(f, func(s string) bool { return strings.Contains(s, "agents/ghost") }) {
		t.Errorf("no failure names the agent: %q", f)
	}
}

// houseToolkit is a toolkit with a read tool, lights, and an act tool, tell,
// which waits for a yes from someone with house:approve in alice's chat. It
// names no asker, so alice may approve what she asked for.
const houseToolkit = `
	description: The house, and messages to its people.
	ask:
	  machine: .rook/approval.yaml
	  input: { ask: gromit/telegram/chat/ask, lookup: people/find, approvers: "house:approve", in: "1", request: "${input.messages.map((m) => m.text).join('\\n')}" }
	tools:
	  lights:
	    description: Which lights are on.
	    args: {}
	    job: { script: ./house/lights.js }
	  tell:
	    description: Write a text in a person's Telegram chat, by the chat's ID.
	    args: { chat: string, text: string }
	    job:
	      call: { at: gromit/telegram/chat/post }
	      input: { in: "${args.chat}", text: "${args.text}" }
	      allow: [send:gromit/telegram/chat/post]
	      approve: true
`

// turnOf splits a call into what the person last said, and the tool results
// that came after it, each as JSON.
func turnOf(c llmstandin.Call) (said string, results []string) {
	for i := len(c.Messages) - 1; i >= 0; i-- {
		b, _ := json.Marshal(c.Messages[i])
		s := string(b)
		m, _ := c.Messages[i].(map[string]any)
		if m["role"] != "user" {
			continue
		}
		if strings.Contains(s, `"tool_result"`) {
			results = append([]string{s}, results...)
			continue
		}
		return s, results
	}
	return "", results
}

// houseModel answers by the words the person last used:
//
//	lights   opens house, calls lights, and says what it gave
//	tell     opens house, calls tell bob hello, and says what came back
//	twice    opens house, calls tell twice in one turn
//	anything else   says "ok: " and the words
func houseModel(c llmstandin.Call) llmstandin.Reply {
	said, results := turnOf(c)
	step := len(results)
	words := func(w string) bool { return strings.Contains(said, w) }
	result := ""
	if step > 0 {
		result = results[step-1]
	}
	switch {
	case words("lights"):
		switch step {
		case 0:
			return calls("open", `{"toolkit": "house"}`)
		case 1:
			return calls("lights", `{}`)
		}
		return llmstandin.Reply{Text: "lights said " + result}
	case words("twice"):
		switch step {
		case 0:
			return calls("open", `{"toolkit": "house"}`)
		case 1:
			return llmstandin.Reply{Calls: []llmstandin.ToolCall{
				{ID: "x1", Name: "tell", Args: `{"chat": "2", "text": "first"}`},
				{ID: "x2", Name: "tell", Args: `{"chat": "3", "text": "second"}`},
			}}
		}
		return llmstandin.Reply{Text: "twice gave " + result}
	case words("tell"):
		switch step {
		case 0:
			return calls("open", `{"toolkit": "house"}`)
		case 1:
			return calls("tell", `{"chat": "2", "text": "hello from alice"}`)
		}
		return llmstandin.Reply{Text: "tell gave " + result}
	}
	start := strings.Index(said, "asks: ")
	if start < 0 {
		return llmstandin.Reply{Text: "ok"}
	}
	end := strings.IndexAny(said[start:], `"\`)
	return llmstandin.Reply{Text: "ok: " + said[start+6:start+end]}
}

// houseProject makes a project whose agent at agents/assistant may open the
// toolkit house, for a person with the scope house, and asks the stand-in
// model.
func houseProject(t *testing.T, w *world, tg *telegram, extra map[string]string) (string, *llmstandin.Server) {
	t.Helper()
	model, address := standin(t, w, houseModel)
	files := map[string]string{
		"agents/assistant.yaml":    modelAgent("assistant", address, "toolkits:\n  house: { include: toolkits/house.yaml, when: \"${input.asker.scopes.includes('house')}\" }\n"),
		"toolkits/house.yaml":      houseToolkit,
		"toolkits/house/lights.js": `return { on: ["kitchen", "hall"] }`,
		"agents/team.yaml":         echoAgent("team"),
		"agents/turn.js":           turnJS,
	}
	for k, v := range extra {
		files[k] = v
	}
	dir := chatProject(t, w, tg, files)
	return dir, model
}

// A read tool runs with no one asked, and the model's reply carries its result.
func TestModelReadToolRunsAtOnce(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, nil)
	tg.dm(1, "which lights are on?")
	w.sync(dir)
	tg.waitFor(w, "1", "lights said")
	if got := tg.texts("1")[0]; !strings.Contains(got, "kitchen") {
		t.Errorf("alice was told %q", got)
		for _, c := range model.Calls {
			b, _ := json.Marshal(c.Messages)
			t.Logf("call %d: %s", c.Turn, b)
		}
	}
	if len(tg.buttons()) != 0 {
		t.Errorf("a question was asked for a read tool: %v", tg.buttons())
	}
}

// A held tool asks alice in her own chat with Yes and No; her tap on Yes is
// heard by the listener, the question's message shows her answer with no
// buttons, the tool runs (bob is told), and alice gets the reply.
func TestApprovalByATapOnYes(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	b := tg.buttons()
	if b[0][0] != "1" || !strings.HasSuffix(b[0][1], " yes") || !strings.HasSuffix(b[1][1], " no") {
		t.Errorf("the buttons are %v", b)
	}
	question := tg.texts("1")[0]
	for _, want := range []string{"assistant wants to run tell.", "hello from alice", "> tell bob hello"} {
		if !strings.Contains(question, want) {
			t.Errorf("the question %q lacks %q", question, want)
		}
	}
	tg.tapOn(t, 0, 1, "yes")
	tg.waitFor(w, "2", "hello from alice")
	tg.waitFor(w, "1", "tell gave")
	w.until("the question closed", func() bool { return len(tg.edited()) == 1 })
	if got := tg.edited()[0]; !strings.HasSuffix(got, "alice said yes.") {
		t.Errorf("the question now shows %q", got)
	}
	tg.mu.Lock()
	acks := len(tg.acks)
	tg.mu.Unlock()
	if acks != 1 {
		t.Errorf("%d taps were acknowledged", acks)
	}
}

// A tap on No: the tool does not run, the model is told alice said no,
// and alice gets the model's reply.
func TestApprovalRefusedByATap(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	tg.tapOn(t, 0, 1, "no")
	tg.waitFor(w, "1", "tell gave")
	quiet()
	if got := tg.texts("2"); len(got) != 0 {
		t.Errorf("bob was told %q after a no", got)
	}
	if got := last(model.Calls[len(model.Calls)-1]); !strings.Contains(got, "alice said no.") {
		t.Errorf("the model was told %s", got)
	}
	if got := tg.edited(); len(got) != 1 || !strings.HasSuffix(got[0], "alice said no.") {
		t.Errorf("the question shows %q", got)
	}
}

// Bob, who lacks house:approve, and a stranger tap Yes: neither counts, each is
// told why in an alert, the question stays open, and then alice's No counts.
func TestOnlyTheApproversTapCounts(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	tg.tapOn(t, 0, 2, "yes")
	tg.tapOn(t, 0, 9, "yes")
	w.until("the taps heard", tg.consumed)
	quiet()
	if got := tg.texts("2"); len(got) != 0 {
		t.Fatalf("bob was told %q: a tap by someone else approved it", got)
	}
	if len(tg.edited()) != 0 {
		t.Fatalf("the question was closed: %q", tg.edited())
	}
	w.until("both told", func() bool { return len(tg.alerts()) == 2 })
	if got := tg.alerts(); got[0] != "Only someone with the scope house:approve can answer this." || got[1] != got[0] {
		t.Errorf("the alerts are %q", got)
	}
	tg.tapOn(t, 0, 1, "no")
	tg.waitFor(w, "1", "tell gave")
}

// A question nobody answers gives no after seven days: the message shows that
// no one answered, the tool does not run, and alice gets a reply.
func TestApprovalTimesOut(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	w.until("the question waiting", func() bool { return len(questions(w)) == 1 })
	w.set(w.now().Add(7*24*time.Hour + time.Minute))
	tg.waitFor(w, "1", "tell gave")
	if got := tg.edited(); len(got) != 1 || !strings.HasSuffix(got[0], "No one answered in time, so the answer is no.") {
		t.Errorf("the question shows %q", got)
	}
	if got := last(model.Calls[len(model.Calls)-1]); !strings.Contains(got, "No one answered in time, so the answer is no.") {
		t.Errorf("the model was told %s", got)
	}
	if got := tg.texts("2"); len(got) != 0 {
		t.Errorf("bob was told %q", got)
	}
}

// A toolkit whose ask names the asker: alice's own tap on her request does not
// count, she is told someone else must answer it, and the buttons stay. Carol,
// who also holds house:approve, answers it.
func TestOwnRequestIsNotApprovedByTheAsker(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, map[string]string{
		"toolkits/house.yaml": strings.Replace(houseToolkit, `in: "1",`, `in: "1", asker: "${input.asker.email}",`, 1),
	})
	write(t, dir, map[string]string{"people/scopes.yaml": strings.Replace(scenarioPeople, "carol, scopes: [house]", "carol, scopes: [house, house:approve]", 1)})
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	tg.tapOn(t, 0, 1, "yes")
	w.until("alice told", func() bool { return len(tg.alerts()) == 1 })
	if got := tg.alerts(); got[0] != "You asked for this, so someone else must answer it." {
		t.Errorf("the alert is %q", got)
	}
	quiet()
	if got := tg.edited(); len(got) != 0 {
		t.Fatalf("alice's own tap closed the question: %q", got)
	}
	tg.tapOn(t, 0, 3, "yes")
	tg.waitFor(w, "2", "hello from alice")
	if got := tg.edited(); len(got) != 1 || !strings.HasSuffix(got[0], "carol said yes.") {
		t.Errorf("the question shows %q", got)
	}
}

// A tap whose choice is not one of the buttons (Telegram's callback data is
// whatever the client sends) does not count, the presser is told nothing,
// since no button sends it, and the question waits for one that does.
func TestForgedChoiceApprovesNothing(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	q, _, _ := strings.Cut(tg.buttons()[0][1], " ")
	tg.tapData(1, q+" YES")
	w.until("the tap heard", tg.consumed)
	quiet()
	if got := tg.edited(); len(got) != 0 {
		t.Fatalf("a choice that is no button closed the question: %q", got)
	}
	if got := tg.alerts(); len(got) != 0 {
		t.Errorf("alice was told %q", got)
	}
	tg.tapOn(t, 0, 1, "yes")
	tg.waitFor(w, "2", "hello from alice")
	if got := tg.edited(); len(got) != 1 || !strings.HasSuffix(got[0], "alice said yes.") {
		t.Errorf("the question shows %q", got)
	}
}

// Five quick messages to a model agent are answered one at a time, in the order
// sent, each reply answering its own message, and each model call carrying the
// conversation before it.
func TestQuickMessagesAnsweredInOrder(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, nil)
	for i := 1; i <= 5; i++ {
		tg.dm(1, fmt.Sprintf("m%d", i))
	}
	w.sync(dir)
	w.until("five answers", func() bool { return len(tg.texts("1")) == 5 })
	want := []string{"ok: m1", "ok: m2", "ok: m3", "ok: m4", "ok: m5"}
	if got := tg.texts("1"); !slices.Equal(got, want) {
		t.Errorf("alice was told %q", got)
	}
	tg.mu.Lock()
	for i, m := range tg.sent {
		if rp, _ := m["reply_parameters"].(map[string]any); rp == nil || rp["message_id"] != float64(10+i) {
			t.Errorf("reply %d answers %v", i, m["reply_parameters"])
		}
	}
	tg.mu.Unlock()
	if n := len(model.Calls[4].Messages); n != 9 {
		t.Errorf("the fifth call carried %d messages, and not the four turns before it", n)
	}
}

// Bob writes while alice's approval waits: he has his own conversation, and is
// answered at once.
func TestOthersAreAnsweredWhileAnApprovalWaits(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	tg.dm(2, "hi")
	tg.waitFor(w, "2", "team to bob (1): hi")
}

// A message sent just as the quiet timer fires, a day after the last, is
// answered: by the old chat and conversation or by new ones, but never lost.
// The clock moves past a day, and the message is sent at once.
func TestMessageAsTheQuietTimerFires(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	for round := range 3 {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			w := newWorld(t)
			dir := copyStarter(t)
			removeListener(t, dir)
			w.sync(dir)
			w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", message("one"))
			w.until("the first answer, and both waiting", func() bool {
				chats, convs := keyed(w, ".rook/blocks/chat.yaml")["telegram:1"], keyed(w, ".rook/blocks/conversation.yaml")["telegram:1:helper"]
				return sayings(t, w)["one"] != nil && len(chats) == 1 && len(convs) == 1 && w.show(chats[0].ID).Timers == 1 && w.show(convs[0].ID).Timers == 1
			})
			// Each round lands the message a little differently against the timers.
			w.set(w.now().Add(24*time.Hour + time.Duration(round)*interval))
			time.Sleep(time.Duration(round) * interval)
			w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", message("two"))
			w.until("the second answer", func() bool { return sayings(t, w)["two"] != nil })
		})
	}
}

// The chat's quiet timer fires, ending the chat, while the conversation's has
// not yet (it started later). A message then starts a new chat, which sends to
// the conversation that is about to end: the message is still answered.
func TestMessageBetweenTheTwoQuietTimers(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	// The agent takes a moment, so the conversation's timer starts later than
	// the chat's.
	write(t, dir, map[string]string{
		"agents/helper.yaml": `
			flow: helper
			input: { said: string }
			stages:
			  slow: { job: { script: ./slow.js, input: { said: "${input.said}" } } }
			output: { reply: "${input.said}" }
		`,
		"agents/slow.js": `const end = Date.now() + 400; while (Date.now() < end) {} return 1`,
	})
	w.sync(dir)
	w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", message("one"))
	w.until("the first answer", func() bool { return sayings(t, w)["one"] != nil })
	chat := keyed(w, ".rook/blocks/chat.yaml")["telegram:1"][0].ID
	w.until("waiting", func() bool {
		convs := keyed(w, ".rook/blocks/conversation.yaml")["telegram:1:helper"]
		return len(convs) == 1 && w.show(convs[0].ID).Timers == 1 && w.show(chat).Timers == 1
	})
	w.set(w.now().Add(24*time.Hour + 100*time.Millisecond))
	w.until("the chat ended", func() bool { return w.show(chat).HasEnded() })
	w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", message("two"))
	w.until("the second answer", func() bool { return sayings(t, w)["two"] != nil })
}

// fileModel answers with what it was shown: "files: " and whether each kind of
// thing is in the last message.
func fileModel(c llmstandin.Call) llmstandin.Reply {
	said, results := turnOf(c)
	if len(results) > 0 {
		return llmstandin.Reply{Text: "result: " + results[len(results)-1]}
	}
	return llmstandin.Reply{Text: "shown: " + said}
}

// A sticker, an edited message and a message from a bot are not messages: none
// is answered, and the text after them is.
func TestNonMessagesAreSkipped(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, nil)
	tg.dm(1, "", map[string]any{"sticker": map[string]any{"file_id": "S1", "file_unique_id": "u", "width": 1, "height": 1, "is_animated": false, "is_video": false, "type": "regular"}})
	tg.update(map[string]any{"edited_message": map[string]any{"message_id": 10, "date": 1, "chat": map[string]any{"id": 1, "type": "private"}, "from": map[string]any{"id": 1, "is_bot": false, "first_name": "Alice"}, "text": "edited"}})
	tg.update(map[string]any{"message": map[string]any{"message_id": 99, "date": 1, "chat": map[string]any{"id": 1, "type": "private"}, "from": map[string]any{"id": 1, "is_bot": true, "first_name": "Bot"}, "text": "from a bot"}})
	tg.dm(1, "real")
	w.sync(dir)
	tg.waitFor(w, "1", "real")
	quiet()
	if got := tg.texts("1"); len(got) != 1 {
		t.Errorf("alice was told %q", got)
	}
}

// An agent sends a file and a reply: the file goes to the chat as a document,
// before the reply.
func TestAgentSendsAFileThenReplies(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	_, address := standin(t, w, sending(`{"name": "list.txt", "text": "eggs\nmilk\n"}`))
	dir := chatProject(t, w, tg, map[string]string{"agents/helper.yaml": modelAgent("helper", address, "")})
	tg.dm(1, "the shopping list")
	w.sync(dir)
	tg.waitFor(w, "1", "sent")
	tg.mu.Lock()
	defer tg.mu.Unlock()
	if len(tg.uploads) != 1 || tg.uploads[0].Name != "list.txt" || tg.uploads[0].Body != "eggs\nmilk\n" || tg.uploads[0].To != "1" {
		t.Errorf("the bot sent %+v", tg.uploads)
	}
	if i, j := slices.Index(tg.methods, "sendDocument"), slices.Index(tg.methods, "sendMessage"); i < 0 || i > j {
		t.Errorf("the methods were called in the order %v", tg.methods)
	}
}

// A send with neither a blob nor a text fails the tool, and the model is told
// why; alice still gets a reply.
func TestSendWithNothingTellsTheModel(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	_, address := standin(t, w, sending(`{"name": "empty.txt"}`))
	dir := chatProject(t, w, tg, map[string]string{"agents/helper.yaml": modelAgent("helper", address, "")})
	tg.dm(1, "send nothing")
	w.sync(dir)
	tg.waitFor(w, "1", "sent")
	if len(tg.uploads) != 0 {
		t.Errorf("the bot sent %+v", tg.uploads)
	}
}

// overheard is a message that does not name the bot, as a listener with
// overhear sends it, in alice's Telegram chat.
func overheard(from, text string) map[string]any {
	return map[string]any{"bridge": "telegram", "text": text, "chat": "1", "from": from, "place": "dm", "answers": "1:20", "unaddressed": true}
}

// attendFlow answers a message holding "bot".
const attendFlow = `
	flow: attend
	input: { said: string, asker: string, address: map, history: "any?" }
	stages:
	  judge: { job: { script: ./judge.js, input: { said: "${input.said}" } } }
	output: { reply: { answer: "${stages.judge.output}" } }
`

// With attend.yaml, an overheard message is answered only when it says so; a
// stranger's is neither judged nor refused; the files are named to it.
func TestAttendDecides(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	write(t, dir, map[string]string{"attend.yaml": attendFlow, "judge.js": `return input.said.includes("bot")`})
	w.sync(dir)
	w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", overheard("1", "people talking"))
	w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", overheard("9", "stranger asks the bot"))
	file := overheard("1", "bot, see this")
	file["attachments"] = []any{map[string]any{"name": "a.png", "type": "image/png", "size": 3, "id": "F"}}
	w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", file)
	w.until("the answer", func() bool { return sayings(t, w)["bot, see this"] != nil })
	quiet()
	said := sayings(t, w)
	if said["people talking"] != nil || said["This bot answers only the people it belongs to."] != nil || said["stranger asks the bot"] != nil {
		t.Errorf("what was said: %v", said)
	}
	var judged []string
	for _, r := range stubs(t, w, "attend") {
		judged = append(judged, fmt.Sprint(w.show(r.ID).Input.(map[string]any)["said"]))
	}
	slices.Sort(judged)
	if !slices.Equal(judged, []string{"bot, see this\n(files: a.png)", "people talking"}) {
		t.Errorf("attend judged %q", judged)
	}
}

// attend.yaml that fails, or that gives a reply of another shape, answers
// nothing, and the chat goes on.
func TestAttendThatFailsOrMisspeaks(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	for name, attend := range map[string]string{
		"fails":        "flow: attend\ninput: { said: string }\nstages:\n  x: { job: { script: ./judge.js } }\n",
		"plain true":   "flow: attend\ninput: { said: string }\nstages:\n  x: { job: { script: ./ok.js } }\noutput: { reply: true }\n",
		"string false": "flow: attend\ninput: { said: string }\nstages:\n  x: { job: { script: ./ok.js } }\noutput: { reply: { answer: \"false\" } }\n",
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			dir := copyStarter(t)
			removeListener(t, dir)
			write(t, dir, map[string]string{"attend.yaml": attend, "judge.js": `throw new Error("no model")`, "ok.js": "return 1"})
			w.sync(dir)
			w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", overheard("1", "overheard"))
			w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", message("named"))
			w.until("the answer", func() bool { return sayings(t, w)["named"] != nil })
			quiet()
			// rook-design: answered only when attend gives { reply: { answer: true } }.
			if sayings(t, w)["overheard"] != nil {
				t.Errorf("attend that gives %s answered the overheard message", name)
			}
		})
	}
}

// The model fails (status 500): alice is told it could not be answered, and her
// next message starts afresh and is answered.
func TestModelFails(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	_, address := standin(t, w, func(c llmstandin.Call) llmstandin.Reply {
		if strings.Contains(last(c), "boom") {
			return llmstandin.Reply{Error: "overloaded"}
		}
		return fileModel(c)
	})
	dir := chatProject(t, w, tg, map[string]string{"agents/helper.yaml": modelAgent("helper", address, "")})
	tg.dm(1, "boom")
	tg.dm(1, "fine")
	w.sync(dir)
	w.until("two answers", func() bool { return len(tg.texts("1")) == 2 })
	got := tg.texts("1")
	if got[0] != "That could not be answered." || !strings.Contains(got[1], "fine") {
		t.Errorf("alice was told %q", got)
	}
}

// The model's key is not set: alice is told it could not be answered, and
// the error names the secret, not its value.
func TestModelKeyMissing(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	model := &llmstandin.Server{Answer: fileModel}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	dir := chatProject(t, w, tg, map[string]string{"agents/helper.yaml": modelAgent("helper", web.URL, "")})
	tg.dm(1, "hello")
	w.sync(dir)
	tg.waitFor(w, "1", "That could not be answered.")
	for _, r := range w.at("agents/helper.yaml") {
		if e := w.show(r.ID).Error; !strings.Contains(e, "TEST_KEY") {
			t.Errorf("the agent failed with %q", e)
		}
	}
}

// A long reply is cut into Telegram's 4096 characters, at a line's end, the
// first part answering the message; a chart is written as a table. The agent
// at agents/assistant replies with what it was sent.
func TestLongReplyAndChart(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	long := strings.TrimSpace(strings.Repeat("a line of the reply that is long enough\n", 150))
	chart := "Here.\n\n```chart\n{\"type\": \"bar\", \"title\": \"Tickets\", \"labels\": [\"Jan\", \"Feb\"], \"series\": [{\"name\": \"Email\", \"values\": [140, 165]}]}\n```\n"
	dir := chatProject(t, w, tg, nil)
	tg.dm(1, long)
	tg.dm(1, chart)
	w.sync(dir)
	w.until("three messages", func() bool { return len(tg.texts("1")) == 3 })
	got := tg.texts("1")
	if len(got[0]) > 4096 || !strings.HasSuffix(got[0], "\n") || got[0]+got[1] != long {
		t.Errorf("the long reply was cut into %d and %d", len(got[0]), len(got[1]))
	}
	if strings.Contains(got[2], "```") || !strings.Contains(got[2], "165") {
		t.Errorf("the chart was written as %q", got[2])
	}
}

// Telegram refuses to send to a chat (the person blocked the bot). The
// failure, wherever it is recorded, never holds the token.
func TestRefusalDoesNotLeakTheToken(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	tg.refuse["1"] = "Forbidden: bot was blocked by the user"
	dir := chatProject(t, w, tg, nil)
	tg.dm(1, "hello")
	w.sync(dir)
	w.until("a say failed", func() bool {
		for _, f := range failedRuns(t, w) {
			if strings.Contains(f, "blocked") {
				return true
			}
		}
		return false
	})
	runs, err := w.List(context.Background(), client.Filter{Finished: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		shown := w.show(r.ID)
		text := fmt.Sprint(shown.Error, shown.Input, shown.Output)
		for _, e := range w.record(r.ID) {
			b, _ := json.Marshal(e)
			text += string(b)
		}
		if strings.Contains(text, "123:abc") {
			t.Errorf("the run %s %s holds the token", r.Machine, r.ID)
		}
	}
}

// While the conversation answers a message, Telegram shows "typing" in its
// chat, and says nothing else for it.
func TestTypingIsShownWhileAnswering(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, nil)
	tg.dm(1, "check")
	w.sync(dir)
	tg.waitFor(w, "1", "check")
	tg.mu.Lock()
	typing := slices.Clone(tg.typing)
	tg.mu.Unlock()
	if !slices.Equal(typing, []string{"1"}) || !slices.Equal(tg.said(), []string{"check"}) {
		t.Errorf("typing was shown in %v, and the bot said %q", typing, tg.said())
	}
}

// ---------------------------------------------------------------------------
// The pages' examples, run on Telegram

// pageExample gives the text of a file a page shows.
func pageExample(t *testing.T, page, path string) string {
	t.Helper()
	for _, ex := range examples(t, page) {
		if ex.path == path {
			return ex.text
		}
	}
	t.Fatalf("%s shows no %s", page, path)
	return ""
}

func writeRaw(t *testing.T, dir, name, text string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// rook-design's ask-alice.yaml, as written: alice is asked on Telegram, taps
// Yes, and the run gives yes.
func TestPageAskAlice(t *testing.T) {
	t.Skip("R8 revives this: pages")
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, nil)
	writeRaw(t, dir, "ask-alice.yaml", pageExample(t, "rook-design", "ask-alice.yaml"))
	w.sync(dir)
	id := w.start("ask-alice.yaml", nil)
	w.until("the question", func() bool { return len(tg.buttons()) == 2 })
	if got := tg.texts("1"); len(got) != 1 || got[0] != "The disk is full. Clear the cache?" {
		t.Errorf("alice was asked %q", got)
	}
	tg.tapOn(t, 0, 1, "yes")
	w.until("the run ended", func() bool { return w.show(id).HasEnded() })
	if r := w.show(id); r.Status != "done" || r.Output != "yes" {
		t.Errorf("the run ended %s with %v: %s", r.Status, r.Output, r.Error)
	}
}

// rook-design's schedules/evening.yaml, as written: at 18:00 in London, alice
// is told the day is over on Telegram.
func TestPageEveningSchedule(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, nil)
	writeRaw(t, dir, "schedules/evening.yaml", pageExample(t, "rook-design", "schedules/evening.yaml"))
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Skip(err)
	}
	now := w.now().In(london)
	w.set(time.Date(now.Year(), now.Month(), now.Day(), 17, 59, 50, 0, london))
	w.sync(dir)
	w.kept("schedules/evening.yaml")
	w.set(time.Date(now.Year(), now.Month(), now.Day(), 18, 0, 1, 0, london))
	tg.waitFor(w, "1", "The day is over.")
}

// rook-design's parts/messages/part.yaml and rook-project's helper, as written,
// with only the model's address and service changed to the stand-in's.
func TestPagePartAndAgent(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	model, address := standin(t, w, func(c llmstandin.Call) llmstandin.Reply {
		said, results := turnOf(c)
		_ = said
		switch len(results) {
		case 0:
			return calls("open", `{"toolkit": "messages"}`)
		case 1:
			return calls("tell", `{"person": "bob", "text": "dinner at six"}`)
		}
		return llmstandin.Reply{Text: "Told bob."}
	})
	dir := chatProject(t, w, tg, nil)
	w.secret("ANTHROPIC_KEY", "sk-test")
	writeRaw(t, dir, "parts/messages/part.yaml", pageExample(t, "rook-design", "parts/messages/part.yaml"))
	helper := pageExample(t, "rook-design", "agents/helper.yaml")
	helper = strings.Replace(helper, "model: { name: claude-sonnet-5-5, secret: ANTHROPIC_KEY }", "model: { name: claude-sonnet-5-5, service: anthropic, address: "+address+", secret: ANTHROPIC_KEY }", 1)
	writeRaw(t, dir, "agents/helper.yaml", helper)
	tg.dm(1, "tell bob dinner is at six")
	w.sync(dir)
	w.until("the question", func() bool { return len(tg.buttons()) == 2 })
	tg.tapOn(t, 0, 1, "yes")
	tg.waitFor(w, "2", "dinner at six")
	tg.waitFor(w, "1", "Told bob.")
	if s := model.Calls[0].System; !strings.Contains(s, "plain text") {
		t.Errorf("the system text does not hold the format for Telegram: %q", s)
	}
}

// Two taps in a row from alice, Yes then No: the first is the answer; the
// second changes nothing.
func TestDoubleTap(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	tg.tapOn(t, 0, 1, "yes")
	tg.tapOn(t, 0, 1, "no")
	tg.waitFor(w, "2", "hello from alice")
	tg.waitFor(w, "1", "tell gave")
	w.until("closed", func() bool { return len(tg.edited()) == 1 })
	quiet()
	if got := tg.edited(); len(got) != 1 || !strings.HasSuffix(got[0], "alice said yes.") {
		t.Errorf("the question shows %q", got)
	}
}

// Bob and alice each ask for a held call, which the house toolkit lets both
// open. alice gets two questions, and answers the second (bob's) first: each
// tap goes to its own question.
func TestTwoQuestionsAnsweredOutOfOrder(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("alice's question", func() bool { return len(tg.buttons()) == 2 })
	tg.dm(2, "please tell bob hello")
	w.until("bob's question", func() bool { return len(tg.buttons()) == 4 })
	b := tg.buttons()
	if b[2][0] != "1" {
		t.Errorf("bob's question went to %s", b[2][0])
	}
	tg.tapOn(t, 1, 1, "no")
	tg.waitFor(w, "2", "tell gave")
	quiet()
	if len(tg.texts("1")) != 2 {
		t.Errorf("alice's own request was answered by a tap on bob's: %q", tg.texts("1"))
	}
	tg.tapOn(t, 0, 1, "yes")
	tg.waitFor(w, "1", "tell gave")
	tg.waitFor(w, "2", "hello from alice")
	if q := tg.texts("1")[1]; !strings.Contains(q, "> please tell bob hello") {
		t.Errorf("alice was shown %q for bob's request", q)
	}
}

// A model that never stops calling a tool reaches the agent's limit of turns:
// alice is told it could not be answered, and the next message is answered.
func TestAgentRunsOutOfTurns(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, nil)
	model.Answer = func(c llmstandin.Call) llmstandin.Reply {
		first, _ := json.Marshal(c.Messages[0])
		if strings.Contains(string(first), "loop") && !strings.Contains(last(c), "after") {
			if len(c.Messages) == 1 {
				return calls("open", `{"toolkit": "house"}`)
			}
			return calls("lights", `{}`)
		}
		return houseModel(c)
	}
	tg.dm(1, "loop forever")
	tg.dm(1, "after")
	w.sync(dir)
	w.until("two answers", func() bool { return len(tg.texts("1")) == 2 })
	if got := tg.texts("1"); got[0] != "That could not be answered." || got[1] != "ok: after" {
		t.Errorf("alice was told %q", got)
	}
}

// An approval answered three days later: the chat has ended by its quiet
// timer meanwhile, the reply is still said, and alice's next message starts a
// new chat whose conversation carries on.
func TestApprovalOutlivesTheChat(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	chat := keyed(w, ".rook/blocks/chat.yaml")["telegram:1"][0].ID
	w.set(w.now().Add(3 * 24 * time.Hour))
	w.until("the chat ended", func() bool { return w.show(chat).HasEnded() })
	// Three days at once may also time out the listener's job, which then
	// rests a minute before it asks Telegram again: the clock moves on until
	// the tap is heard.
	tg.tapOn(t, 0, 1, "yes")
	w.until(`"tell gave" said in chat 1`, func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return slices.ContainsFunc(tg.texts("1"), func(s string) bool { return strings.Contains(s, "tell gave") })
	})
	tg.dm(1, "thanks")
	tg.waitFor(w, "1", "ok: thanks")
	if n := len(model.Calls[len(model.Calls)-1].Messages); n < 5 {
		t.Errorf("the conversation did not carry on: %d messages", n)
	}
	if n := len(keyed(w, ".rook/blocks/conversation.yaml")["telegram:1:helper"]); n != 1 {
		t.Errorf("%d conversations", n)
	}
}
