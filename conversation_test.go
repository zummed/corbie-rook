package rook_test

// The starting conversation: messages that come during a turn are answered
// together in the next, each person in a thread has their own run, a failed
// turn is told and starts afresh, and a project with no agent says so.

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	rook "github.com/zummed/corbie-rook"
	"github.com/zummed/corbie/client"
)

// waitingAgent stands in for the agent at agents/assistant: it waits for the
// event go, then replies with the texts of its turn's messages, joined by
// " + ", and counts the turns of its conversation.
const waitingAgent = `
	statechart: assistant
	input: { messages: list, medium: string, asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
	initial: waiting
	states:
	  waiting: { on: { go: answered } }
	  answered: { end: done, output: { reply: "${input.messages.map((m) => m.text).join(' + ')}", conversation: "${(input.conversation || 0) + 1}" } }
`

// said is a message from a person in a chat on Telegram, as the listener sends
// it.
func said(chat, user, text string) map[string]any {
	return map[string]any{"service": "telegram", "text": text, "chat": chat, "user": user, "name": names[atoi(user)], "place": "dm", "answers": chat + ":10"}
}

// conversationProject is the starter with Telegram's script standing in and no
// listener, the agent given at agents/assistant, and the files given.
func conversationProject(t *testing.T, w *world, agent string, files map[string]string) string {
	t.Helper()
	dir := copyStarter(t)
	removeListener(t, dir)
	write(t, dir, map[string]string{"agents/assistant.yaml": agent})
	write(t, dir, files)
	w.sync(dir)
	return dir
}

// key is the conversation's key for a person in a Telegram chat.
func key(chat, user string) string { return "telegram:" + chat + ":" + user + ":assistant" }

// agentRuns gives the runs of the agent at agents/assistant that a person's
// conversation called, oldest first.
func agentRuns(w *world, chat, user string) []*client.Run {
	var out []*client.Run
	for _, run := range w.at("agents/assistant.yaml") {
		if in, ok := w.show(run.ID).Input.(map[string]any); ok && run.Parent == "" {
			if m := in["messages"].([]any)[0].(map[string]any); m["chat"] == chat && m["user"] == user {
				out = append(out, run)
			}
		}
	}
	slices.SortFunc(out, func(a, b *client.Run) int { return a.Started.Compare(b.Started) })
	return out
}

// turn waits for the n-th run of a person's agent, and gives it.
func (w *world) turn(chat, user string, n int) *client.Run {
	w.t.Helper()
	var run *client.Run
	w.until(fmt.Sprintf("turn %d of %s in %s", n, user, chat), func() bool {
		runs := agentRuns(w, chat, user)
		if len(runs) > n {
			w.t.Fatalf("%d turns, more than %d", len(runs), n)
		}
		if len(runs) == n {
			run = runs[n-1]
		}
		return run != nil
	})
	return run
}

// texts gives the texts of the messages a turn of the agent was given.
func (w *world) texts(run *client.Run) []string {
	var out []string
	for _, m := range w.show(run.ID).Input.(map[string]any)["messages"].([]any) {
		out = append(out, m.(map[string]any)["text"].(string))
	}
	return out
}

// Messages that come while the agent works are kept, and the next turn is
// given all of them, in the order they came, as one list. A turn's reply is
// posted, and each turn carries the conversation on.
func TestMessagesDuringATurnAreAnsweredTogether(t *testing.T) {
	w := newWorld(t)
	conversationProject(t, w, waitingAgent, nil)
	w.sendTo("conversation.yaml", key("1", "1"), "message", said("1", "1", "a1"))
	first := w.turn("1", "1", 1)
	for _, text := range []string{"a2", "a3", "a4"} {
		w.sendTo("conversation.yaml", key("1", "1"), "message", said("1", "1", text))
	}
	quiet()
	w.send(first.ID, "go", nil)
	second := w.turn("1", "1", 2)
	if got := w.texts(second); !slices.Equal(got, []string{"a2", "a3", "a4"}) {
		t.Fatalf("the second turn was given %q", got)
	}
	w.send(second.ID, "go", nil)
	w.until("both replies", func() bool { return sayings(t, w)["a2 + a3 + a4"] != nil && sayings(t, w)["a1"] != nil })
	if got := w.show(second.ID).Input.(map[string]any)["conversation"]; got != 1.0 {
		t.Errorf("the second turn was given the conversation %v", got)
	}
	w.sendTo("conversation.yaml", key("1", "1"), "message", said("1", "1", "a5"))
	third := w.turn("1", "1", 3)
	if got := w.texts(third); !slices.Equal(got, []string{"a5"}) {
		t.Errorf("the third turn was given %q", got)
	}
	if runs := keyed(w, "conversation.yaml")[key("1", "1")]; len(runs) != 1 {
		t.Errorf("there are %d conversations", len(runs))
	}
}

