package rook_test

// Chats on Telegram, run against the stand-in for Telegram's Bot API: people,
// rules.yaml, agents with and without a model, approvals by a tap, attend,
// review, files, failures, and the pages' examples.

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

// scenarioPeople: alice and bob in the team, carol in none, and a stranger, 9.
const scenarioPeople = `
	people:
	  alice: { telegram: "1" }
	  bob: { telegram: "2" }
	  carol: { telegram: "3" }
	groups:
	  team: [alice, bob]
`

// chatProject makes a Telegram project reaching the stand-in, with the
// scenario's people, and the files given (dedented), and sets the token.
func chatProject(t *testing.T, w *world, tg *telegram, files map[string]string) string {
	t.Helper()
	w.secret("TELEGRAM_TOKEN", "123:abc")
	dir := pointed(t, "telegram", tg.Server, "api.telegram.org")
	write(t, dir, map[string]string{"people.yaml": scenarioPeople})
	write(t, dir, files)
	return dir
}

// echoFlow is an agent that is a flow with no model: it replies with who asked
// and what, and counts the turns of its conversation.
const echoFlow = `
	flow: NAME
	input: { said: string, asker: string, conversation: "any?" }
	stages:
	  turn: { job: { script: ./turn.js, input: { name: NAME, said: "${input.said}", asker: "${input.asker}", conversation: "${input.conversation}" } } }
	output: ${stages.turn.output}
`

const turnJS = `return { reply: input.name + " to " + input.asker + " (" + ((input.conversation || 0) + 1) + "): " + input.said, conversation: (input.conversation || 0) + 1 }`

func echoAgent(name string) string { return strings.ReplaceAll(echoFlow, "NAME", name) }

// modelAgent is an agent with a stand-in model at an address, which opens the
// parts given.
func modelAgent(name, address, extra string) string {
	return "agent: " + name + "\n" +
		"include: .rook/agents/base.yaml\n" +
		"model: { name: test-model, service: anthropic, address: " + address + ", secret: TEST_KEY }\n" +
		"system: |\n  You help.\n\n  ${input.format}\n" + extra
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

// teamRules gives alice helper, and the rest of the team team.
const teamRules = `
	places:
	  dm: { readers: [asker] }
	agents:
	  - { name: helper, for: [alice] }
	  - { name: team, for: [team] }
`

// Three people write at once in their own chats: each gets the agent rules.yaml
// gives them, in a conversation of their own, and carol, in no group, is
// told the bot does not answer her here.
func TestSeveralPeopleGetTheirOwnAgents(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": echoAgent("helper"),
		"agents/team.yaml":   echoAgent("team"),
		"agents/turn.js":     turnJS,
		"rules.yaml":         teamRules,
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

// A message in a Telegram group is not heard, even from someone with an agent
// and even when it names the bot: Telegram's listener hears only direct
// messages, as rook-design says. Nothing is said in the group, and no chat run
// starts for it.
func TestGroupMessagesAreNotHeard(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": echoAgent("helper"),
		"agents/turn.js":     turnJS,
		"rules.yaml": `
			places:
			  dm: { readers: [asker] }
			agents:
			  - { name: helper, for: [anyone] }
		`,
	})
	tg.group(-100, 1, "@rook_bot hello from the group")
	tg.group(-100, 2, "and me")
	tg.dm(1, "direct")
	w.sync(dir)
	tg.waitFor(w, "1", "direct")
	quiet()
	if got := tg.texts("-100"); len(got) != 0 {
		t.Errorf("the bot said in the group %q", got)
	}
	for key := range keyed(w, ".rook/blocks/chat.yaml") {
		if key != "telegram:1" {
			t.Errorf("a chat %s", key)
		}
	}
}

