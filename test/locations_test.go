package test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zummed/corbie/client"
	"github.com/zummed/corbie/instance"
)

// These tests are of what a project's tree gives Rook: who may send to its
// runs, how its runs live through changes, and how a message is followed from
// its router to every run it led to.

// message is a message from alice in her chat on Telegram.
func message(text string) map[string]any { return said("1", "1", text) }

// A run of another tree, though its client owns both, can neither send a
// message to the conversation nor call the agent of the project: the
// conversation receives only from the routers, and the agent only from the
// conversation.
func TestAnotherTreeCannotReachTheProject(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	w.sync(dir)
	other := filepath.Join(t.TempDir(), "other")
	write(t, other, map[string]string{"poke.yaml": `
		flow: poke
		allow: ["send:/project/**"]
		stages:
		  send: { job: { send: { to: { at: /project/conversation, key: "telegram:1:1:assistant" }, event: message, data: { service: telegram, text: hi, chat: "1", user: "1", place: dm } } } }
		  call: { job: { call: { at: /project/agents/assistant }, input: { messages: [{ text: hi }], medium: telegram, asker: { name: alice, email: alice@example.com, scopes: [messages] } } } }
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
	if got := undelivered(w, id); len(got) != 1 || !strings.Contains(got[0], "sent to /project/conversation#telegram:1:1:assistant was not delivered") {
		t.Errorf("the sends noted undelivered are %+v", got)
	}
	for _, path := range []string{"conversation.yaml", "agents/assistant.yaml"} {
		if runs := w.at(path); len(runs) != 0 {
			t.Errorf("%s has runs %v", path, runs)
		}
	}
}

// Another client, as a gateway's is, cannot call the bot's chat/post in the
// project, though its ceiling allows everything.
func TestAnotherClientCannotCallAService(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	w.sync(dir)
	gateway := w.newClient("gateway")
	id, err := gateway.Start(context.Background(), client.Start{At: location(bot + "/telegram/chat/post"), Input: map[string]any{"in": "1", "text": "hi"}})
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
	if runs := w.at(bot + "/telegram/chat/post.yaml"); len(runs) != 0 {
		t.Errorf("chat/post ran: %v", runs)
	}
}

// A tap that names a conversation's key, or the key of a question that has
// ended, reaches no run: the router sends a tap only to `answers`, whose run
// at that key ends at once, since no question opened it, and sends nothing.
func TestTapReachesOnlyAWaitingQuestion(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
		".rook/telegram/telegram.js": oneMessage,
		"agents/asker.yaml":          telegramAsker,
		"people/find.yaml":           telegramFind,
		"people/scopes.yaml":         "bob@example.com: { name: bob, scopes: [house:approve] }\n",
	})
	w.sync(dir)
	w.until("the answer", func() bool { return sayings(t, w)["one"] != nil })
	conversation := keyed(w, "conversation.yaml")["telegram:1:1:assistant"][0].ID
	id := w.start("agents/asker.yaml", nil)
	question := w.question(map[string]bool{}).ID
	key := w.key(question)
	w.tap(question, "2", "yes")
	w.until("the question answered", func() bool { return w.show(id).HasEnded() })
	w.until("its answers run ended", func() bool { return keyed(w, "answers.yaml")[key][0].HasEnded() })
	write(t, dir, map[string]string{".rook/telegram/telegram.js": `
		if (input.do === "hear") {
		  if (input.cursor !== null) throw new Error("nothing more")
		  emit("answer", { service: "telegram", question: "telegram:1:1:assistant", user: "2", choice: "yes" })
		  emit("answer", { service: "telegram", question: "` + key + `", user: "2", choice: "no" })
		  return { cursor: 1 }
		}
		return {}
	`})
	w.sync(dir)
	w.until("both taps' runs ended", func() bool {
		runs := keyed(w, "answers.yaml")
		return len(runs["telegram:1:1:assistant"]) == 1 && runs["telegram:1:1:assistant"][0].HasEnded() && len(runs[key]) == 2 && runs[key][0].HasEnded() && runs[key][1].HasEnded()
	})
	// Only the run the question opened sent anything: the first tap.
	sent := 0
	for _, runs := range keyed(w, "answers.yaml") {
		for _, run := range runs {
			sent += len(w.ownAsks(run.ID, instance.AskEvent))
		}
	}
	if sent != 1 {
		t.Errorf("the runs of answers sent %d answers", sent)
	}
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

// A conversation quiet for a day ends; the next message to its key starts a
// new one, whose input is that message, and which starts a new conversation
// with the agent.
func TestQuietConversationEndsAndTheNextMessageStartsAnother(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	w.sync(dir)
	w.sendTo("conversation.yaml", key("1", "1"), "message", message("one"))
	w.until("the conversation waiting", func() bool {
		runs := keyed(w, "conversation.yaml")[key("1", "1")]
		return len(runs) == 1 && sayings(t, w)["one"] != nil && w.show(runs[0].ID).Timers == 1
	})
	first := keyed(w, "conversation.yaml")[key("1", "1")][0].ID
	w.set(w.now().Add(25 * time.Hour))
	w.until("the conversation ended", func() bool { return w.show(first).Status == "done" })
	w.sendTo("conversation.yaml", key("1", "1"), "message", message("two"))
	w.until("the second answer", func() bool { return sayings(t, w)["two"] != nil })
	runs := keyed(w, "conversation.yaml")[key("1", "1")]
	if len(runs) != 2 {
		t.Fatalf("the conversations are %v", runs)
	}
	for _, run := range runs {
		if run.ID != first && fmt.Sprint(w.show(run.ID).Input.(map[string]any)["text"]) != "two" {
			t.Errorf("the new conversation's input is %v", w.show(run.ID).Input)
		}
	}
	for _, run := range w.at("agents/assistant.yaml") {
		if in := w.show(run.ID).Input.(map[string]any); w.texts(run)[0] == "two" && in["conversation"] != nil {
			t.Errorf("the new conversation carried on the old: %v", in)
		}
	}
}

// A changed conversation.yaml lets a conversation under way finish: an
// approval it waits for still reaches its end, and the reply is said.
func TestChangedConversationFinishesWhatItDoes(t *testing.T) {
	w := newWorld(t)
	dir, _ := approving(t, w, oneMessage)
	w.sync(dir)
	question := w.question(map[string]bool{})
	conversation := keyed(w, "conversation.yaml")[key("1", "1")][0].ID
	setSite(t, dir, "conversation.yaml", "lifetime: keyed\n", "lifetime: keyed\nmeta: { changed: true }\n")
	w.sync(dir)
	w.tap(question.ID, "3", "yes")
	w.until("the reply said", func() bool { return sayings(t, w)["told"] != nil })
	if run := w.show(conversation); run.Status != "running" {
		t.Errorf("the conversation is %s", run.Status)
	}
}
