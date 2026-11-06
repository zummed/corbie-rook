package rook_test

// A stop, /stop on Telegram or a press of Slack's stop button, cancels what a
// conversation is answering: the agent's run, its tools' runs and the question
// a held call waits on. The person is told, the next message is answered as
// usual, and only someone who gets that agent there can stop it.

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// hourMachine is a statechart that waits an hour, then ends.
const hourMachine = `
	        statechart: hour
	        initial: waiting
	        states:
	          waiting: { timers: { due: 1h }, on: { due: over } }
	          over: { end: done }
`

// slowPart is a part with one tool, wait, which takes an hour.
const slowPart = `
	does: Waiting.
	tools:
	  wait:
	    does: Wait an hour.
	    args: {}
	    job:
	      machine:` + hourMachine

// stopModel calls wait, after opening slow, for a message with the word wait,
// and answers any other as houseModel does.
func stopModel(c llmstandin.Call) llmstandin.Reply {
	said, results := turnOf(c)
	if !strings.Contains(said, "wait") {
		return houseModel(c)
	}
	switch len(results) {
	case 0:
		return calls("open", `{"part": "slow"}`)
	case 1:
		return calls("wait", `{}`)
	}
	return llmstandin.Reply{Text: "waited"}
}

// stopProject makes a Telegram project whose helper, for alice, opens the
// parts house and slow, and asks the stand-in model above.
func stopProject(t *testing.T, w *world, tg *telegram) string {
	t.Helper()
	_, address := standin(t, w, stopModel)
	return chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml":    modelAgent("helper", address, "open:\n  house: { include: parts/house/part.yaml }\n  slow: { include: parts/slow/part.yaml }\n"),
		"parts/house/part.yaml": housePart(""),
		"parts/house/lights.js": `return { on: ["kitchen", "hall"] }`,
		"parts/slow/part.yaml":  slowPart,
		"rules.yaml":            teamRules,
		"agents/team.yaml":      echoAgent("team"),
		"agents/turn.js":        turnJS,
	})
}

// one waits for the one run of a machine, and gives it.
func one(t *testing.T, w *world, machine string) string {
	t.Helper()
	w.until("a run of "+machine, func() bool { return len(stubs(t, w, machine)) == 1 })
	return stubs(t, w, machine)[0].ID
}

// cancelled waits for each run to be cancelled.
func cancelled(w *world, ids ...string) {
	w.t.Helper()
	for _, id := range ids {
		w.until(id+" cancelled", func() bool { return w.show(id).Status == "cancelled" })
	}
}

// stops gives the stops in a run's own record: who the chat says sent each,
// and the sender's id on its bridge.
func (w *world) stops(id string) []string {
	var out []string
	for _, e := range w.own(id) {
		for _, ev := range e.Events {
			if ev.Name != "stop" {
				continue
			}
			data, _ := ev.Data.(map[string]any)
			message, _ := data["message"].(map[string]any)
			out = append(out, fmt.Sprint(data["asker"], " ", message["from"]))
		}
	}
	return out
}

// /stop while a tool waits: the agent's run, the tool's run and the message's
// run are cancelled, alice is told "Stopped." in reply to her /stop, and her
// next message is answered. The conversation's record shows the stop and who
// sent it.
func TestStopCancelsALongTool(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := stopProject(t, w, tg)
	tg.dm(1, "wait an hour")
	w.sync(dir)
	hour := one(t, w, "hour")
	agent := one(t, w, "helper")
	message := one(t, w, "message")
	tg.dm(1, "/stop")
	tg.waitFor(w, "1", "Stopped.")
	cancelled(w, hour, agent, message)
	tg.mu.Lock()
	stopped := tg.sent[len(tg.sent)-1]
	tg.mu.Unlock()
	if rp, _ := stopped["reply_parameters"].(map[string]any); rp == nil || rp["message_id"] != 11.0 {
		t.Errorf("Stopped. does not answer the /stop: %v", stopped)
	}
	tg.dm(1, "hello")
	tg.waitFor(w, "1", "ok: hello")
	if got := tg.texts("1"); len(got) != 2 {
		t.Errorf("alice was told %q", got)
	}
	conversation := keyed(w, ".rook/blocks/conversation.yaml")["telegram:1:helper"]
	if len(conversation) != 1 {
		t.Fatalf("there are %d conversations", len(conversation))
	}
	if got := w.stops(conversation[0].ID); !slices.Equal(got, []string{"alice 1"}) {
		t.Errorf("the conversation's record shows the stops %q", got)
	}
	if f := failedRuns(t, w); len(f) > 0 {
		t.Errorf("runs failed: %v", f)
	}
}