// rules.yaml chooses by person and group: the first entry that holds the asker.
// An entry for anyone after named ones catches everyone else, the stranger
// included, who is named by bridge and id.
func TestRulesChooseByPersonThenAnyone(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": echoAgent("helper"),
		"agents/team.yaml":   echoAgent("team"),
		"agents/public.yaml": echoAgent("public"),
		"agents/turn.js":     turnJS,
		"rules.yaml": `
			places:
			  dm: { readers: [asker] }
			agents:
			  - { name: helper, for: [alice] }
			  - { name: team, for: [team] }
			  - { name: public, for: [anyone] }
		`,
	})
	tg.dm(1, "x")
	tg.dm(2, "x")
	tg.dm(3, "x")
	tg.dm(9, "x")
	w.sync(dir)
	for chat, want := range map[string]string{"1": "helper to alice (1): x", "2": "team to bob (1): x", "3": "public to carol (1): x", "9": "public to telegram user 9 (1): x"} {
		tg.waitFor(w, chat, want)
	}
	// The chat's record says which entry chose the agent.
	rules := map[string]string{}
	for _, run := range stubs(t, w, "route") {
		out, _ := w.show(run.ID).Output.(map[string]any)
		rules[fmt.Sprint(out["agent"])] = fmt.Sprint(out["rule"])
	}
	if rules["public"] != "map[for:[anyone] name:public]" || rules["team"] != "map[for:[team] name:team]" {
		t.Errorf("the routes name the rules %v", rules)
	}
}

// To choose by words, a project writes its own agent: a flow at agents/router
// that calls another agent by what was said. rook-project says a machine of your
// own at agents/<name> answers a message with its own steps.
func TestRouterAgentChoosesByWords(t *testing.T) {
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

// An entry of rules.yaml written without for: fails the route, which names the
// entry, and the person is told the message could not be answered.
func TestRuleWithoutForTellsSomeone(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": echoAgent("helper"),
		"agents/turn.js":     turnJS,
		"rules.yaml": `
			places:
			  dm: { readers: [asker] }
			agents:
			  - { name: helper }
		`,
	})
	tg.dm(1, "hello")
	w.sync(dir)
	tg.waitFor(w, "1", "That could not be answered. Ask again in a while.")
	if f := failures(w); !slices.ContainsFunc(f, func(s string) bool { return strings.Contains(s, "rules.yaml: the agent helper has no for: list") }) {
		t.Errorf("no failure names the entry: %q", f)
	}
}

// rules.yaml names an agent that has no file. The message fails, and the
// person is told it could not be answered.
func TestRuleNamesAMissingAgent(t *testing.T) {
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
	tg.waitFor(w, "1", "That could not be answered. Ask again in a while.")
}

// houseParts is a part with a read tool, lights, and an act tool, tell, which
// waits for alice's yes; with review set, the ask asks review first and leaves
// tell to it alone.
func housePart(review string) string {
	return `
		does: The house, and messages to its people.
		ask: { machine: .rook/blocks/approval.yaml, input: { part: house, people: [alice], request: "${input.request}"` + review + ` } }
		tools:
		  lights:
		    does: Which lights are on.
		    args: {}
		    job: { script: ./lights.js }
		  tell:
		    does: Say a text to a person, by their name in people.yaml.
		    args: { person: string, text: string }
		    job: { machine: .rook/blocks/tell.yaml, input: { person: "${args.person}", text: "${args.text}" }, approve: true }
	`
}

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
			return calls("open", `{"part": "house"}`)
		case 1:
			return calls("lights", `{}`)
		}
		return llmstandin.Reply{Text: "lights said " + result}
	case words("twice"):
		switch step {
		case 0:
			return calls("open", `{"part": "house"}`)
		case 1:
			return llmstandin.Reply{Calls: []llmstandin.ToolCall{
				{ID: "x1", Name: "tell", Args: `{"person": "bob", "text": "first"}`},
				{ID: "x2", Name: "tell", Args: `{"person": "carol", "text": "second"}`},
			}}
		}
		return llmstandin.Reply{Text: "twice gave " + result}
	case words("tell"):
		switch step {
		case 0:
			return calls("open", `{"part": "house"}`)
		case 1:
			return calls("tell", `{"person": "bob", "text": "hello from alice"}`)
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

// houseProject makes a project whose helper, for alice, has the house part and
// asks the stand-in model.
func houseProject(t *testing.T, w *world, tg *telegram, review string, extra map[string]string) (string, *llmstandin.Server) {
	t.Helper()
	model, address := standin(t, w, houseModel)
	files := map[string]string{
		"agents/helper.yaml":    modelAgent("helper", address, "open:\n  house: { include: parts/house/part.yaml }\n"),
		"parts/house/part.yaml": housePart(review),
		"parts/house/lights.js": `return { on: ["kitchen", "hall"] }`,
		"rules.yaml":            teamRules,
		"agents/team.yaml":      echoAgent("team"),
		"agents/turn.js":        turnJS,
	}
	for k, v := range extra {
		files[k] = v
	}
	dir := chatProject(t, w, tg, files)
	return dir, model
}

// A read tool runs with no one asked, and the model's reply carries its result.
func TestModelReadToolRunsAtOnce(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, "", nil)
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
	dir, _ := houseProject(t, w, tg, "", nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	b := tg.buttons()
	if b[0][0] != "1" || !strings.HasSuffix(b[0][1], " yes") || !strings.HasSuffix(b[1][1], " no") {
		t.Errorf("the buttons are %v", b)
	}
	question := tg.texts("1")[0]
	for _, want := range []string{"An agent wants to run tell.", "hello from alice", "alice: tell bob hello"} {
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
	dir, model := houseProject(t, w, tg, "", nil)
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

// Bob, who is not the part's approver, and a stranger tap Yes: neither counts,
// the question stays open, and then alice's No counts.
func TestOnlyTheApproversTapCounts(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, "", nil)
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
	tg.tapOn(t, 0, 1, "no")
	tg.waitFor(w, "1", "tell gave")
}

// A question nobody answers gives no after seven days: the message shows that
// no one answered, the tool does not run, and alice gets a reply.
func TestApprovalTimesOut(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, "", nil)
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

// Two held calls in one turn are one question, and one Yes runs both.
func TestTwoHeldCallsAreOneQuestion(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, "", nil)
	tg.dm(1, "twice please")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) >= 2 })
	quiet()
	if n := len(tg.buttons()); n != 2 {
		t.Fatalf("%d buttons were sent", n)
	}
	if q := tg.texts("1")[0]; !strings.Contains(q, "first") || !strings.Contains(q, "second") {
		t.Errorf("the question %q does not show both calls", q)
	}
	tg.tapOn(t, 0, 1, "yes")
	tg.waitFor(w, "2", "first")
	tg.waitFor(w, "3", "second")
	tg.waitFor(w, "1", "twice gave")
}

