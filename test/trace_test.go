package test

// A message followed from its chat to the conversation, the agent, each
// tool's run and the question an approval asks, as corbie run show --record
// and the client's API read them; and a project changed while a conversation
// waits on an approval.

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	rook "github.com/zummed/corbie-rook/rook"
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

// traceHelper is the agent at agents/assistant with a model at an address,
// which may open the toolkit messages: the tool clock, a script with no
// approval, and tell, held for someone with messages:approve. Its ask names
// no asker, so alice may approve what she asked for.
func traceHelper(address, system string) map[string]string {
	return map[string]string{
		"agents/assistant.yaml": `
			agent: assistant
			include: .rook/agent.yaml
			model: { name: test-model, service: anthropic, address: "` + address + `", secret: TEST_KEY }
			system: ` + system + `
			ask:
			  machine: .rook/approval.yaml
			  input: { ask: [{ at: gromit/telegram/chat/ask, in: "1" }], approvers: "messages:approve" }
			toolkits:
			  messages: { include: toolkits/messages.yaml }
		`,
		"toolkits/messages.yaml": `
			description: Messages to the people this bot knows, and the time.
			tools:
			  clock:
			    description: Give the time in a zone.
			    args: { zone: string }
			    job: { script: ./clock.js, input: { zone: "${args.zone}" } }
			  tell:
			    description: Write a text in a person's Telegram chat, by the chat's ID.
			    args: { chat: string, text: string }
			    job:
			      call: { at: gromit/telegram/chat/post }
			      input: { in: "${args.chat}", text: "${args.text}" }
			      allow: [send:gromit/telegram/chat/post]
			      approve: true
		`,
		"toolkits/clock.js": `return { zone: input.zone, time: "12:00" }`,
	}
}

// openClockTell are the tool calls of a model that opens messages, reads the
// clock and tells bob.
func openClockTell() []llmstandin.ToolCall {
	return []llmstandin.ToolCall{
		{Name: "open", Args: `{"toolkit": "messages"}`},
		{Name: "clock", Args: `{"zone": "UTC"}`},
		{Name: "tell", Args: `{"chat": "2", "text": "the build is green"}`},
	}
}

