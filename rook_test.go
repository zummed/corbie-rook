package rook_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	rook "github.com/zummed/corbie-rook"
	"github.com/zummed/corbie/client"
	"github.com/zummed/corbie/instance"
	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// bot is the name of the bot in every test's project.
const bot = "gromit"

// router gives the path of a service's router in a test's project.
func router(service string) string { return bot + "/" + service + "/" + bot + ".yaml" }

// telegramScript stands in for telegram.js. It hears three messages from one
// person and one from a stranger, and then fails, which rests the listener.
const telegramScript = `
	if (input.do === "hear") {
	  if (input.cursor !== null) throw new Error("nothing more")
	  emit("message", { service: "telegram", text: "one", chat: "1", user: "1", name: "Alice", place: "dm", answers: "1:10" })
	  emit("message", { service: "telegram", text: "two", chat: "1", user: "1", name: "Alice", place: "dm", answers: "1:11" })
	  emit("message", { service: "telegram", text: "three", chat: "1", user: "1", name: "Alice", place: "dm", answers: "1:12" })
	  emit("message", { service: "telegram", text: "hello", chat: "9", user: "9", name: "Stranger", place: "dm", answers: "9:13" })
	  return { cursor: 1 }
	}
	if (input.do === "ask") return { sent: [{ chat: input.in, id: 101 }] }
	if (input.do === "post") return { sent: [input.answers || ""] }
	return {}
`

// helperStub stands in for the agent at agents/assistant: it replies with what
// it was said, a line for each message of the turn, and counts the turns of
// its conversation.
const helperStub = `
	statechart: assistant
	input: { messages: list, medium: string, format: "any?", asker: map, thread: "any?", attachments: "any?", conversation: "any?" }
	initial: e
	states:
	  e: { end: done, output: { reply: "${input.messages.map((m) => m.text).join('\\n')}", conversation: "${(input.conversation || 0) + 1}" } }
`

// exampleProject makes a project with a bot on the services given, as rook.Init
// does, and puts testdata's files in it: the agents helper and team, the
// toolkit messages, two people's scopes and a .corbieignore that lists
// toolkits/.
func exampleProject(dir string, services []string) error {
	if err := rook.Init(dir, bot, services); err != nil {
		return err
	}
	return filepath.WalkDir("testdata", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		text, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		to := filepath.Join(dir, strings.TrimPrefix(name, "testdata/"))
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		return os.WriteFile(to, text, 0o644)
	})
}

// realAgent puts testdata's helper in a project as the agent the conversation
// calls, agents/assistant, with a stand-in model at an address.
func realAgent(t *testing.T, dir, address string) {
	t.Helper()
	text, err := os.ReadFile("testdata/agents/helper.yaml")
	if err != nil {
		t.Fatal(err)
	}
	written := strings.Replace(string(text), "model: { name: test-model, service: anthropic, secret: TEST_KEY }",
		"model: { name: test-model, service: anthropic, address: "+address+", secret: TEST_KEY }", 1)
	written = strings.Replace(written, "agent: helper\n", "agent: assistant\n", 1)
	if err := os.WriteFile(filepath.Join(dir, "agents/assistant.yaml"), []byte(written), 0o644); err != nil {
		t.Fatal(err)
	}
}

// write writes files into a project, each dedented.
func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(dedent(text)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// copyStarter makes a project with a bot on Telegram, with Telegram's script
// and the agent at agents/assistant standing in for what needs a network or a
// model.
func copyStarter(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "project")
	if err := exampleProject(dir, []string{"telegram"}); err != nil {
		t.Fatal(err)
	}
	write(t, dir, map[string]string{
		".rook/telegram/telegram.js": telegramScript,
		"agents/assistant.yaml":      helperStub,
	})
	// The stand-in script uses no secret, and the test's controller holds none.
	cut(t, filepath.Join(dir, ".rook/telegram"), `, "secret:TELEGRAM_TOKEN@net:api.telegram.org"`)
	return dir
}