// A tap whose choice is not one of the buttons (Telegram's callback data is
// whatever the client sends) does not count, and the question waits for one
// that does.
func TestForgedChoiceApprovesNothing(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, "", nil)
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
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, "", nil)
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
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, "", nil)
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

// A text document sent with a caption is downloaded and given to the agent;
// the model is shown its handle, not its key.
func TestDocumentIsGivenToTheAgent(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	_, address := standin(t, w, fileModel)
	dir := chatProject(t, w, tg, map[string]string{"agents/helper.yaml": modelAgent("helper", address, "")})
	tg.files["D1"] = "line one\nline two\n"
	tg.dm(1, "", map[string]any{"caption": "read this", "document": map[string]any{"file_id": "D1", "file_unique_id": "u", "file_name": "notes.txt", "mime_type": "text/plain", "file_size": 18}})
	w.sync(dir)
	w.until("the answer", func() bool { return len(tg.texts("1")) == 1 })
	got := tg.texts("1")[0]
	for _, want := range []string{"read this", "notes.txt", "text/plain"} {
		if !strings.Contains(got, want) {
			t.Errorf("the model was not shown %q: %s", want, got)
		}
	}
	if !slices.Equal(tg.got, []string{"D1"}) {
		t.Errorf("getFile was asked for %v", tg.got)
	}
	w.titled(map[string][]string{"message": {"read this"}, "helper": {"alice: read this"}})
}

// A voice message with no caption is a message, titled by its file's name.
func TestVoiceWithNoText(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	_, address := standin(t, w, fileModel)
	dir := chatProject(t, w, tg, map[string]string{"agents/helper.yaml": modelAgent("helper", address, "")})
	tg.files["V1"] = "OggS..."
	tg.dm(1, "", map[string]any{"voice": map[string]any{"file_id": "V1", "file_unique_id": "u", "mime_type": "audio/ogg", "duration": 2, "file_size": 7}})
	w.sync(dir)
	w.until("the answer", func() bool { return len(tg.texts("1")) == 1 })
	if got := tg.texts("1")[0]; !strings.Contains(got, "voice") {
		t.Errorf("the model was shown %s", got)
	}
	w.titled(map[string][]string{"message": {"voice"}, "chat": {"dm: voice"}})
}