// Two people writing in one thread have a run each, and a turn each, which
// may run at once: each is given only their own messages and their own
// conversation, and is looked up as themselves.
func TestEachPersonInAThreadHasTheirOwnTurn(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	write(t, dir, map[string]string{"agents/assistant.yaml": waitingAgent, "people/find.yaml": telegramFind, "people/scopes.yaml": scenarioPeople})
	w.sync(dir)
	w.sendTo("conversation.yaml", key("5", "1"), "message", said("5", "1", "alice one"))
	w.sendTo("conversation.yaml", key("5", "2"), "message", said("5", "2", "bob one"))
	alice, bob := w.turn("5", "1", 1), w.turn("5", "2", 1)
	if a, b := w.texts(alice), w.texts(bob); !slices.Equal(a, []string{"alice one"}) || !slices.Equal(b, []string{"bob one"}) {
		t.Fatalf("alice's turn was given %q, and bob's %q", a, b)
	}
	asker := func(run *client.Run) any { return w.show(run.ID).Input.(map[string]any)["asker"] }
	if a, b := asker(alice), asker(bob); fmt.Sprint(a) != "map[email:alice@example.com name:alice scopes:[house house:approve]]" ||
		fmt.Sprint(b) != "map[email:bob@example.com name:bob scopes:[house]]" {
		t.Errorf("alice was looked up as %v, and bob as %v", a, b)
	}
	w.send(alice.ID, "go", nil)
	w.sendTo("conversation.yaml", key("5", "1"), "message", said("5", "1", "alice two"))
	second := w.turn("5", "1", 2)
	if got := w.show(second.ID).Input.(map[string]any)["conversation"]; got != 1.0 {
		t.Errorf("alice's second turn was given the conversation %v", got)
	}
	w.send(bob.ID, "go", nil)
	w.send(second.ID, "go", nil)
	w.until("three replies", func() bool { return len(sayings(t, w)) == 3 })
	if keys := slices.Sorted(func(yield func(string) bool) {
		for k := range keyed(w, "conversation.yaml") {
			yield(k)
		}
	}); !reflect.DeepEqual(keys, []string{key("5", "1"), key("5", "2")}) {
		t.Errorf("the conversations are %v", keys)
	}
}

// A turn whose agent fails is told "That could not be answered.", and the
// next turn starts the conversation afresh, so a message the agent cannot
// take does not fail every later one.
func TestFailedTurnIsToldAndStartsAfresh(t *testing.T) {
	w := newWorld(t)
	conversationProject(t, w, `
		flow: assistant
		input: { messages: list, medium: string, asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
		stages:
		  work: { job: { script: ./boom.js, input: { said: "${input.messages.map((m) => m.text).join(' ')}" } } }
		output: { reply: "${input.messages.map((m) => m.text).join(' ')}", conversation: "${(input.conversation || 0) + 1}" }
	`, map[string]string{"agents/boom.js": `if (input.said === "two") throw new Error("the model cannot take this")
		return {}`})
	for _, text := range []string{"one", "two", "three"} {
		w.sendTo("conversation.yaml", key("1", "1"), "message", said("1", "1", text))
		want := text
		if text == "two" {
			want = "That could not be answered."
		}
		w.until(want+" posted", func() bool { return sayings(t, w)[want] != nil })
	}
	given := map[string]any{}
	for _, run := range w.at("agents/assistant.yaml") {
		in := w.show(run.ID).Input.(map[string]any)
		given[w.texts(run)[0]] = in["conversation"]
	}
	if want := map[string]any{"one": nil, "two": 1.0, "three": nil}; !reflect.DeepEqual(given, want) {
		t.Errorf("the turns were given the conversations %v", given)
	}
}