// cut removes a text from every machine in a directory and below it.
func cut(t *testing.T, dir, text string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".yaml") {
			return err
		}
		src, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(name, []byte(strings.ReplaceAll(string(src), text, "")), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// heard stands in for telegram.js: its listener hears these once, then rests.
// One with a run is a tap, and any other a message. A question gives the
// message it posted, so a close has something to close.
func heard(events ...map[string]any) string {
	var emits []string
	for _, e := range events {
		data, _ := json.Marshal(e)
		name := "message"
		if _, ok := e["run"]; ok {
			name = "answer"
		}
		emits = append(emits, "emit(\""+name+"\", "+string(data)+")")
	}
	return `
		if (input.do === "hear") {
		  if (input.cursor !== null) throw new Error("nothing more")
		  ` + strings.Join(emits, "\n\t\t  ") + `
		  return { cursor: 1 }
		}
		if (input.do === "ask") return { sent: [{ chat: input.in, id: 101 }] }
		if (input.do === "send") return { sent: input.in + ":1" }
		if (input.do === "post") return { sent: [input.answers || ""] }
		return {}
	`
}

// from is a message from a person in their own chat with the bot, the n-th
// of that chat.
func from(id, text string, n int) map[string]any {
	return map[string]any{"service": "telegram", "text": text, "chat": id, "user": id, "name": names[atoi(id)], "place": "dm", "answers": fmt.Sprintf("%s:%d", id, n)}
}

// heardIn is a message from a person in a chat, which is the place given.
func heardIn(chat, user, place, text string) map[string]any {
	return map[string]any{"service": "telegram", "text": text, "chat": chat, "user": user, "name": names[atoi(user)], "place": place, "answers": chat + ":10"}
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// removeListener takes the listener out of the Telegram router, the one
// machine the project keeps running: the router runs and hears nothing.
func removeListener(t *testing.T, dir string) {
	t.Helper()
	removeListenerOf(t, dir, "telegram")
}

// removeListenerOf takes the listener out of a service's router.
func removeListenerOf(t *testing.T, dir, service string) {
	t.Helper()
	setSite(t, dir, router(service), "    job: { machine: .rook/"+service+"/listen.yaml, restart: { after: always } }\n", "")
}

// answer sends an answer to a question to the router of a service, as its
// listener does, and the router sends it to the question.
func (w *world) answer(service, question string, data map[string]any) {
	w.t.Helper()
	data["run"] = question[strings.Index(question, "/")+1:]
	w.send(w.kept(router(service)), "answer", data)
}

// keyed gives the runs of a keyed location of the project by their key.
func keyed(w *world, path string) map[string][]*client.Run {
	out := map[string][]*client.Run{}
	for _, run := range w.at(path) {
		if run.Parent == "" {
			out[run.Key] = append(out[run.Key], run)
		}
	}
	return out
}

// questions gives the questions waiting on people: the runs of the bot's
// Telegram question that wait for a tap.
func questions(w *world) []*client.Run {
	var out []*client.Run
	for _, run := range w.at(bot + "/telegram/chat/ask.yaml") {
		if run.Status == "running" && run.Timers == 1 {
			out = append(out, run)
		}
	}
	return out
}

// question waits for a question that is not among those seen, and gives it.
func (w *world) question(seen map[string]bool) *client.Run {
	w.t.Helper()
	var found *client.Run
	w.until("a question", func() bool {
		for _, run := range questions(w) {
			if !seen[run.ID] {
				found = run
			}
		}
		return found != nil
	})
	seen[found.ID] = true
	return found
}

// tap answers a question as a tap on Telegram by the person with an ID,
// which the router sends to the question.
func (w *world) tap(question, user, choice string) {
	w.t.Helper()
	w.answer("telegram", question, map[string]any{"service": "telegram", "user": user, "name": names[atoi(user)], "choice": choice})
}

// telegramAsker is a machine that asks a question on Telegram, in chat 2, of
// people whose scopes hold house:approve, and gives the choice. It is under
// agents/, whose machines the placed question takes calls from.
const telegramAsker = `
	flow: asker
	allow: [send:gromit/telegram/chat/ask]
	stages:
	  asking: { job: { call: { at: gromit/telegram/chat/ask }, input: { in: "2", text: "Well?", choices: ["yes", "no"], approvers: "house:approve" } } }
	output: ${stages.asking.output.choice}
`

// A question is a run of a bot's chat/ask that a machine calls, and a tap by a
// person whose scopes, by the lookup, hold its approvers answers it. A tap by
// anyone else changes nothing.
func TestAskPersonIsAnsweredByATap(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	write(t, dir, map[string]string{
		"agents/asker.yaml":  telegramAsker,
		"people/find.yaml":   telegramFind,
		"people/scopes.yaml": "alice@example.com: { name: alice, scopes: [house] }\nbob@example.com: { name: bob, scopes: [house:approve] }\n",
	})
	removeListener(t, dir)
	w.sync(dir)
	id := w.start("agents/asker.yaml", nil)
	question := w.question(map[string]bool{})
	for _, from := range []string{"3", "1"} {
		w.tap(question.ID, from, "yes")
		time.Sleep(10 * interval)
		if run := w.show(id); run.Status != "running" {
			t.Fatalf("a tap by %s ended the asker: %+v", from, run)
		}
	}
	w.tap(question.ID, "2", "no")
	w.until("the asker done", func() bool { return w.show(id).Status == "done" })
	if got := w.show(id).Output; got != "no" {
		t.Fatalf("the choice was %v", got)
	}
	if run := w.show(question.ID); run.StartedBy == "" || !reflect.DeepEqual(run.Output, map[string]any{"choice": "no", "by": "bob"}) {
		t.Errorf("the question does not name the run that called it, or gave %v: %+v", run.Output, run)
	}
	w.titled(map[string][]string{"telegram-chat-ask": {"Well?"}})
}

// A project with a bot on every service reads: the routers are singletons
// that receive from no location, the conversation is keyed, nothing under
// .rook has a location, and .rook/, people/scopes.yaml and testdata's
// toolkits/ are listed. Every machine Rook
// ships reads when it is placed, and its two included files read in an agent.
func TestProjectReads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := exampleProject(dir, rook.Services()); err != nil {
		t.Fatal(err)
	}
	placed := map[string]string{
		"agents/assistant.yaml": "agent: assistant\ninclude: .rook/agent.yaml\nmodel: { name: test-model, service: anthropic, secret: TEST_KEY }\nsystem: You help.\n",
		"agents/interested.yaml": "agent: interested\ninclude: .rook/filters/model.yaml\nmodel: { name: test-model, service: anthropic, secret: TEST_KEY }\n" +
			"system:\n  - { include: .rook/filters/model.yaml#system }\n  - The bot answers questions about the build.\n",
	}
	err := filepath.WalkDir(filepath.Join(dir, rook.Shipped), func(name string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(filepath.Join(dir, rook.Shipped), name)
		if err != nil || d.IsDir() || !strings.HasSuffix(name, ".yaml") || rel == "agent.yaml" || rel == "filters/model.yaml" {
			return err
		}
		placed["placed/"+rel] = "include: .rook/" + rel + "\n"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(placed) < 25 {
		t.Fatalf("Rook ships only %d machines", len(placed))
	}
	write(t, dir, placed)
	tree, err := client.ReadTree(dir, project)
	if err != nil {
		t.Fatal(err)
	}
	lifetimes := map[string][]string{}
	for _, m := range tree.Machines {
		if m.Problem != nil {
			t.Errorf("%s: %v", m.Path, m.Problem)
			continue
		}
		if strings.HasPrefix(m.Path, ".rook/") {
			t.Errorf("%s has a location", m.Path)
		}
		life := m.Draft.Definition.Lifetime
		lifetimes[life] = append(lifetimes[life], m.Location)
	}
	want := map[string][]string{
		"singleton": {"/project/gromit/gitlab/gromit", "/project/gromit/jira/gromit", "/project/gromit/slack/gromit", "/project/gromit/telegram/gromit"},
		"keyed":     {"/project/conversation"},
	}
	for life, locations := range want {
		slices.Sort(lifetimes[life])
		if !reflect.DeepEqual(lifetimes[life], locations) {
			t.Errorf("%s: %v", life, lifetimes[life])
		}
	}
	if !reflect.DeepEqual(tree.Listed, []string{".rook/", "people/scopes.yaml", "toolkits/"}) {
		t.Errorf("the listed files are %q", tree.Listed)
	}
	for _, m := range tree.Machines {
		if m.Path == "agents/assistant.yaml" && m.Problem == nil && !reflect.DeepEqual(m.Draft.Definition.Receive, map[string][]string{"": {"conversation"}}) {
			t.Errorf("an agent that includes .rook/agent.yaml receives from %v", m.Draft.Definition.Receive)
		}
		if m.Draft != nil && slices.Contains(want["singleton"], m.Location) && !reflect.DeepEqual(m.Draft.Definition.Receive, map[string][]string{"": {}}) {
			t.Errorf("the router %s receives from %v", m.Path, m.Draft.Definition.Receive)
		}
	}
}

// init writes the starting files, the conversation for its bot, and a router
// and placed calls for each service named, and none for another; .rook holds
// every service's machines.
func TestInitWritesTheTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := rook.Init(dir, "gromit", []string{"slack", "gitlab"}); err != nil {
		t.Fatal(err)
	}
	var got []string
	err := filepath.WalkDir(dir, func(name string, d fs.DirEntry, err error) error {
		rel, _ := filepath.Rel(dir, name)
		switch {
		case err != nil:
			return err
		case rel == rook.Shipped:
			return filepath.SkipDir
		case !d.IsDir():
			got = append(got, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		".corbieignore", ".mcp.json", "AGENTS.md", "CLAUDE.md", "conversation.yaml",
		"gromit/gitlab/chat/ask.yaml", "gromit/gitlab/chat/post.yaml", "gromit/gitlab/files/get.yaml", "gromit/gitlab/gromit.yaml",
		"gromit/slack/assistant/status.yaml", "gromit/slack/chat/ask.yaml", "gromit/slack/chat/post.yaml", "gromit/slack/files/get.yaml", "gromit/slack/gromit.yaml",
		"people/find.yaml", "people/scopes.yaml",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("init wrote %q", got)
	}
	for _, name := range []string{"conversation.yaml", "gromit/slack/gromit.yaml", "gromit/slack/chat/ask.yaml"} {
		if text, _ := os.ReadFile(filepath.Join(dir, name)); strings.Contains(string(text), "BOT") || !strings.Contains(string(text), "gromit") {
			t.Errorf("%s does not name the bot:\n%s", name, text)
		}
	}
	for _, service := range rook.Services() {
		if _, err := os.Stat(filepath.Join(dir, rook.Shipped, service, "listen.yaml")); err != nil {
			t.Errorf(".rook lacks %s's listener: %v", service, err)
		}
	}
}

// No file Rook writes into a project names a model, or a model's key: a
// project with no model works, and one with a model names it.
func TestNoModelInWhatRookWrites(t *testing.T) {
	for _, top := range []string{"shipped", "starter"} {
		filepath.WalkDir(top, func(name string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			text, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			for n, line := range strings.Split(string(text), "\n") {
				code := !strings.HasPrefix(strings.TrimSpace(line), "#") && !strings.HasPrefix(strings.TrimSpace(line), "//")
				for _, word := range []string{"claude-code", "CLAUDE_TOKEN", "ANTHROPIC", "anthropic", "sonnet", "haiku", "opus"} {
					if strings.Contains(line, word) {
						t.Errorf("%s:%d names %s", name, n+1, word)
					}
				}
				if code && strings.Contains(line, "model:") {
					t.Errorf("%s:%d writes model:", name, n+1)
				}
			}
			return nil
		})
	}
}

// Init again rewrites .rook, adds to .corbieignore only the lines it lacks,
// writes back a file of Named or of a service named that is not there, and
// changes nothing else of the person's.
func TestInitAgainKeepsThePersonsFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := rook.Init(dir, "gromit", []string{"slack"}); err != nil {
		t.Fatal(err)
	}
	mine := "bo@example.com: { name: Bo, scopes: [ops] }\n"
	stale := filepath.Join(dir, ".rook/stale.yaml")
	for name, text := range map[string]string{"people/scopes.yaml": mine, "gromit/slack/gromit.yaml": "# mine\n", ".corbieignore": "build/", ".rook/slack/listen.yaml": "changed", ".rook/stale.yaml": "old"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"AGENTS.md", "gromit/slack/chat/post.yaml"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := rook.Init(dir, "gromit", []string{"slack"}); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]string{"people/scopes.yaml": mine, "gromit/slack/gromit.yaml": "# mine\n", ".corbieignore": "build/\n.rook/\npeople/scopes.yaml\n"} {
		if got, _ := os.ReadFile(filepath.Join(dir, name)); string(got) != want {
			t.Errorf("%s is now %q", name, got)
		}
	}
	for _, name := range []string{"AGENTS.md", "gromit/slack/chat/post.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s, which init writes when it is not there, was not written", name)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".rook/AGENTS.md")); !strings.HasPrefix(string(got), "Rook: ") {
		t.Errorf(".rook/AGENTS.md is %q, and not the page rook", got)
	}
	if differ, err := rook.Differ(dir); err != nil || !reflect.DeepEqual(differ, []string{".corbieignore", "gromit/slack/gromit.yaml", "people/scopes.yaml"}) {
		t.Errorf("the files not as Rook starts them are %v, %v", differ, err)
	}
	if text := rook.Pages()[3].Text; !strings.Contains(text, "\nconversation.yaml\n-----------------\n\n    # ") || !strings.Contains(text, "\n<bot>/jira/<bot>.yaml\n") {
		t.Error("the page rook-starter lacks a starting file")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, ".rook/slack/listen.yaml")); string(got) == "changed" {
		t.Error(".rook was not rewritten")
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("a file Rook no longer ships stayed in .rook")
	}
	// A starting file that is not one of Named stays removed, and so does a
	// service's file when init does not name the service.
	removed := []string{"people/scopes.yaml", "people/find.yaml", "CLAUDE.md", ".mcp.json", "conversation.yaml", "gromit/slack/chat/ask.yaml"}
	for _, name := range removed {
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := rook.Init(dir, "", nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range removed {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s, a starting file the person removed, came back", name)
		}
	}
}

// init refuses a service there is not, a service with no bot, and a bot whose
// name is no machine's name, and writes nothing.
func TestInitRefusesWhatIsNot(t *testing.T) {
	for _, c := range []struct {
		bot      string
		services []string
		want     string
	}{
		{"gromit", []string{"carrier-pigeon"}, `there is no service "carrier-pigeon"`},
		{"", []string{"slack"}, "a service needs a bot"},
		{"my bot", []string{"slack"}, `"my bot" is not a bot's name`},
		{"../up", nil, `"../up" is not a bot's name`},
	} {
		dir := filepath.Join(t.TempDir(), "project")
		if err := rook.Init(dir, c.bot, c.services); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Init(%q, %v) gave %v", c.bot, c.services, err)
		}
		if _, err := os.Stat(dir); err == nil {
			t.Errorf("Init(%q, %v) wrote a directory", c.bot, c.services)
		}
	}
}

// A project made by init, with Telegram's script standing in, runs: the router
// runs the listener as its child, and sends each message to the conversation
// keyed by the service, the chat, the person and the agent; the reply is
// posted in the chat it came from.
func TestRouterSendsEachPersonToTheirConversation(t *testing.T) {
	w := newWorld(t)
	dir := copyStarter(t)
	w.sync(dir)
	w.kept(router("telegram"))
	w.until("two conversations", func() bool {
		all := keyed(w, "conversation.yaml")
		return len(all["telegram:1:1:assistant"]) == 1 && len(all["telegram:9:9:assistant"]) == 1 && len(all) == 2
	})
	posted := map[string]bool{}
	w.until("four replies posted", func() bool {
		for _, run := range w.at(bot + "/telegram/chat/post.yaml") {
			if run.Status == "done" {
				input := w.show(run.ID).Input.(map[string]any)
				for _, line := range strings.Split(fmt.Sprint(input["text"]), "\n") {
					posted[fmt.Sprint(input["in"], " ", line)] = true
				}
			}
		}
		return len(posted) == 4
	})
	for _, want := range []string{"1 one", "1 two", "1 three", "9 hello"} {
		if !posted[want] {
			t.Errorf("%q was not posted: %v", want, posted)
		}
	}
}

// sayings gives everything a project's services were told to post, by its
// text, with the `in` each post was given.
func sayings(t *testing.T, w *world) map[string]map[string]any {
	t.Helper()
	runs, err := w.List(context.Background(), client.Filter{Finished: true})
	if err != nil {
		t.Fatal(err)
	}
	said := map[string]map[string]any{}
	for _, run := range runs {
		if !strings.HasSuffix(run.Machine, "-chat-post") {
			continue
		}
		input := w.show(run.ID).Input.(map[string]any)
		text := input["text"].(string)
		if said[text] == nil {
			said[text] = map[string]any{}
		}
		if address, ok := input["address"].(map[string]any); ok {
			maps.Copy(said[text], address)
		}
		if in, ok := input["in"]; ok {
			said[text]["in"] = in
		}
	}
	return said
}

// approving makes a project whose listener hears what the script says, and
// whose agent at agents/assistant asks a stand-in model, which opens the
// toolkit messages, tells bob hello, and then says told. The lookup knows
// the people by their Telegram IDs: alice may ask, and carol may approve.
func approving(t *testing.T, w *world, script string) (string, *llmstandin.Server) {
	t.Helper()
	w.secret("TEST_KEY", "sk-test")
	dir := copyStarter(t)
	replies := []llmstandin.Reply{
		{Calls: []llmstandin.ToolCall{{ID: "c1", Name: "open", Args: `{"toolkit": "messages"}`}}},
		{Calls: []llmstandin.ToolCall{{ID: "c2", Name: "tell", Args: `{"chat": "2", "text": "hello"}`}}},
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
	write(t, dir, map[string]string{
		".rook/telegram/telegram.js": script,
		"people/find.yaml":           telegramFind,
		"people/scopes.yaml":         "alice@example.com: { name: alice, scopes: [messages] }\ncarol@example.com: { name: carol, scopes: [messages:approve] }\n",
	})
	return dir, model
}

// oneMessage stands in for telegram.js, and hears one message.
const oneMessage = `
	if (input.do === "hear") {
	  if (input.cursor !== null) throw new Error("nothing more")
	  emit("message", { service: "telegram", text: "one", chat: "1", user: "1", name: "Alice", place: "dm", answers: "1:10" })
	  return { cursor: 1 }
	}
	if (input.do === "ask") return { sent: [{ chat: input.in, id: 101 }] }
	if (input.do === "post") return { sent: [input.answers || ""] }
	return {}
`

// undelivered gives what a run's record notes of the events it sent that were
// not delivered.
func undelivered(w *world, id string) []string {
	var out []string
	for _, ask := range w.asked(id, instance.AskNote) {
		if strings.Contains(ask.Text, "was not delivered") {
			out = append(out, ask.Text)
		}
	}
	return out
}