// A file Telegram cannot give (getFile works, the download is 404) is given to
// the agent as the message holds it, with no contents, and the message is
// answered.
func TestBrokenDownloadIsAnswered(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	_, address := standin(t, w, fileModel)
	dir := chatProject(t, w, tg, map[string]string{"agents/helper.yaml": modelAgent("helper", address, "")})
	tg.dm(1, "", map[string]any{"caption": "this one", "document": map[string]any{"file_id": "GONE", "file_unique_id": "u", "file_name": "gone.pdf", "mime_type": "application/pdf", "file_size": 10}})
	w.sync(dir)
	w.until("the answer", func() bool { return len(tg.texts("1")) == 1 })
	if got := tg.texts("1")[0]; !strings.Contains(got, "gone.pdf") || strings.Contains(got, "could not") {
		t.Errorf("alice was told %s", got)
	}
}

// A sticker, an edited message and a message from a bot are not messages: none
// is answered, and the text after them is.
func TestNonMessagesAreSkipped(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": echoAgent("helper"),
		"agents/turn.js":     turnJS,
	})
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

// Telegram keeps no history a bot can read: the agent is given no history and
// no earlier files. A photo sent in one message is still in the conversation
// for the next, since the conversation carries what the model was shown.
func TestEarlierPhotoOnTelegram(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	model, address := standin(t, w, fileModel)
	dir := chatProject(t, w, tg, map[string]string{"agents/helper.yaml": modelAgent("helper", address, "")})
	cat := "\xff\xd8 a cat"
	tg.files["P1"] = cat
	tg.dm(1, "", map[string]any{"caption": "look", "photo": photo("P1", len(cat))})
	tg.dm(1, "what was in the photo I sent?")
	w.sync(dir)
	w.until("two answers", func() bool { return len(tg.texts("1")) == 2 })
	for _, run := range w.at("agents/helper.yaml") {
		in := w.show(run.ID).Input.(map[string]any)
		if in["history"] != nil || (in["earlier"] != nil && len(in["earlier"].([]any)) > 0) {
			t.Errorf("the agent was given history %v and earlier %v", in["history"], in["earlier"])
		}
	}
	all, _ := json.Marshal(model.Calls[1].Messages)
	if !strings.Contains(string(all), "image/jpeg") {
		t.Errorf("the second call does not carry the photo: %s", all)
	}
}

// The tool look on Telegram has no earlier files to look at: a model that
// calls it with an id gets nothing, and the message is answered.
func TestLookOnTelegramGivesNothing(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	_, address := standin(t, w, func(c llmstandin.Call) llmstandin.Reply {
		if _, r := turnOf(c); len(r) == 0 {
			return calls("look", `{"id": "P1"}`)
		}
		return fileModel(c)
	})
	dir := chatProject(t, w, tg, map[string]string{"agents/helper.yaml": modelAgent("helper", address, "")})
	tg.dm(1, "the photo above")
	w.sync(dir)
	w.until("the answer", func() bool { return len(tg.texts("1")) == 1 })
	if len(tg.got) != 0 {
		t.Errorf("look downloaded %v", tg.got)
	}
}