// A project as init writes it has no agent: a person who writes is told the
// bot has no agent yet, with no name of the project's files, and the failed
// turn names the agent's location.
func TestStarterAnswersNobody(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	w.secret("TELEGRAM_TOKEN", "123:abc")
	dir := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := rook.Init(dir, bot, []string{"telegram"}); err != nil {
		t.Fatal(err)
	}
	point(t, dir, "telegram", tg.Server, "api.telegram.org")
	tg.dm(1, "hi")
	tg.dm(9, "hello?")
	w.sync(dir)
	tg.waitFor(w, "1", "This bot has no agent yet.")
	tg.waitFor(w, "9", "This bot has no agent yet.")
	for _, f := range failures(w) {
		if !strings.Contains(f, "nothing is recorded at "+project+"/agents/assistant") && !strings.HasSuffix(f, " fail: ") {
			t.Errorf("a run failed: %s", f)
		}
	}
}

// A stranger, whom the lookup does not find, is answered by the agent with no
// scopes, and so no toolkit; the name is the one the service gave.
func TestStrangerHasNoScopes(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, model := houseProject(t, w, tg, nil)
	tg.dm(9, "hello there")
	w.sync(dir)
	tg.waitFor(w, "9", "ok: hello there")
	in := w.show(w.at("agents/assistant.yaml")[0].ID).Input.(map[string]any)
	if fmt.Sprint(in["asker"]) != "map[email: name:Stranger scopes:[]]" {
		t.Errorf("the stranger was looked up as %v", in["asker"])
	}
	if tools := fmt.Sprint(model.Calls[0].Tools); strings.Contains(tools, "house") || strings.Contains(tools, "open") {
		t.Errorf("the stranger's agent was offered %s", tools)
	}
}

// A project with two agents has a conversation for each, and its router
// chooses by the words: a message about a deploy goes to the conversation
// that calls agents/deploy, any other to the one that calls agents/assistant.
// Each keeps its own conversation with its agent.
func TestRouterChoosesAConversationByWords(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
		".rook/telegram/telegram.js": heard(from("1", "please deploy the site", 10), from("1", "how are you", 11)),
		"agents/assistant.yaml":      echoAgent("assistant"),
		"agents/deploy.yaml":         echoAgent("deploy"),
		"agents/turn.js":             turnJS,
	})
	conversation := readFile(t, dir, "conversation.yaml")
	write(t, dir, map[string]string{"deploys.yaml": strings.ReplaceAll(strings.ReplaceAll(conversation, "agents/assistant", "agents/deploy"), "statechart: conversation", "statechart: deploys")})
	setSite(t, dir, router("telegram"), "        when: \"${!event.data.change}\"\n        send:", ""+
		"        - when: \"${/\\\\bdeploy\\\\b/i.test(event.data.text)}\"\n"+
		"          send: { to: { at: deploys, key: \"${'telegram:' + event.data.chat + ':' + event.data.user + ':deploy'}\" }, event: message, data: \"${event.data}\" }\n"+
		"        - when: \"${!event.data.change}\"\n"+
		"          send:")
	setSite(t, dir, router("telegram"), "allow: [send:conversation,", "allow: [send:deploys, send:conversation,")
	w.sync(dir)
	w.until("both answered", func() bool {
		said := sayings(t, w)
		return said["deploy to Alice (1): please deploy the site"] != nil && said["assistant to Alice (1): how are you"] != nil
	})
	if runs := keyed(w, "deploys.yaml"); len(runs["telegram:1:1:deploy"]) != 1 || len(runs) != 1 {
		t.Errorf("the deploys conversations are %v", runs)
	}
}

// An agent may post as it works, at the bot's chat/post, and give an empty
// reply or none: nothing more is posted, and the conversation goes on.
func TestAgentPostsAsItWorksAndGivesNoReply(t *testing.T) {
	for name, output := range map[string]string{"an empty reply": `output: { reply: "" }`, "no output": ""} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			conversationProject(t, w, `
				flow: assistant
				input: { messages: list, medium: string, asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
				allow: [send:gromit/telegram/chat/post]
				stages:
				  progress:
				    job: { call: { at: gromit/telegram/chat/post }, input: { in: "${input.messages[0].chat}", text: "on ${input.messages[0].text}" } }
			`+"\n"+output, nil)
			for _, text := range []string{"one", "two"} {
				w.sendTo("conversation.yaml", key("1", "1"), "message", said("1", "1", text))
				w.until("on "+text+" posted", func() bool { return sayings(t, w)["on "+text] != nil })
			}
			quiet()
			if said := sayings(t, w); len(said) != 2 {
				t.Errorf("what was posted is %v", said)
			}
			if runs := keyed(w, "conversation.yaml")[key("1", "1")]; len(runs) != 1 || w.show(runs[0].ID).Status != "running" {
				t.Errorf("the conversations are %v", runs)
			}
		})
	}
}
