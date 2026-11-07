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

	"github.com/zummed/corbie/client"
	"github.com/zummed/corbie/instance"
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

// slowToolkit is a toolkit with one tool, wait, which takes an hour.
const slowToolkit = `
	description: Waiting.
	tools:
	  wait:
	    description: Wait an hour.
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
		return calls("open", `{"toolkit": "slow"}`)
	case 1:
		return calls("wait", `{}`)
	}
	return llmstandin.Reply{Text: "waited"}
}

// stopProject makes a Telegram project whose agent at agents/assistant opens
// the toolkits house and slow, and asks the stand-in model above.
func stopProject(t *testing.T, w *world, tg *telegram) string {
	t.Helper()
	_, address := standin(t, w, stopModel)
	return chatProject(t, w, tg, map[string]string{
		"agents/assistant.yaml":    modelAgent("assistant", address, "toolkits:\n  house: { include: toolkits/house.yaml }\n  slow: { include: toolkits/slow.yaml }\n"),
		"toolkits/house.yaml":      houseToolkit,
		"toolkits/house/lights.js": `return { on: ["kitchen", "hall"] }`,
		"toolkits/slow.yaml":       slowToolkit,
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

// stops gives the stops in a run's own record: the name and the ID on its
// service of who sent each.
func (w *world) stops(id string) []string {
	var out []string
	for _, e := range w.own(id) {
		for _, ev := range e.Events {
			if ev.Name != "stop" {
				continue
			}
			data, _ := ev.Data.(map[string]any)
			out = append(out, fmt.Sprint(data["name"], " ", data["user"]))
		}
	}
	return out
}

// /stop while a tool waits: the agent's run, the tool's run and the turn's
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
	agent := one(t, w, "assistant")
	message := one(t, w, "turn")
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
	conversation := keyed(w, "conversation.yaml")["telegram:1:1:assistant"]
	if len(conversation) != 1 {
		t.Fatalf("there are %d conversations", len(conversation))
	}
	if got := w.stops(conversation[0].ID); !slices.Equal(got, []string{"Alice 1"}) {
		t.Errorf("the conversation's record shows the stops %q", got)
	}
	if f := failedRuns(t, w); len(f) > 0 {
		t.Errorf("runs failed: %v", f)
	}
}

// /stop while a held call waits for alice's yes: the question, its approval
// and the agent are cancelled, and alice is told "Stopped.". A tap on the
// question afterwards runs nothing, and the router's record notes it as
// undelivered.
func TestStopCancelsAQuestion(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := stopProject(t, w, tg)
	tg.dm(1, "tell bob hello")
	w.sync(dir)
	question := w.question(map[string]bool{})
	approval := one(t, w, "approval")
	agent := one(t, w, "assistant")
	tg.dm(1, "/stop")
	tg.waitFor(w, "1", "Stopped.")
	cancelled(w, question.ID, approval, agent)
	tg.tapOn(t, 0, 1, "yes")
	w.until("the tap heard", tg.consumed)
	router := w.kept(router("telegram"))
	w.until("the tap noted as undelivered", func() bool { return len(undelivered(w, router)) == 1 })
	quiet()
	if got := tg.texts("2"); len(got) != 0 {
		t.Errorf("bob was told %q after the stop", got)
	}
	if got := tg.texts("1"); got[len(got)-1] != "Stopped." {
		t.Errorf("alice was told %q", got)
	}
}

// slowAgent is an agent with no model that takes an hour to answer.
const slowAgent = `
	flow: assistant
	stages:
	  long:
	    job:
	      machine:` + hourMachine + `
	output: { reply: waited }