// An agent sends a file and a reply: the file goes to the chat as a document,
// before the reply.
func TestAgentSendsAFileThenReplies(t *testing.T) {
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
	for name, attend := range map[string]string{
		"fails":       "flow: attend\ninput: { said: string }\nstages:\n  x: { job: { script: ./judge.js } }\n",
		"plain true":  "flow: attend\ninput: { said: string }\nstages:\n  x: { job: { script: ./ok.js } }\noutput: { reply: true }\n",
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

// reviewSays is review.yaml as a flow with no model that says one word.
func reviewSays(word string) string {
	return "flow: review\ninput: { context: string }\nstages:\n  x: { job: { script: ./ok.js } }\noutput: { reply: { says: " + word + ", note: because } }\n"
}

// A part asks for a review, and the project has no review.yaml: alice is
// asked, shown that the review failed, and the error does not say more than it
// should.
func TestReviewMissing(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, ", review: true, alone: [tell]", nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	q := tg.texts("1")[0]
	if !strings.Contains(q, "Review failed: ") {
		t.Errorf("alice was asked %q", q)
	}
	tg.tapOn(t, 0, 1, "yes")
	tg.waitFor(w, "2", "hello from alice")
}

// review.yaml that gives no reply, or a word it does not know, is taken as
// refer: alice is asked, and her yes runs the held call.
func TestReviewMisspeaks(t *testing.T) {
	for name, review := range map[string]string{
		"no reply":     "flow: review\ninput: { context: string }\nstages:\n  x: { job: { script: ./ok.js } }\noutput: { verdict: yes }\n",
		"unknown word": reviewSays("maybe"),
	} {
		t.Run(name, func(t *testing.T) {
			tg := newTelegram(t)
			w := newWorld(t)
			dir, model := houseProject(t, w, tg, ", review: true, alone: [tell]", map[string]string{"review.yaml": review, "ok.js": "return 1"})
			tg.dm(1, "tell bob hello")
			w.sync(dir)
			w.until("a question or a reply", func() bool { return len(tg.texts("1")) > 0 })
			if len(tg.buttons()) == 0 {
				t.Errorf("review that gives %s: alice was not asked, and the model was told %s", name, last(model.Calls[len(model.Calls)-1]))
				return
			}
			if q := tg.texts("1")[0]; name == "no reply" && !strings.Contains(q, "Review failed: it gave no reply") {
				t.Errorf("alice was asked %q", q)
			}
			tg.tapOn(t, 0, 1, "yes")
			tg.waitFor(w, "2", "hello from alice")
		})
	}
}

// review.yaml as the page shows it, an agent that includes
// .rook/review/agent.yaml, on the stand-in model: it is shown the held call and
// the request, and its refuse ends the call.
func TestReviewAgentWithAModel(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	reviewer := &llmstandin.Server{Answer: func(c llmstandin.Call) llmstandin.Reply {
		return llmstandin.Reply{Text: `{"says": "refuse", "note": "Not what was asked."}`}
	}}
	web := httptest.NewServer(reviewer)
	t.Cleanup(web.Close)
	dir, model := houseProject(t, w, tg, ", review: true, alone: [tell]", map[string]string{"review.yaml": `
		agent: review
		include: .rook/review/agent.yaml
		model: { name: test-model, service: anthropic, address: ` + web.URL + `, secret: TEST_KEY }
	`})
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	tg.waitFor(w, "1", "tell gave")
	if got := last(model.Calls[len(model.Calls)-1]); !strings.Contains(got, "The review said refuse: Not what was asked.") {
		t.Errorf("the model was told %s", got)
	}
	shown := fmt.Sprint(reviewer.Calls[0].Messages)
	for _, want := range []string{"Tool: tell", "hello from alice", "alice: tell bob hello", "Your yes ends it"} {
		if !strings.Contains(shown, want) {
			t.Errorf("the review was not shown %q: %s", want, shown)
		}
	}
}

// The part's approver has no Telegram or Slack id, or is not in people.yaml at
// all: the question fails, the held call is denied, and bob is not told.
func TestApproverCannotBeAsked(t *testing.T) {
	for name, people := range map[string]string{
		"no direct bridge": "people:\n  alice: { telegram: \"1\" }\n  bob: { telegram: \"2\" }\n  dana: { gitlab: dana }\ngroups: {}\n",
		"not in people":    "people:\n  alice: { telegram: \"1\" }\n  bob: { telegram: \"2\" }\ngroups: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			tg := newTelegram(t)
			w := newWorld(t)
			dir, model := houseProject(t, w, tg, "", nil)
			setSite(t, dir, "parts/house/part.yaml", "people: [alice]", "people: [dana]")
			write(t, dir, map[string]string{"people.yaml": people})
			tg.dm(1, "tell bob hello")
			w.sync(dir)
			w.until("a reply", func() bool { return len(tg.texts("1")) > 0 })
			if got := last(model.Calls[len(model.Calls)-1]); !strings.Contains(got, "approves nothing") {
				t.Errorf("the model was told %s", got)
			}
			if len(tg.texts("2")) != 0 {
				t.Errorf("bob was told something with no approval")
			}
		})
	}
}

// An approver group that holds alice: she is asked, and her tap counts as the
// group's.
func TestApproverGroup(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, "", nil)
	setSite(t, dir, "parts/house/part.yaml", "people: [alice]", "people: [team]")
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("questions to both", func() bool { return len(tg.buttons()) == 4 })
	// bob answers, in his own chat: the team's answer.
	tg.tapOn(t, 0, 2, "yes")
	tg.waitFor(w, "2", "hello from alice")
	tg.waitFor(w, "1", "tell gave")
	w.until("both questions closed", func() bool { return len(tg.edited()) == 2 })
	for _, e := range tg.edited() {
		if !strings.HasSuffix(e, "bob said yes.") {
			t.Errorf("a question shows %q", e)
		}
	}
}