// traceProject is the starter with Telegram's stand-in, hearing these
// messages, a lookup that knows the people by their Telegram IDs, and the
// agent above asking a stand-in model.
func traceProject(t *testing.T, w *world, messages ...map[string]any) (string, *llmstandin.Server) {
	t.Helper()
	w.secret("TEST_KEY", "sk-test")
	dir := copyStarter(t)
	model, address := toolModel(t, "done: told bob", openClockTell()...)
	files := traceHelper(address, "You help alice.")
	files[".rook/telegram/telegram.js"] = heard(messages...)
	files["people/find.yaml"] = telegramFind
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

// The trail of one message that leads to two tools and an approval: a person
// can go from the router to the conversation, from the conversation to its
// turn, from the turn to the agent, and from the agent to each tool's name,
// input and result, the held call, its approval and the question that
// approval asked, and see who answered it.
func TestTraceMessageToToolsAndApproval(t *testing.T) {
	w := newWorld(t)
	dir, _ := traceProject(t, w, from("1", "tell bob the build is green", 10))
	w.sync(dir)
	question := w.question(map[string]bool{})
	w.tap(question.ID, "1", "yes")
	w.until("the reply said", func() bool { return sayings(t, w)["done: told bob"] != nil })

	router := w.kept(router("telegram"))
	// 1. router -> conversation
	conversation := lastOf(t, "conversation", w.sentFrom(router, instance.AskEvent, location("conversation")))
	if run := w.show(conversation); run.Cause.Run != router || run.Key != "telegram:1:1:assistant" || run.Title != "telegram dm: tell bob the build is green" {
		t.Errorf("the conversation is %+v", run)
	}
	// 2. conversation -> its turn, and the turn -> the agent, by its call
	var turn string
	for _, run := range stubs(t, w, "turn") {
		if run.Parent == conversation {
			turn = run.ID
		}
	}
	agent := lastOf(t, "agent", w.sentFrom(turn, instance.AskCall, location("agents/assistant")))
	if run := w.show(agent); run.Cause.Run != turn || run.Location != location("agents/assistant") || run.Title != "alice: tell bob the build is green" {
		t.Errorf("the agent is %+v", run)
	}
	// 3. agent -> each tool: its name and input as asked, and its result
	var names []string
	for _, out := range w.arrived(agent, "done", "turn") {
		for _, call := range out.(map[string]any)["calls"].([]any) {
			c := call.(map[string]any)
			names = append(names, fmt.Sprint(c["name"], c["args"]))
		}
	}
	if want := []string{"openmap[toolkit:messages]", "clockmap[zone:UTC]", "tellmap[chat:2 text:the build is green]"}; !slices.Equal(names, want) {
		t.Errorf("the model's calls in the record are %q", names)
	}
	var clockInput any
	for _, a := range w.ownAsks(agent, instance.AskScript) {
		if a.Name == "clock" {
			clockInput = a.Job.Input
		}
	}
	if fmt.Sprint(clockInput) != "map[zone:UTC]" {
		t.Errorf("the clock's input in the record is %v", clockInput)
	}
	if got := w.arrived(agent, "done", "clock"); len(got) != 1 || fmt.Sprint(got[0]) != "map[time:12:00 zone:UTC]" {
		t.Errorf("the clock's result in the record is %v", got)
	}
	held := w.ownAsks(agent, instance.AskHold)
	if len(held) != 1 || held[0].Name != "tell" {
		t.Errorf("the held calls are %+v", held)
	}
	told := lastOf(t, "tell's call", w.sentFrom(agent, instance.AskCall, location(bot+"/telegram/chat/post")))
	if in := fmt.Sprint(w.show(told).Input); in != "map[in:2 text:the build is green]" {
		t.Errorf("tell's call was given %s", in)
	}
	var approval string
	for _, a := range w.ownAsks(agent, instance.AskChild) {
		if a.Name == "ask" {
			approval = a.Child
		}
	}
	if got := w.arrived(agent, "done", "ask"); len(got) != 1 || fmt.Sprint(got[0]) != "map[approved:[tell] note:alice said yes.]" {
		t.Errorf("the approval gave the agent %v", got)
	}
	// 4. approval -> question, and who answered it
	if run := w.show(approval); run.Machine != "approval" || run.Title != "Approve tell" {
		t.Errorf("the approval is %+v", run)
	}
	// The approval asks in each place with a child run, place, which calls
	// the question.
	var place string
	for _, a := range w.ownAsks(approval, instance.AskChild) {
		place = a.Child
	}
	asked := lastOf(t, "question", w.sentFrom(place, instance.AskCall, location(bot+"/telegram/chat/ask")))
	if asked != question.ID || w.show(asked).Cause.Run != place || w.show(place).Parent != approval {
		t.Errorf("the approval's place called %s, and the question is %s", asked, question.ID)
	}
	var answer any
	var press string
	for _, run := range stubs(t, w, "press") {
		if run.Parent == asked {
			answer, press = w.show(run.ID).Output, run.ID
		}
	}
	if m, _ := answer.(map[string]any); m["by"] != "alice" || m["counts"] != true {
		t.Errorf("the question's record says it was answered by %v", answer)
	}
	// 5. the record lines rook-design shows are what corbie run show --record
	// prints for these runs.
	recordsAsThePageShows(t, w, map[string]string{
		"router": router, "conversation": conversation, "turn": turn, "agent": agent,
		"approval": approval, "place": place, "question": asked, "press": press, "tell": told,
		"find": lastOf(t, "lookup", w.sentFrom(turn, instance.AskCall, location("people/find"))),
	})
}

// recordsAsThePageShows checks each record rook-design shows under Following
// a message: the page's lines for corbie run show <name> --record are printed,
// in that order, for the run of that name. A line is compared without its
// time and run, with each run's ID written as <name>, and the project at
// /my-bot.
func recordsAsThePageShows(t *testing.T, w *world, runs map[string]string) {
	t.Helper()
	var page string
	for _, p := range rook.Pages() {
		if p.Name == "rook-design" {
			page = p.Text
		}
	}
	shown := map[string][]string{}
	name := ""
	for _, line := range strings.Split(page, "\n") {
		if command, ok := strings.CutPrefix(line, "    corbie run show <"); ok && strings.HasSuffix(command, "> --record") {
			name = strings.TrimSuffix(command, "> --record")
			shown[name] = nil
		} else if text, ok := strings.CutPrefix(line, "      "); ok && name != "" {
			shown[name] = append(shown[name], text)
		} else {
			name = ""
		}
	}
	if len(shown) < 4 {
		t.Fatalf("rook-design shows the records %v", slices.Sorted(maps.Keys(shown)))
	}
	prefix := regexp.MustCompile(`^\d\d:\d\d:\d\d \S+  `)
	for name, want := range shown {
		id, ok := runs[name]
		if !ok {
			t.Errorf("rook-design shows the record of <%s>, which the test does not follow", name)
			continue
		}
		var printed []string
		for _, line := range strings.Split(w.corbie("run", "show", id, "--record"), "\n") {
			line = prefix.ReplaceAllString(line, "")
			for n, id := range runs {
				line = strings.ReplaceAll(line, id, "<"+n+">")
			}
			printed = append(printed, strings.ReplaceAll(line, project+"/", "/my-bot/"))
		}
		rest := printed
		for _, line := range want {
			i := slices.Index(rest, line)
			if i < 0 {
				t.Errorf("corbie run show <%s> --record does not print, where rook-design shows it,\n%s\nIt prints:\n%s", name, line, strings.Join(printed, "\n"))
				break
			}
			rest = rest[i+1:]
		}
	}
}

// A project with no model: the agent is a flow. It posts at the message's
// chat, calls a question for alice, and replies with her answer. The submit
// names no secret that is missing, and the trail goes from the turn to the
// flow and from the flow to the question.
func TestNoModelFlowAgentIsFollowed(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
		".rook/telegram/telegram.js": heard(from("2", "deploy now", 12)),
		"people/find.yaml":           telegramFind,
		"agents/assistant.yaml": `
			flow: desk
			input: { messages: list, medium: string, format: "any?", asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
			allow: [send:gromit/telegram/chat/post, send:gromit/telegram/chat/ask]
			stages:
			  ack:
			    job: { call: { at: gromit/telegram/chat/post }, input: { in: "${input.messages[0].chat}", text: "asking alice about ${input.messages[0].text}" } }
			  question:
			    job:
			      call: { at: gromit/telegram/chat/ask }
			      input: { in: "1", text: "${input.asker.name} asks: ${input.messages[0].text}. Allow?", choices: ["yes", "no"], approvers: "messages:approve" }
			output: { reply: "alice said ${stages.question.output.choice}" }
		`,
	})
	os.Remove(filepath.Join(dir, "agents/helper.yaml"))
	os.Remove(filepath.Join(dir, "agents/team.yaml"))
	receipt := w.syncAt(w.Client, dir, project)
	if !receipt.Taken {
		t.Fatalf("refused: %s", receipt.Refused)
	}
	// The harness's stand-in leaves TELEGRAM_TOKEN in the service's machines;
	// that is the service's own secret, and no model's.
	for _, missing := range receipt.Tree.Missing {
		if !strings.Contains(missing, "TELEGRAM_TOKEN") {
			t.Errorf("a project with no model is missing %v", missing)
		}
	}
	question := w.question(map[string]bool{})
	w.tap(question.ID, "1", "yes")
	w.until("the reply said", func() bool { return sayings(t, w)["alice said yes"] != nil })
	if to := sayings(t, w)["asking alice about deploy now"]; to["in"] != "2" {
		t.Errorf("the flow posted in %v", to)
	}
	desk := stubs(t, w, "desk")
	if len(desk) != 1 {
		t.Fatalf("the desk ran %d times", len(desk))
	}
	if asked := lastOf(t, "question", w.sentFrom(desk[0].ID, instance.AskCall, location(bot+"/telegram/chat/ask"))); asked != question.ID || w.show(asked).Cause.Run != desk[0].ID {
		t.Errorf("the desk called %s, the question is %s", asked, question.ID)
	}
	if w.show(w.show(desk[0].ID).Cause.Run).Machine != "turn" {
		t.Errorf("the desk was not started by a conversation's turn")
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
	agent := stubs(t, w, "assistant")[0].ID
	address := strings.TrimSuffix(strings.SplitN(strings.Split(readFile(t, dir, "agents/assistant.yaml"), "address: \"")[1], "\"", 2)[0], "/")
	write(t, dir, map[string]string{"agents/assistant.yaml": traceHelper(address, "You help alice, version two.")["agents/assistant.yaml"]})
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
	w.sendTo("conversation.yaml", key("1", "1"), "message", from("1", "again", 11))
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

// A flow agent deleted while its question waits: the submit cancels the agent
// and, with it, the question it called. The asker is told the answer failed,
// and a later message that the bot has no agent yet; the failed turn names the
// agent's location.
func TestDeletedAgentWhileQuestionWaits(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
		".rook/telegram/telegram.js": heard(from("2", "deploy now", 12)),
		"agents/assistant.yaml": `
			flow: desk
			input: { messages: list, medium: string, format: "any?", asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
			allow: [send:gromit/telegram/chat/ask]
			stages:
			  question:
			    job:
			      call: { at: gromit/telegram/chat/ask }
			      input: { in: "1", text: "bob asks: ${input.messages[0].text}. Allow?", choices: ["yes", "no"], approvers: "messages:approve" }
			output: { reply: "alice said ${stages.question.output.choice}" }
		`,
	})
	w.sync(dir)
	question := w.question(map[string]bool{})
	desk := stubs(t, w, "desk")[0].ID
	if err := os.Remove(filepath.Join(dir, "agents/assistant.yaml")); err != nil {
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
	w.until("the asker told", func() bool { return sayings(t, w)["That could not be answered."] != nil })
	w.sendTo("conversation.yaml", key("2", "2"), "message", from("2", "again", 13))
	w.until("the asker told there is no agent", func() bool { return sayings(t, w)["This bot has no agent yet."] != nil })
	failed := false
	for _, run := range stubs(t, w, "turn") {
		failed = failed || strings.Contains(w.show(run.ID).Error, "agents/assistant")
	}
	if !failed {
		t.Errorf("no failed turn names the agent's location")
	}
}