`

// Slack's stop button under a thread where alice's answer is being worked on:
// presses by a stranger and by bob go to conversations of their own, which no
// turn runs in, and do nothing. Hers cancels the agent and its job, says
// "Stopped." in the thread, and clears the status. So it is in a channel and
// in a direct message's thread.
func TestSlackStopButtonStopsTheAnswer(t *testing.T) {
	for _, channel := range []string{"C1", "D1"} {
		t.Run(channel, func(t *testing.T) {
			s := newSlack(t)
			w := newWorld(t)
			dir := slackProject(t, w, s)
			write(t, dir, map[string]string{"agents/assistant.yaml": slowAgent})
			w.sync(dir)
			w.until("Slack connected", s.connected)
			message := inChannel("U1", "5.0", "4.0", "<@UBOT> wait")
			if channel == "D1" {
				message = dm("U1", "5.0", "wait")
				message["thread_ts"] = "4.0"
			}
			s.says("a", message)
			hour := one(t, w, "hour")
			agent := one(t, w, "assistant")
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
			all := keyed(w, "conversation.yaml")
			conversation := all["slack:"+channel+"/4.0:U1:assistant"]
			if len(conversation) != 1 {
				t.Fatalf("the conversations are %v", all)
			}
			if got := w.stops(conversation[0].ID); !slices.Equal(got, []string{"Alice U1"}) {
				t.Errorf("the conversation's record shows the stops %q", got)
			}
			for key, runs := range all {
				if runs[0].ID != conversation[0].ID && w.show(runs[0].ID).Status != "done" {
					t.Errorf("the conversation %s is %s", key, w.show(runs[0].ID).Status)
				}
			}
		})
	}
}

// countingAgent is the echo agent, which counts the turns of its conversation,
// and which takes an hour first for a message with the word wait.
const countingAgent = `
	flow: helper
	input: { messages: list, medium: string, format: "any?", asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
	stages:
	  long:
	    when: "${input.messages.some((m) => m.text.includes('wait'))}"
	    job:
	      machine:` + hourMachine + `
	  turn: { job: { script: ./turn.js, input: { name: helper, said: "${input.messages.map((m) => m.text).join(' ')}", asker: "${input.asker.name}", conversation: "${input.conversation}" } } }
	output: ${stages.turn.output}
`

// A stopped message leaves the conversation as it was: the message after it is
// the conversation's second turn, after the first.
func TestStopKeepsTheConversation(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := chatProject(t, w, tg, map[string]string{
		"agents/assistant.yaml": countingAgent,
		"agents/turn.js":        turnJS,
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

// /stop@<bot>, and /stop with words after it, are a stop, which the router
// sends to the person's conversation, and is not answered. /stopwatch is a
// message.
func TestStopWithABotNameOrWords(t *testing.T) {
	for _, text := range []string{"/stop@rook_bot", "/stop please", "/stopwatch"} {
		t.Run(text, func(t *testing.T) {
			tg := newTelegram(t)
			w := newWorld(t)
			dir := chatProject(t, w, tg, nil)
			tg.dm(1, text)
			tg.dm(1, "after")
			w.sync(dir)
			tg.waitFor(w, "1", "after")
			var events []string
			for _, run := range keyed(w, "conversation.yaml")["telegram:1:1:assistant"] {
				for _, e := range w.own(run.ID) {
					for _, ev := range e.Events {
						if ev.Name == "stop" || ev.Name == "message" {
							events = append(events, ev.Name)
						}
					}
				}
			}
			want, said := []string{"stop", "message"}, []string{"after"}
			if text == "/stopwatch" {
				want, said = []string{"message", "message"}, []string{"/stopwatch", "after"}
			}
			if !slices.Equal(events, want) || !slices.Equal(tg.lines("1"), said) {
				t.Errorf("the conversations took %v, and alice was told %q", events, tg.lines("1"))
			}
		})
	}
}

// A stop sent to a conversation that is not running starts a run, which ends
// at once. A stop to a conversation that has answered, and waits for the next
// message, is dropped, and the conversation keeps running.
func TestStopStartsNoConversation(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	removeListener(t, dir)
	w.sync(dir)
	stop := map[string]any{"service": "telegram", "place": "dm", "chat": "1", "user": "1", "name": "Alice"}
	w.sendTo("conversation.yaml", "telegram:1:1:assistant", "stop", stop)
	w.until("the stopped conversation ends", func() bool {
		runs := keyed(w, "conversation.yaml")["telegram:1:1:assistant"]
		return len(runs) == 1 && runs[0].Status == "done"
	})
	w.sendTo("conversation.yaml", "telegram:2:2:assistant", "message", from("2", "hello", 1))
	var id string
	w.until("the conversation answers", func() bool {
		runs := keyed(w, "conversation.yaml")["telegram:2:2:assistant"]
		if len(runs) != 1 {
			return false
		}
		id = runs[0].ID
		return slices.ContainsFunc(w.at("conversation.yaml"), func(r *client.Run) bool { return r.Parent == id && r.Status == "done" })
	})
	w.sendTo("conversation.yaml", "telegram:2:2:assistant", "stop", stop)
	w.until("the conversation takes the stop", func() bool {
		return slices.ContainsFunc(w.record(id), func(e client.Entry) bool {
			return slices.ContainsFunc(e.Events, func(a instance.Arrived) bool { return a.Name == "stop" })
		})
	})
	if run := w.show(id); run.Status != "running" {
		t.Errorf("a stop ended a conversation that had answered: %s %s", run.Status, run.Error)
	}
}

// A message and /stop that Telegram gives in one poll: the stop comes while
// the conversation adds the message, and cancels the turn as it starts. Alice
// is told "Stopped." and nothing else, no agent's run is left running, and her
// next message is answered.
func TestStopRightAfterAMessage(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir := stopProject(t, w, tg)
	tg.dm(1, "wait an hour")
	tg.dm(1, "/stop")
	w.sync(dir)
	tg.waitFor(w, "1", "Stopped.")
	cancelled(w, one(t, w, "turn"))
	for _, run := range stubs(t, w, "assistant") {
		cancelled(w, run.ID)
	}
	for _, run := range stubs(t, w, "hour") {
		cancelled(w, run.ID)
	}
	tg.dm(1, "hello")
	tg.waitFor(w, "1", "ok: hello")
	if got := tg.texts("1"); !slices.Equal(got, []string{"Stopped.", "ok: hello"}) {
		t.Errorf("alice was told %q", got)
	}
	if f := failedRuns(t, w); len(f) > 0 {
		t.Errorf("runs failed: %v", f)
	}
}