// The model fails (status 500): alice is told it could not be answered, and her
// next message starts afresh and is answered.
func TestModelFails(t *testing.T) {
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
	if got[0] != "That could not be answered. Ask again in a while." || !strings.Contains(got[1], "fine") {
		t.Errorf("alice was told %q", got)
	}
}

// The model's key is not set: alice is told it could not be answered, and
// the error names the secret, not its value.
func TestModelKeyMissing(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	model := &llmstandin.Server{Answer: fileModel}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	dir := chatProject(t, w, tg, map[string]string{"agents/helper.yaml": modelAgent("helper", web.URL, "")})
	tg.dm(1, "hello")
	w.sync(dir)
	tg.waitFor(w, "1", "That could not be answered. Ask again in a while.")
	for _, r := range w.at("agents/helper.yaml") {
		if e := w.show(r.ID).Error; !strings.Contains(e, "TEST_KEY") {
			t.Errorf("the agent failed with %q", e)
		}
	}
}

// A long reply is cut into Telegram's 4096 characters, at a line's end, the
// first part answering the message; a chart is written as a table.
func TestLongReplyAndChart(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	long := strings.Repeat("a line of the reply that is long enough\n", 150)
	chart := "Here.\n\n```chart\n{\"type\": \"bar\", \"title\": \"Tickets\", \"labels\": [\"Jan\", \"Feb\"], \"series\": [{\"name\": \"Email\", \"values\": [140, 165]}]}\n```\n"
	_, address := standin(t, w, func(c llmstandin.Call) llmstandin.Reply {
		if strings.Contains(last(c), "chart") {
			return llmstandin.Reply{Text: chart}
		}
		return llmstandin.Reply{Text: long}
	})
	dir := chatProject(t, w, tg, map[string]string{"agents/helper.yaml": modelAgent("helper", address, "")})
	tg.dm(1, "long")
	tg.dm(1, "chart")
	w.sync(dir)
	w.until("three messages", func() bool { return len(tg.texts("1")) == 3 })
	got := tg.texts("1")
	if len(got[0]) > 4096 || !strings.HasSuffix(got[0], "\n") || got[0]+got[1] != strings.TrimSpace(long) {
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
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": echoAgent("helper"),
		"agents/turn.js":     turnJS,
	})
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

