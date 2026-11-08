package rook_test

// Chats on Telegram, run against the stand-in for Telegram's Bot API: people,
// agents with and without a model, approvals by a tap, filters, files,
// failures, and the pages' examples.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
	input: { messages: list, medium: string, format: "any?", asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
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
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/assistant.yaml": echoAgent("helper"),
		"agents/turn.js":        turnJS,
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

// Three people write at once in their own chats: each is answered in a
// conversation of their own, which counts only their own turns.
func TestSeveralPeopleHaveTheirOwnConversations(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/assistant.yaml": echoAgent("helper"),
		"agents/turn.js":        turnJS,
	})
	tg.dm(1, "a1")
	tg.dm(2, "b1")
	tg.dm(3, "c1")
	w.sync(dir)
	tg.waitFor(w, "1", "helper to alice (1): a1")
	tg.waitFor(w, "2", "helper to bob (1): b1")
	tg.waitFor(w, "3", "helper to carol (1): c1")
	tg.dm(1, "a2")
	tg.waitFor(w, "1", "helper to alice (2): a2")
	keys := keyed(w, "conversation.yaml")
	if len(keys) != 3 || len(keys["telegram:1:1:assistant"]) != 1 || len(keys["telegram:2:2:assistant"]) != 1 || len(keys["telegram:3:3:assistant"]) != 1 {
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

// houseToolkit is a toolkit with a read tool, lights, and an act tool, tell,
// which waits for a yes from someone with house:approve in alice's chat. It
// names no asker, so alice may approve what she asked for.
const houseToolkit = `
	description: The house, and messages to its people.
	ask:
	  machine: .rook/approval.yaml
	  input: { ask: [{ at: gromit/telegram/chat/ask, in: "1" }], approvers: "house:approve", request: "${input.messages.map((m) => m.text).join('\\n')}" }
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

// turnOf splits a call into what the person last said, without the thread
// the prompt gives after it, and the tool results that came after it, each as
// JSON.
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
		said, _, _ = strings.Cut(s, "The thread so far")
		return said, results
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
		"toolkits/house.yaml": strings.Replace(houseToolkit, `approvers: "house:approve",`, `approvers: "house:approve", asker: "${input.asker.email}",`, 1),
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

// Five quick messages to a model agent are answered in turns, which are
// given the five in the order sent; each turn's reply answers its last
// message, and each model call carries the conversation before it.
func TestQuickMessagesAnsweredInOrder(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, nil)
	for i := 1; i <= 5; i++ {
		tg.dm(1, fmt.Sprintf("m%d", i))
	}
	w.sync(dir)
	var given []string
	var turns []*client.Run
	w.until("five messages answered", func() bool {
		given, turns = nil, agentRuns(w, "1", "1")
		for _, run := range turns {
			given = append(given, w.texts(run)...)
		}
		return len(given) == 5 && len(tg.texts("1")) == len(turns)
	})
	if !slices.Equal(given, []string{"m1", "m2", "m3", "m4", "m5"}) {
		t.Errorf("the turns were given %q", given)
	}
	tg.mu.Lock()
	sent := slices.Clone(tg.sent)
	tg.mu.Unlock()
	for i, run := range turns {
		texts := w.texts(run)
		last := 9 + slices.Index([]string{"m1", "m2", "m3", "m4", "m5"}, texts[len(texts)-1]) + 1
		if rp, _ := sent[i]["reply_parameters"].(map[string]any); rp == nil || rp["message_id"] != float64(last) {
			t.Errorf("reply %d answers %v, not message %d", i, sent[i]["reply_parameters"], last)
		}
	}
	if n := len(model.Calls[len(model.Calls)-1].Messages); n != 2*len(turns)-1 {
		t.Errorf("the last call carried %d messages, and not the %d turns before it", n, len(turns)-1)
	}
}

// Bob writes while alice's approval waits: he has his own conversation, and is
// answered at once.
func TestOthersAreAnsweredWhileAnApprovalWaits(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := houseProject(t, w, tg, nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	tg.dm(2, "hi")
	tg.waitFor(w, "2", "ok: hi")
}

// A message sent just as the quiet timer fires, a day after the last, is
// answered: by the old conversation or by a new one, but never lost. The
// clock moves past a day, and the message is sent at once.
func TestMessageAsTheQuietTimerFires(t *testing.T) {
	for round := range 3 {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			w := newWorld(t)
			dir := copyStarter(t)
			removeListener(t, dir)
			w.sync(dir)
			w.sendTo("conversation.yaml", key("1", "1"), "message", said("1", "1", "one"))
			w.until("the first answer, and the conversation waiting", func() bool {
				runs := keyed(w, "conversation.yaml")[key("1", "1")]
				return sayings(t, w)["one"] != nil && len(runs) == 1 && w.show(runs[0].ID).Timers == 1
			})
			// Each round lands the message a little differently against the timer.
			w.set(w.now().Add(24*time.Hour + time.Duration(round)*interval))
			time.Sleep(time.Duration(round) * interval)
			w.sendTo("conversation.yaml", key("1", "1"), "message", said("1", "1", "two"))
			w.until("the second answer", func() bool { return sayings(t, w)["two"] != nil })
		})
	}
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

// The model fails (status 500): alice is told it could not be answered, and her
// next message starts afresh and is answered.
func TestModelFails(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	_, address := standin(t, w, func(c llmstandin.Call) llmstandin.Reply {
		if strings.Contains(last(c), "asks: boom") {
			return llmstandin.Reply{Error: "overloaded"}
		}
		return fileModel(c)
	})
	dir := chatProject(t, w, tg, map[string]string{"agents/assistant.yaml": modelAgent("assistant", address, "")})
	tg.dm(1, "boom")
	w.sync(dir)
	w.until("one answer", func() bool { return len(tg.texts("1")) == 1 })
	tg.dm(1, "fine")
	w.until("two answers", func() bool { return len(tg.texts("1")) == 2 })
	got := tg.texts("1")
	if got[0] != "That could not be answered." || !strings.Contains(got[1], "fine") {
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
	dir := chatProject(t, w, tg, map[string]string{"agents/assistant.yaml": modelAgent("assistant", web.URL, "")})
	tg.dm(1, "hello")
	w.sync(dir)
	tg.waitFor(w, "1", "That could not be answered.")
	for _, r := range w.at("agents/assistant.yaml") {
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
	w.sync(dir)
	w.until("two messages", func() bool { return len(tg.texts("1")) == 2 })
	tg.dm(1, chart)
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

// When Telegram answers "typing" with a 500, the message is still answered:
// the status is not part of the answer.
func TestFailedTypingStillAnswers(t *testing.T) {
	tg := newTelegram(t)
	override(tg.Server, func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasSuffix(r.URL.Path, "/sendChatAction") {
			return false
		}
		w.WriteHeader(500)
		fmt.Fprint(w, "upstream error")
		return true
	})
	w := newWorld(t)
	dir := chatProject(t, w, tg, nil)
	tg.dm(1, "check")
	w.sync(dir)
	tg.waitFor(w, "1", "check")
	if got := tg.texts("1"); !slices.Equal(got, []string{"check"}) {
		t.Errorf("the bot said %q", got)
	}
	for _, f := range failedRuns(t, w) {
		if !strings.HasPrefix(f, "telegram-chat-status:") {
			t.Errorf("a run failed: %s", f)
		}
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

// rook-design's deploys.yaml, with the placed pipeline call and question, as
// written, on Slack and GitLab's stand-ins: bob asks to deploy orders in a
// thread, his own press is refused, alice's yes starts the pipeline, and the
// thread is told it started.
func TestPageDeploys(t *testing.T) {
	s := newSlack(t)
	gl := newGitLab(t)
	w := newWorld(t)
	dir := slackProject(t, w, s)
	point(t, dir, "gitlab", gl.Server)
	w.secret("GITLAB_TOKEN", "gl-token")
	w.value("GITLAB_SITE", strings.TrimPrefix(gl.URL, "http://"))
	for _, path := range []string{"deploys.yaml", "gromit/gitlab/pipeline/create.yaml", "gromit/slack/chat/ask.yaml"} {
		writeRaw(t, dir, path, pageExample(t, "rook-design", path))
	}
	write(t, dir, map[string]string{"people/scopes.yaml": `
		alice@example.com: { name: alice, scopes: ["deploy:approve"] }
		bob@example.com: { name: bob, scopes: ["deploy:approve"] }
	`})
	w.sync(dir)
	w.until("Slack connected", s.connected)
	w.sendTo("deploys.yaml", "C1/4.0", "message", map[string]any{"service": "slack", "place": "#help", "chat": "C1/4.0", "user": "U2", "name": "Bob", "email": "bob@example.com", "text": "deploy orders"})
	w.until("the question", func() bool { return len(s.posted()) == 1 })
	post := s.posted()[0]
	if post["channel"] != "C1" || post["thread_ts"] != "4.0" {
		t.Fatalf("the question went to %v", post)
	}
	yes := value(t, post, "yes")
	s.press("a", "U2", yes)
	w.until("bob told", func() bool { return len(s.whispered()) == 1 })
	if got := s.whispered()[0]["text"]; got != "You asked for this, so someone else must answer it." {
		t.Errorf("bob was told %v", got)
	}
	if len(gl.pipelines) != 0 {
		t.Fatal("the pipeline started before a yes that counts")
	}
	s.press("b", "U1", yes)
	w.until("the thread told", func() bool { return len(s.posted()) == 2 })
	if got := s.posted()[1]; got["channel"] != "C1" || got["thread_ts"] != "4.0" || got["markdown_text"] != "Pipeline started." {
		t.Errorf("the thread was told %v", got)
	}
	if got := gl.pipelines; len(got) != 1 || got[0]["project"] != "group/orders" || got[0]["ref"] != "main" {
		t.Errorf("the pipelines started are %v", got)
	}
}

// rook-design's schedules/evening.yaml, as written but for the chat's ID: at
// 18:00 in London, chat 1 is told the day is over on Telegram.
func TestPageEveningSchedule(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, nil)
	evening := pageExample(t, "rook-design", "schedules/evening.yaml")
	writeRaw(t, dir, "schedules/evening.yaml", strings.Replace(evening, `in: "123456789"`, `in: "1"`, 1))
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

// rook-project's agent, toolkit, its instructions and placed calls, as
// written but for the model's address: alice, with orders:read only, asks on
// Telegram; the model opens orders, is given its instructions, is shown
// read_file without the project it fixes and not change_file, reads the file
// from group/orders, and replies.
func TestPageToolkitAndAgent(t *testing.T) {
	tg := newTelegram(t)
	gl := newGitLab(t)
	w := newWorld(t)
	model, address := standin(t, w, func(c llmstandin.Call) llmstandin.Reply {
		switch _, results := turnOf(c); len(results) {
		case 0:
			return calls("open", `{"toolkit": "orders"}`)
		case 1:
			return calls("read_file", `{"path": "config/app.yaml"}`)
		}
		return llmstandin.Reply{Text: "It is size 2."}
	})
	dir := chatProject(t, w, tg, nil)
	point(t, dir, "gitlab", gl.Server)
	gl.repo["group/orders@HEAD:config/app.yaml"] = "size: 2\n"
	w.secret("GITLAB_TOKEN", "gl-token")
	w.value("GITLAB_SITE", strings.TrimPrefix(gl.URL, "http://"))
	w.secret("ANTHROPIC_KEY", "sk-test")
	for _, path := range []string{"agents/assistant.yaml", "toolkits/orders.yaml", "toolkits/orders.md", "gromit/gitlab/repository/files/read.yaml", "gromit/gitlab/repository/commits/create.yaml", ".corbieignore"} {
		writeRaw(t, dir, path, pageExample(t, "rook-project", path))
	}
	agent := pageExample(t, "rook-project", "agents/assistant.yaml")
	writeRaw(t, dir, "agents/assistant.yaml", strings.Replace(agent, "model: { name: claude-sonnet-5-5, secret: ANTHROPIC_KEY }", "model: { name: claude-sonnet-5-5, service: anthropic, address: "+address+", secret: ANTHROPIC_KEY }", 1))
	write(t, dir, map[string]string{"people/scopes.yaml": `alice@example.com: { name: alice, scopes: ["orders:read"] }`})
	tg.dm(1, "what size is the app?")
	w.sync(dir)
	tg.waitFor(w, "1", "It is size 2.")
	if len(model.Calls) != 3 {
		t.Fatalf("the model was called %d times", len(model.Calls))
	}
	if s := model.Calls[0].System; !strings.Contains(s, "plain text: write no Markdown, and no tables") {
		t.Errorf("the system text does not hold Telegram's format: %s", s)
	}
	if opened := fmt.Sprint(model.Calls[1].Messages); !strings.Contains(opened, "The orders app is built from the repository group/orders.") {
		t.Errorf("open did not give the instructions: %s", opened)
	}
	tools, _ := json.Marshal(model.Calls[1].Tools)
	if !strings.Contains(string(tools), `"name":"read_file"`) || strings.Contains(string(tools), "change_file") || strings.Contains(string(tools), `"project"`) {
		t.Errorf("after open the model is shown %s", tools)
	}
	if read := fmt.Sprint(model.Calls[2].Messages); !strings.Contains(read, "size: 2") {
		t.Errorf("read_file gave the model %s", read)
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
	w.sync(dir)
	w.until("one answer", func() bool { return len(tg.texts("1")) == 1 })
	tg.dm(1, "after")
	w.until("two answers", func() bool { return len(tg.texts("1")) == 2 })
	if got := tg.texts("1"); got[0] != "That could not be answered." || got[1] != "ok: after" {
		t.Errorf("alice was told %q", got)
	}
}

// An approval answered three days later: the conversation waits for its
// turn all the while, the reply is still posted, and alice's next message
// carries the conversation on.
func TestApprovalAnsweredDaysLater(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, nil)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	w.until("a question", func() bool { return len(tg.buttons()) == 2 })
	w.set(w.now().Add(3 * 24 * time.Hour))
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
	w.until(`"ok: thanks" said in chat 1`, func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return slices.Contains(tg.texts("1"), "ok: thanks")
	})
	if n := len(model.Calls[len(model.Calls)-1].Messages); n < 5 {
		t.Errorf("the conversation did not carry on: %d messages", n)
	}
	if n := len(keyed(w, "conversation.yaml")["telegram:1:1:assistant"]); n != 1 {
		t.Errorf("%d conversations", n)
	}
}