// A tool that fails is followed: the agent's record shows the tool's failure
// with its error, and the model is given it.
func TestFailedToolIsFollowed(t *testing.T) {
	w := newWorld(t)
	dir, model := traceProject(t, w, from("1", "what time is it", 10))
	write(t, dir, map[string]string{"toolkits/clock.js": `throw new Error("the clock is broken")`})
	w.sync(dir)
	w.until("a question", func() bool { return len(questions(w)) == 1 })
	agent := stubs(t, w, "assistant")[0].ID
	failed := fmt.Sprint(w.arrived(agent, "failed", "clock"))
	if !strings.Contains(failed, "the clock is broken") {
		t.Errorf("the record shows the clock's failure as %s", failed)
	}
	text, _ := json.Marshal(model.Calls[2].Messages)
	if !strings.Contains(string(text), "the clock is broken") {
		t.Errorf("the model was not told the clock failed: %s", text)
	}
}

// A tap by someone whose scopes lack the approvers' does not answer a
// question, and the question's record shows the tap, who it was from, and that
// it did not count. Then alice's tap counts.
func TestTapByAStrangerIsShownAndIgnored(t *testing.T) {
	w := newWorld(t)
	dir, _ := traceProject(t, w, from("2", "tell bob", 10))
	write(t, dir, map[string]string{"people/find.yaml": telegramFind})
	w.sync(dir)
	question := w.question(map[string]bool{})
	w.tap(question.ID, "9", "yes")
	w.until("the tap checked", func() bool {
		for _, run := range stubs(t, w, "press") {
			if run.Parent == question.ID && run.Status == "done" {
				return true
			}
		}
		return false
	})
	if run := w.show(question.ID); run.Status != "running" {
		t.Fatalf("the stranger's tap ended the question: %s", run.Status)
	}
	var out any
	for _, run := range stubs(t, w, "press") {
		out = w.show(run.ID).Output
	}
	if m, _ := out.(map[string]any); m["by"] != "Stranger" || m["counts"] != false {
		t.Errorf("the tap's record says %v", out)
	}
	w.tap(question.ID, "1", "yes")
	w.until("the reply said", func() bool { return sayings(t, w)["done: told bob"] != nil })
}