// /stop while a held call waits for alice's yes: the question, its approval
// and the agent are cancelled, and alice is told "Stopped.". A tap on the
// question afterwards runs nothing, and is noted as undelivered.
func TestStopCancelsAQuestion(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := stopProject(t, w, tg)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	question := w.question(map[string]bool{})
	approval := one(t, w, "approval")
	agent := one(t, w, "helper")
	tg.dm(1, "/stop")
	tg.waitFor(w, "1", "Stopped.")
	cancelled(w, question.ID, approval, agent)
	tg.tapOn(t, 0, 1, "yes")
	w.until("the tap heard", tg.consumed)
	var notes []string
	w.until("the tap noted as undelivered", func() bool {
		notes = nil
		for _, run := range listenerRuns(w, "telegram") {
			notes = append(notes, undelivered(w, run.ID)...)
		}
		return len(notes) == 1
	})
	quiet()
	if got := tg.texts("2"); len(got) != 0 {
		t.Errorf("bob was told %q after the stop", got)
	}
	if got := tg.texts("1"); got[len(got)-1] != "Stopped." {
		t.Errorf("alice was told %q", got)
	}
}

// /stop with nothing being answered is told so, and the next message is
// answered in the same conversation. The chat and the conversation it started
// are titled stop.
func TestStopWithNothingRunning(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := stopProject(t, w, tg)
	tg.dm(1, "/stop")
	w.sync(dir)
	tg.waitFor(w, "1", "Nothing is running.")
	tg.dm(1, "hello")
	tg.waitFor(w, "1", "ok: hello")
	if n := len(keyed(w, ".rook/blocks/conversation.yaml")["telegram:1:helper"]); n != 1 {
		t.Errorf("there are %d conversations", n)
	}
	w.titled(map[string][]string{"chat": {"dm: stop"}, "conversation": {"helper in dm: stop"}})
	if f := failedRuns(t, w); len(f) > 0 {
		t.Errorf("runs failed: %v", f)
	}
}

// slowAgent is an agent with no model that takes an hour to answer.
const slowAgent = `
	flow: helper
	stages:
	  long:
	    job:
	      machine:` + hourMachine + `
	output: { reply: waited }
`

// Slack's stop button under a thread where alice's answer is being worked on:
// presses by a stranger and by bob, who gets no agent there, do nothing. Hers
// cancels the agent and its job, clears the status, and says "Stopped." in
// the thread. So it is in a channel and in a direct message's thread.
func TestSlackStopButtonStopsTheAnswer(t *testing.T) {
	for _, channel := range []string{"C1", "D1"} {
		t.Run(channel, func(t *testing.T) {
			s := newSlack(t)
			w := newWorld(t)
			dir := slackProject(t, w, s)
			withHelp(t, dir)
			write(t, dir, map[string]string{"agents/helper.yaml": slowAgent})
			w.sync(dir)
			w.until("Slack connected", s.connected)
			message := inChannel("U1", "5.0", "4.0", "<@UBOT> wait")
			if channel == "D1" {
				message = dm("U1", "5.0", "wait")
				message["thread_ts"] = "4.0"
			}
			s.says("a", message)
			hour := one(t, w, "hour")
			agent := one(t, w, "helper")
			press := func(id, user string) {
				s.says(id, map[string]any{"type": "agent_session_stopped", "channel": channel, "thread_ts": "4.0", "user": user, "event_ts": "6." + id, "streaming_message_ts": []any{}})
			}
			press("1", "U9")
			press("2", "U2")
			w.until("both presses received", func() bool { return s.acked() == 3 })
			quiet()
			if got := s.posted(); len(got) != 0 {
				t.Fatalf("after others' presses the bot posted %v", got)
			}
			if run := w.show(agent); run.Status != "running" {
				t.Fatalf("after others' presses the agent is %s", run.Status)
			}
			press("3", "U1")
			w.until("Stopped. posted", func() bool { return len(s.posted()) == 1 })
			cancelled(w, hour, agent)
			if post := s.posted()[0]; post["markdown_text"] != "Stopped." || post["channel"] != channel || post["thread_ts"] != "4.0" {
				t.Errorf("the bot posted %v", post)
			}
			w.until("the status cleared", func() bool {
				s.mu.Lock()
				defer s.mu.Unlock()
				return len(s.status) == 2
			})
			s.mu.Lock()
			last := s.status[1]
			s.mu.Unlock()
			if last["status"] != "active" || last["channel_id"] != channel || last["thread_ts"] != "4.0" {
				t.Errorf("the last status is %v", last)
			}
			conversation := keyed(w, ".rook/blocks/conversation.yaml")["slack:"+channel+"/4.0:helper"]
			if len(conversation) != 1 {
				t.Fatalf("the conversations are %v", keyed(w, ".rook/blocks/conversation.yaml"))
			}
			if got := w.stops(conversation[0].ID); !slices.Equal(got, []string{"alice U1"}) {
				t.Errorf("the conversation's record shows the stops %q", got)
			}
			if n := len(keyed(w, ".rook/blocks/conversation.yaml")); n != 1 {
				t.Errorf("there are %d conversations", n)
			}
		})
	}
}


