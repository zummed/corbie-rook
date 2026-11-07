package rook_test

import (
	"cmp"
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zummed/corbie/client"
	"github.com/zummed/corbie/instance"
	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// These tests are of what a project's tree gives Rook: who may send to its
// runs, how its runs live through changes, and how a message is followed from
// its bridge to every run it led to.

// message is a message from alice in her chat on Telegram.
func message(text string) map[string]any {
	return map[string]any{"bridge": "telegram", "text": text, "chat": "1", "from": "1", "place": "dm", "answers": "1:10"}
}

// A run of another tree, though its client owns both, can neither send a
// message to a chat nor call an agent of the project: each file accepts only
// its own tree by default.
func TestAnotherTreeCannotReachTheProject(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	w.sync(dir)
	other := filepath.Join(t.TempDir(), "other")
	write(t, other, map[string]string{"poke.yaml": `
		flow: poke
		allow: ["send:/project/**"]
		stages:
		  send: { job: { send: { to: { at: /project/.rook/blocks/chat, key: "telegram:1" }, event: message, data: { bridge: telegram, text: hi, chat: "1", from: "1", place: dm } } } }
		  call: { job: { call: { at: /project/agents/helper }, input: { said: hi, asker: alice, home: x } } }
		  after: { when: "${stages.call.failed}", job: { script: ./echo.js, input: "${stages.call.error}" } }
		output: ${stages.after.output}
	`, "echo.js": "return input"})
	if r := w.syncAt(w.Client, other, "/other"); !r.Taken {
		t.Fatalf("the tree /other was refused: %s", r.Refused)
	}
	id, err := w.Start(context.Background(), client.Start{At: "/other/poke"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := w.Wait(context.Background(), id, interval)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "done" || !strings.Contains(fmt.Sprint(w.show(id).Output), "receive") {
		t.Errorf("the call ended %s with %v: %s", run.Status, w.show(id).Output, run.Error)
	}
	if got := undelivered(w, id); len(got) != 1 || !strings.Contains(got[0], "sent to /project/.rook/blocks/chat#telegram:1 was not delivered") {
		t.Errorf("the sends noted undelivered are %+v", got)
	}
	for _, path := range []string{".rook/blocks/chat.yaml", "agents/helper.yaml"} {
		if runs := w.at(path); len(runs) != 0 {
			t.Errorf("%s has runs %v", path, runs)
		}
	}
}

// Another client, as a gateway's is, cannot call a bridge's say in the
// project, though its ceiling allows everything.
func TestAnotherClientCannotCallABridge(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	w.sync(dir)
	gateway := w.newClient("gateway")
	id, err := gateway.Start(context.Background(), client.Start{At: "/project/.rook/bridges/telegram/say", Input: map[string]any{"to": "1", "text": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := gateway.Wait(context.Background(), id, interval)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || !strings.Contains(run.Error, "receive") {
		t.Errorf("the call ended %s: %s", run.Status, run.Error)
	}
	if runs := w.at(".rook/bridges/telegram/say.yaml"); len(runs) != 0 {
		t.Errorf("say ran: %v", runs)
	}
}

// A tap that names a conversation's run, or a question that has ended, is
// noted as undelivered in the router's record, and reaches no run: the router
// sends a tap only to a question at the bot's chat/ask.
func TestTapReachesOnlyAWaitingQuestion(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
		".rook/telegram/telegram.js": oneMessage,
		"agents/asker.yaml":          asker,
		"people/find.yaml":           telegramFind,
		"people/scopes.yaml":         "bob@example.com: { name: bob, scopes: [house:approve] }\n",
	})
	w.sync(dir)
	w.until("the answer", func() bool { return sayings(t, w)["one"] != nil })
	conversation := keyed(w, "conversation.yaml")["telegram:1:1:assistant"][0].ID
	id := w.start("agents/asker.yaml", nil)
	question := w.question(map[string]bool{}).ID
	w.tap(question, "2", "yes")
	w.until("the question answered", func() bool { return w.show(id).HasEnded() })
	own := func(id string) string { return id[strings.Index(id, "/")+1:] }
	write(t, dir, map[string]string{".rook/telegram/telegram.js": `
		if (input.do === "hear") {
		  if (input.cursor !== null) throw new Error("nothing more")
		  emit("answer", { service: "telegram", run: "` + own(conversation) + `", user: "2", choice: "yes" })
		  emit("answer", { service: "telegram", run: "` + own(question) + `", user: "2", choice: "no" })
		  return { cursor: 1 }
		}
		return {}
	`})
	w.sync(dir)
	router := w.kept(router("telegram"))
	w.until("both taps noted undelivered", func() bool { return len(undelivered(w, router)) == 2 })
	for _, e := range w.record(conversation) {
		for _, ev := range e.Events {
			if ev.Name == "answer" {
				t.Errorf("the conversation took %v", ev)
			}
		}
	}
	if got := w.show(id).Output; got != "yes" {
		t.Errorf("the question gave %v", got)
	}
}

// A secret set at another tree's prefix is not set for the project's runs: a
// job that names it fails before it starts, naming it.
func TestSecretOfAnotherTreeDoesNotReachTheProject(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	w := newWorld(t)
	w.secretAt("/other", "OTHER_TOKEN", "x")
	dir := copyStarter(t)
	removeListener(t, dir)
	write(t, dir, map[string]string{"on/peek.yaml": `
		flow: peek
		stages:
		  peek: { job: { script: ./peek.js, allow: [net:example.com, "secret:OTHER_TOKEN@net:example.com"] } }
	`, "on/peek.js": "return 1"})
	w.sync(dir)
	run, err := w.Wait(context.Background(), w.start("on/peek.yaml", nil), interval)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "failed" || !strings.Contains(run.Error, "OTHER_TOKEN") {
		t.Fatalf("the run ended %s: %s", run.Status, run.Error)
	}
}

// A chat quiet for a day ends, and so does its conversation; the next message
// to the chat starts a new one, whose input is that message.
func TestQuietChatEndsAndTheNextMessageStartsAnother(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	w.sync(dir)
	w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", message("one"))
	w.until("the chat and its conversation waiting", func() bool {
		chats, conversations := keyed(w, ".rook/blocks/chat.yaml")["telegram:1"], keyed(w, ".rook/blocks/conversation.yaml")["telegram:1:helper"]
		if len(chats) != 1 || len(conversations) != 1 || sayings(t, w)["one"] == nil {
			return false
		}
		answered, err := w.List(context.Background(), client.Filter{Parent: conversations[0].ID, Status: "done", Finished: true})
		return err == nil && len(answered) == 1 && w.show(chats[0].ID).Timers == 1 && w.show(conversations[0].ID).Timers == 1
	})
	first := keyed(w, ".rook/blocks/chat.yaml")["telegram:1"][0].ID
	w.set(w.now().Add(25 * time.Hour))
	w.until("the chat and its conversation ended", func() bool {
		return w.show(first).Status == "done" && keyed(w, ".rook/blocks/conversation.yaml")["telegram:1:helper"][0].Status == "done"
	})
	w.sendTo(".rook/blocks/chat.yaml", "telegram:1", "message", message("two"))
	w.until("the second answer", func() bool { return sayings(t, w)["two"] != nil })
	chats := keyed(w, ".rook/blocks/chat.yaml")["telegram:1"]
	if len(chats) != 2 {
		t.Fatalf("the chats are %v", chats)
	}
	for _, run := range chats {
		if run.ID != first && fmt.Sprint(w.show(run.ID).Input.(map[string]any)["text"]) != "two" {
			t.Errorf("the new chat's input is %v", w.show(run.ID).Input)
		}
	}
	for _, run := range w.at("agents/helper.yaml") {
		if in := w.show(run.ID).Input.(map[string]any); in["said"] == "two" && in["conversation"] != nil {
			t.Errorf("the new conversation carried on the old: %v", in)
		}
	}
}

// A changed conversation.yaml lets a conversation under way finish: an
// approval it waits for still reaches its end, and the reply is said.
func TestChangedConversationFinishesWhatItDoes(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	w := newWorld(t)
	dir, _ := approving(t, w, oneMessage)
	w.sync(dir)
	question := w.question(map[string]bool{})
	conversation := keyed(w, ".rook/blocks/conversation.yaml")["telegram:1:helper"][0].ID
	setSite(t, dir, ".rook/blocks/conversation.yaml", "lifetime: keyed\n", "lifetime: keyed\nmeta: { changed: true }\n")
	w.sync(dir)
	w.tap(question.ID, "1", "yes")
	w.until("the reply said", func() bool { return sayings(t, w)["told"] != nil })
	if run := w.show(conversation); run.Status != "running" {
		t.Errorf("the conversation is %s", run.Status)
	}
}

// A message can be followed from its bridge's listener to every run it led to:
// the chat, the conversation, the agent, its tools and the question its
// approval asked. Each run names the run that sent to it or called it, and each
// record names the run it sent to or called. The chat's record says which entry
// of rules.yaml chose the agent, and the approver is shown the conversation's
// last messages.
func TestAMessageCanBeFollowed(t *testing.T) {
	t.Skip("R7 revives this: conversation, agent and starter")
	w := newWorld(t)
	w.secret("TEST_KEY", "sk-test")
	dir := copyStarter(t)
	replies := []llmstandin.Reply{
		{Text: "noted"},
		{Calls: []llmstandin.ToolCall{{ID: "c1", Name: "open", Args: `{"toolkit": "messages"}`}}},
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
	write(t, dir, map[string]string{".rook/telegram/telegram.js": `
		if (input.do === "hear") {
		  if (input.cursor !== null) throw new Error("nothing more")
		  emit("message", { bridge: "telegram", text: "the plan is ready", chat: "1", from: "1", place: "dm", answers: "1:10" })
		  emit("message", { bridge: "telegram", text: "tell bob", chat: "1", from: "1", place: "dm", answers: "1:11" })
		  return { cursor: 1 }
		}
		return { sent: [input.answers || ""] }
	`})
	w.sync(dir)
	question := w.question(map[string]bool{})
	if text := fmt.Sprint(w.show(question.ID).Input); !strings.Contains(text, "> alice: the plan is ready\n> alice: tell bob") {
		t.Errorf("the approver was shown %s", text)
	}
	w.tap(question.ID, "1", "yes")
	w.until("the reply said", func() bool { return sayings(t, w)["told"] != nil })

	// sent gives the runs a run's record names for what it sent or called to a
	// location.
	sent := func(from, kind, at string) []string {
		var out []string
		for _, ask := range w.asked(from, kind) {
			if ask.Location == at {
				out = append(out, cmp.Or(ask.Run, ask.To))
			}
		}
		return out
	}
	// one fails the test unless there is one run, and gives it.
	one := func(what string, runs []string) string {
		t.Helper()
		if len(runs) == 0 {
			t.Fatalf("no %s", what)
		}
		return runs[len(runs)-1]
	}
	bridge := w.kept(router("telegram"))
	listener := ""
	for _, run := range w.at(router("telegram")) {
		if run.Parent == bridge {
			listener = run.ID
		}
	}
	chat := one("chat", sent(listener, instance.AskEvent, location(".rook/blocks/chat")))
	if run := w.show(chat); run.StartedBy != listener || run.Key != "telegram:1" {
		t.Errorf("the chat %+v was not started by the listener %s", run, listener)
	}
	rules := 0
	for _, run := range stubs(t, w, "route") {
		if w.show(run.Parent).ID == chat && fmt.Sprint(w.show(run.ID).Output.(map[string]any)["rule"]) == "map[for:[alice] name:helper]" {
			rules++
		}
	}
	if rules != 2 {
		t.Errorf("%d of the chat's routes name the rule that chose the agent", rules)
	}
	conversation := one("conversation", sent(chat, instance.AskEvent, location(".rook/blocks/conversation")))
	if run := w.show(conversation); run.StartedBy != chat || run.Key != "telegram:1:helper" {
		t.Errorf("the conversation is %+v", run)
	}
	agents := sent(conversation, instance.AskCall, location("agents/helper"))
	if len(agents) != 2 {
		t.Fatalf("the conversation called %v", agents)
	}
	agent := agents[1]
	if run := w.show(agent); w.show(run.StartedBy).Parent != conversation {
		t.Errorf("the agent %+v was not called by the conversation", run)
	}
	tools := map[string]string{}
	runs, err := w.List(context.Background(), client.Filter{Parent: agent, Finished: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range runs {
		tools[run.Machine] = run.ID
	}
	if tools["tell"] == "" || tools["approval"] == "" {
		t.Fatalf("the agent's runs are %v", tools)
	}
	if asked := one("question", sent(tools["approval"], instance.AskCall, location(".rook/blocks/ask"))); asked != question.ID || w.show(asked).StartedBy != tools["approval"] {
		t.Errorf("the approval called %s, and the question is %s", asked, question.ID)
	}
}