// A conversation's next message, sent while the approval waits, waits its
// turn: the conversation keeps it, and once the yes is given a second agent
// run answers it, given that message alone, with the first turn in its
// conversation.
func TestSecondMessageWaitsForTheApproval(t *testing.T) {
	w := newWorld(t)
	dir, _ := traceProject(t, w, from("1", "tell bob", 10))
	w.sync(dir)
	question := w.question(map[string]bool{})
	w.sendTo("conversation.yaml", key("1", "1"), "message", from("1", "and the time?", 11))
	quiet()
	if n := len(stubs(t, w, "assistant")); n != 1 {
		t.Errorf("%d agent runs while the first waits", n)
	}
	w.tap(question.ID, "1", "yes")
	second := w.question(map[string]bool{question.ID: true})
	agents := agentRuns(w, "1", "1")
	if len(agents) != 2 {
		t.Fatalf("%d agent runs", len(agents))
	}
	if got := w.texts(agents[1]); !slices.Equal(got, []string{"and the time?"}) {
		t.Errorf("the second agent was given %q", got)
	}
	if w.show(agents[1].ID).Input.(map[string]any)["conversation"] == nil {
		t.Errorf("the second agent was given no conversation")
	}
	w.tap(second.ID, "1", "no")
}

// The router's file removed and put back while a question waits: the router
// stops and starts, the conversation, agent and question go on, and a tap
// after it answers the question.
func TestRouterRestartedWhileApprovalWaits(t *testing.T) {
	w := newWorld(t)
	dir, _ := traceProject(t, w, from("1", "tell bob", 10))
	w.sync(dir)
	question := w.question(map[string]bool{})
	routerFile := readFile(t, dir, router("telegram"))
	removeListener(t, dir)
	w.sync(dir)
	if run := w.show(question.ID); run.Status != "running" {
		t.Fatalf("the question is %s", run.Status)
	}
	if err := os.WriteFile(filepath.Join(dir, router("telegram")), []byte(routerFile), 0o644); err != nil {
		t.Fatal(err)
	}
	w.sync(dir)
	w.tap(question.ID, "1", "yes")
	w.until("the reply said", func() bool { return sayings(t, w)["done: told bob"] != nil })
}