// countingAgent is the echo agent, which counts the turns of its conversation,
// and which takes an hour first for a message with the word wait.
const countingAgent = `
	flow: helper
	input: { said: string, asker: string, conversation: "any?" }
	stages:
	  long:
	    when: "${input.said.includes('wait')}"
	    job:
	      machine:` + hourMachine + `
	  turn: { job: { script: ./turn.js, input: { name: helper, said: "${input.said}", asker: "${input.asker}", conversation: "${input.conversation}" } } }
	output: ${stages.turn.output}
`

// A stopped message leaves the conversation as it was: the message after it is
// the conversation's second turn, after the first.
func TestStopKeepsTheConversation(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": countingAgent,
		"agents/turn.js":     turnJS,
		"rules.yaml":         teamRules,
		"agents/team.yaml":   echoAgent("team"),
	})
	tg.dm(1, "hello")
	w.sync(dir)
	tg.waitFor(w, "1", "helper to alice (1): hello")
	tg.dm(1, "wait an hour")
	hour := one(t, w, "hour")
	tg.dm(1, "/stop")
	tg.waitFor(w, "1", "Stopped.")
	cancelled(w, hour)
	tg.dm(1, "again")
	tg.waitFor(w, "1", "helper to alice (2): again")
}

// /stop@<bot>, and /stop with words after it, are a stop. /stopwatch is a
// message.
func TestStopWithABotNameOrWords(t *testing.T) {
	for _, text := range []string{"/stop@rook_bot", "/stop please", "/stopwatch"} {
		t.Run(text, func(t *testing.T) {
			tg := newTelegram(t)
			w := newWorld(t)
			dir := stopProject(t, w, tg)
			tg.dm(1, text)
			w.sync(dir)
			want := "Nothing is running."
			if text == "/stopwatch" {
				want = "ok: /stopwatch"
			}
			tg.waitFor(w, "1", want)
		})
	}
}

// A stop whose who.yaml fails, here on a field rules.yaml does not take, is
// told it could not be answered, and stops nothing.
func TestStopWhoseWhoFails(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/helper.yaml": echoAgent("helper"),
		"agents/turn.js":     turnJS,
		"rules.yaml":         "places:\n  dm: { readers: [asker] }\nagents:\n  - { name: helper, for: [alice], words: [hi] }\n",
	})
	tg.dm(1, "/stop")
	w.sync(dir)
	tg.waitFor(w, "1", "That could not be answered.")
	if f := failures(w); !slices.ContainsFunc(f, func(s string) bool { return strings.Contains(s, "the agent helper has words") }) {
		t.Errorf("no failure names the field: %q", f)
	}
	if f := failures(w); slices.ContainsFunc(f, func(s string) bool { return strings.Contains(s, "TypeError") }) {
		t.Errorf("the stop failed on its own output: %q", f)
	}
	if n := len(keyed(w, ".rook/blocks/conversation.yaml")); n != 0 {
		t.Errorf("there are %d conversations", n)
	}
}