// The tool progress on Telegram shows "typing", and not the model's text, as
// its does: tells the model.
func TestProgressOnTelegram(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	_, address := standin(t, w, func(c llmstandin.Call) llmstandin.Reply {
		if _, r := turnOf(c); len(r) == 0 {
			return calls("progress", `{"text": "Checking the boiler"}`)
		}
		return llmstandin.Reply{Text: "done"}
	})
	dir := chatProject(t, w, tg, map[string]string{"agents/helper.yaml": modelAgent("helper", address, "")})
	tg.dm(1, "check")
	w.sync(dir)
	tg.waitFor(w, "1", "done")
	if got := tg.said(); slices.ContainsFunc(got, func(s string) bool { return strings.Contains(s, "boiler") }) {
		t.Errorf("the progress text was said: %q", got)
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
	tg := newTelegram(t)
	w := newWorld(t)
	model, address := standin(t, w, func(c llmstandin.Call) llmstandin.Reply {
		said, results := turnOf(c)
		_ = said
		switch len(results) {
		case 0:
			return calls("open", `{"part": "messages"}`)
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

// The approval's in: names a Slack channel, in a project that runs only
// Telegram: the question fails, and the held call is denied.
func TestApprovalInSlackChannelWithoutSlack(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, `, in: "#ops"`, nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a reply", func() bool { return len(tg.texts("1")) > 0 })
	if got := last(model.Calls[len(model.Calls)-1]); !strings.Contains(got, "approves nothing") {
		t.Errorf("the model was told %s", got)
	}
	if len(tg.texts("2")) != 0 {
		t.Errorf("bob was told")
	}
}

// A held tell to someone not in people.yaml is approved, then fails: the model
// is told why, and alice gets a reply.
func TestApprovedToolFails(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, "", nil)
	model.Answer = func(c llmstandin.Call) llmstandin.Reply {
		_, results := turnOf(c)
		switch len(results) {
		case 0:
			return calls("open", `{"part": "house"}`)
		case 1:
			return calls("tell", `{"person": "zed", "text": "hi"}`)
		}
		return llmstandin.Reply{Text: "result " + results[len(results)-1]}
	}
	tg.dm(1, "tell zed hi")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	tg.tapOn(t, 0, 1, "yes")
	tg.waitFor(w, "1", "result")
	if got := tg.texts("1")[1]; !strings.Contains(got, "zed") {
		t.Errorf("alice was told %q", got)
	}
}

// Two taps in a row from alice, Yes then No: the first is the answer; the
// second is acknowledged and changes nothing.
func TestDoubleTap(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, "", nil)
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

// Bob, whose agent also has the house part, and alice each ask for a held
// call. alice gets two questions, and answers the second (bob's) first: each
// tap goes to its own question.
func TestTwoQuestionsAnsweredOutOfOrder(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, "", nil)
	_ = model
	helper, err := os.ReadFile(filepath.Join(dir, "agents/helper.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	writeRaw(t, dir, "agents/team.yaml", strings.Replace(string(helper), "agent: helper", "agent: team", 1))
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
	if q := tg.texts("1")[1]; !strings.Contains(q, "bob: please tell bob hello") {
		t.Errorf("alice was shown %q for bob's request", q)
	}
}

// Telegram ids written as numbers in people.yaml, without quotes, as a person
// copying them would: alice is still known, asked and her tap counts.
func TestUnquotedTelegramIds(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, "", nil)
	write(t, dir, map[string]string{"people.yaml": "people:\n  alice: { telegram: 1 }\n  bob: { telegram: 2 }\ngroups: {}\n"})
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	tg.tapOn(t, 0, 1, "yes")
	tg.waitFor(w, "2", "hello from alice")
}

// A model that never stops calling a tool reaches the agent's limit of turns:
// alice is told it could not be answered, and the next message is answered.
func TestAgentRunsOutOfTurns(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, "", nil)
	model.Answer = func(c llmstandin.Call) llmstandin.Reply {
		first, _ := json.Marshal(c.Messages[0])
		if strings.Contains(string(first), "loop") && !strings.Contains(last(c), "after") {
			if len(c.Messages) == 1 {
				return calls("open", `{"part": "house"}`)
			}
			return calls("lights", `{}`)
		}
		return houseModel(c)
	}
	tg.dm(1, "loop forever")
	tg.dm(1, "after")
	w.sync(dir)
	w.until("two answers", func() bool { return len(tg.texts("1")) == 2 })
	if got := tg.texts("1"); got[0] != "That could not be answered. Ask again in a while." || got[1] != "ok: after" {
		t.Errorf("alice was told %q", got)
	}
}

// An approval answered three days later: the chat has ended by its quiet
// timer meanwhile, the reply is still said, and alice's next message starts a
// new chat whose conversation carries on.
func TestApprovalOutlivesTheChat(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, "", nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	chat := keyed(w, ".rook/blocks/chat.yaml")["telegram:1"][0].ID
	w.set(w.now().Add(3 * 24 * time.Hour))
	w.until("the chat ended", func() bool { return w.show(chat).HasEnded() })
	tg.tapOn(t, 0, 1, "yes")
	tg.waitFor(w, "1", "tell gave")
	tg.dm(1, "thanks")
	tg.waitFor(w, "1", "ok: thanks")
	if n := len(model.Calls[len(model.Calls)-1].Messages); n < 5 {
		t.Errorf("the conversation did not carry on: %d messages", n)
	}
	if n := len(keyed(w, ".rook/blocks/conversation.yaml")["telegram:1:helper"]); n != 1 {
		t.Errorf("%d conversations", n)
	}
}

// people.yaml without its groups: line, as a person with no groups writes it:
// alice is answered and the stranger refused.
func TestPeopleWithoutGroups(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": echoAgent("helper"),
		"agents/turn.js":     turnJS,
		"people.yaml":        "people:\n  alice: { telegram: \"1\" }\n",
	})
	tg.dm(1, "hello")
	tg.dm(9, "hello")
	w.sync(dir)
	w.until("both routes ended", func() bool {
		n := 0
		for _, r := range stubs(t, w, "route") {
			if w.show(r.ID).HasEnded() {
				n++
			}
		}
		return n == 2
	})
	quiet()
	if a, s := tg.texts("1"), tg.texts("9"); !slices.Equal(a, []string{"helper to alice (1): hello"}) || !slices.Equal(s, []string{"This bot answers only the people it belongs to."}) {
		t.Errorf("with no groups: in people.yaml alice was told %q and the stranger %q; failures: %q", a, s, failedRuns(t, w))
	}
}