// Every run a message led to is listed with its location, and each names the
// run that started it, or is its child, so a person with only run ls and run
// show can walk the trail backwards from the question to the router.
func TestTrailWalksBackwardsFromTheQuestion(t *testing.T) {
	w := newWorld(t)
	dir, _ := traceProject(t, w, from("1", "tell bob", 10))
	w.sync(dir)
	question := w.question(map[string]bool{})
	var chain []string
	for id := question.ID; id != ""; {
		run := w.show(id)
		chain = append(chain, run.Machine)
		next := run.Cause.Run
		if next == "" {
			next = run.Parent
		}
		id = next
	}
	want := []string{"chat-ask", "place", "approval", "assistant", "turn", "conversation", "gromit"}
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
	files[".rook/telegram/telegram.js"] = heard(from("1", "hello", 10))
	write(t, dir, files)
	receipt := w.syncAt(w.Client, dir, project)
	if !strings.Contains(fmt.Sprint(receipt.Tree.Missing), "TEST_KEY") {
		t.Errorf("the submit names as missing %v", receipt.Tree.Missing)
	}
	w.until("the asker told", func() bool { return sayings(t, w)["That could not be answered."] != nil })
	agents := stubs(t, w, "assistant")
	if len(agents) != 1 {
		t.Fatalf("%d agent runs", len(agents))
	}
	if run := w.show(agents[0].ID); !strings.Contains(run.Error+run.Refused, "TEST_KEY") {
		t.Errorf("the agent's run says %q %q", run.Error, run.Refused)
	}
}
