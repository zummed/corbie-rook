package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	rook "github.com/zummed/corbie-rook/rook"
	"github.com/zummed/corbie/client/cli"
)

func run(t *testing.T, in string, args ...string) (string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	status := cli.Program("rook", []cli.Command{initCommand}, args, strings.NewReader(in), &out, &errOut)
	return out.String() + errOut.String(), status
}

// corbie mcp, allowed rook init, reads the manual as JSON: the command, and
// the page rook, which it tells a model.
func TestCorbieMCPFindsInitAndThePageRook(t *testing.T) {
	out, status := run(t, "", "manual", "--json")
	var pages []struct{ Name, Group, Text string }
	if err := json.Unmarshal([]byte(out), &pages); status != 0 || err != nil {
		t.Fatalf("corbie rook manual --json ended %d: %v\n%s", status, err, out)
	}
	found := map[string]bool{}
	for _, page := range pages {
		found[page.Group+" "+page.Name] = page.Name != "rook" || strings.Contains(page.Text, "rook-design")
	}
	if !found["Commands corbie rook init"] || !found["Rook rook"] {
		t.Fatalf("the manual holds %v", found)
	}
}

func TestEveryPageTheBriefingNamesIsThere(t *testing.T) {
	text, _ := run(t, "", "manual", "rook")
	named := regexp.MustCompile("`corbie (?:rook )?manual\\s+([a-z-]+)`").FindAllStringSubmatch(text, -1)
	if len(named) < 5 {
		t.Fatalf("found only %v", named)
	}
	// The machine types and plugins main.go links, which the page names by kind.
	for _, page := range append(named, []string{"", "agent"}, []string{"", "statechart"}, []string{"", "http"}) {
		if out, status := run(t, "", "manual", page[1]); status != 0 {
			t.Errorf("page %s: %s", page[1], out)
		}
	}
}

// The page rook-project shows what corbie rook init printed: making the
// project with Slack, then adding GitLab in it, which names the router's
// placeholder. It shows what corbie test printed in the project on Slack
// alone: the locations the conversation calls that it lacks, and exit 0.
func TestThePageShowsWhatInitPrints(t *testing.T) {
	var page string
	for _, p := range rook.Pages() {
		if p.Name == "rook-project" {
			page = p.Text
		}
	}
	// shown gives what the page shows a command printed.
	shown := func(command string) string {
		_, after, ok := strings.Cut(page, "\n    "+command+"\n")
		if !ok {
			t.Fatalf("rook-project does not show %q", command)
		}
		var lines []string
		for _, line := range strings.Split(after, "\n") {
			if line != "" && !strings.HasPrefix(line, "      ") {
				break
			}
			lines = append(lines, strings.TrimPrefix(line, "      "))
		}
		return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
	}
	t.Chdir(t.TempDir())
	for _, c := range []struct{ in, command string }{
		{".", "corbie rook init my-bot --bot gromit --services slack"},
		{"my-bot", "corbie test ."},
		{".", "corbie rook init . --bot gromit --services gitlab"},
	} {
		t.Chdir(c.in)
		var out string
		var status int
		if args := strings.Fields(c.command); args[1] == "rook" {
			out, status = run(t, "", args[2:]...)
		} else {
			var stdout, stderr bytes.Buffer
			status = cli.Main(args[1:], strings.NewReader(""), &stdout, &stderr)
			out = stdout.String() + stderr.String()
		}
		if want := shown(c.command); status != 0 || out != want {
			t.Errorf("%s ended %d and printed\n%s\nand the page shows\n%s", c.command, status, out, want)
		}
	}
	if !strings.Contains(shown("corbie rook init . --bot gromit --services gitlab"), "  set the listener's projects: or group: in gromit/gitlab/gromit.yaml\n") {
		t.Error("the page shows no line for the GitLab router's placeholder")
	}
}

// init prints the secrets and the values each chosen service needs, set at
// the service's directory under the bot's.
func TestInitPrintsTheSecretsAndValuesOfItsServices(t *testing.T) {
	t.Chdir(t.TempDir())
	out, status := run(t, "", "init", "my-bot", "--bot", "gromit", "--services", "gitlab,jira")
	if status != 0 {
		t.Fatalf("init ended %d:\n%s", status, out)
	}
	for _, want := range []string{
		"  corbie controller secret set GITLAB_TOKEN --at /my-bot/gromit/gitlab\n",
		"  corbie controller value set GITLAB_SITE <host> --at /my-bot/gromit/gitlab\n",
		"  corbie controller secret set JIRA_TOKEN --at /my-bot/gromit/jira\n",
		"  corbie controller value set JIRA_SITE <host> --at /my-bot/gromit/jira\n",
		"  corbie controller value set JIRA_EMAIL <email> --at /my-bot/gromit/jira\n",
		"  set the listener's projects: or group: in gromit/gitlab/gromit.yaml\n",
		"  set the listener's projects: in gromit/jira/gromit.yaml\n",
		"  cd my-bot && corbie run submit .\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("init does not print %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "--services <names>") {
		t.Errorf("init with services prints the hint to choose them:\n%s", out)
	}
}

// init with no services sets no secret, and says how to name a bot and its
// services; a service with no bot is refused.
func TestInitWithNoServicesSaysHowToChooseThem(t *testing.T) {
	t.Chdir(t.TempDir())
	out, status := run(t, "", "init", "my-bot")
	if status != 0 {
		t.Fatalf("init ended %d:\n%s", status, out)
	}
	if !strings.Contains(out, "  corbie rook init my-bot --bot <name> --services <names>\n") {
		t.Errorf("init does not say how to choose services:\n%s", out)
	}
	if strings.Contains(out, "secret set") || strings.Contains(out, "value set") {
		t.Errorf("init with no services prints a secret or value:\n%s", out)
	}
	if out, status := run(t, "", "init", "other", "--services", "slack"); status == 0 || !strings.Contains(out, "--bot") {
		t.Errorf("init with a service and no bot ended %d:\n%s", status, out)
	}
}

// Services named as people write a list, with a space after a comma or a comma
// at the end, or one named twice, are each taken once.
func TestInitTakesAListAsPeopleWriteIt(t *testing.T) {
	for _, list := range []string{"slack, gitlab", "slack,gitlab,", "slack,gitlab,slack"} {
		t.Chdir(t.TempDir())
		out, status := run(t, "", "init", "my-bot", "--bot", "gromit", "--services", list)
		if status != 0 {
			t.Errorf("--services %q ended %d:\n%s", list, status, out)
			continue
		}
		for _, want := range []string{"secret set SLACK_TOKEN", "secret set GITLAB_TOKEN"} {
			if n := strings.Count(out, want); n != 1 {
				t.Errorf("--services %q prints %s %d times:\n%s", list, want, n, out)
			}
		}
	}
}

// init that adds a bot to a project holding another says, after the new bot's
// secrets, that it needs a conversation of its own; init that adds a service
// to the same bot does not.
func TestInitOfASecondBotSaysItNeedsItsOwnConversation(t *testing.T) {
	t.Chdir(t.TempDir())
	if out, status := run(t, "", "init", "my-bot", "--bot", "gromit", "--services", "slack"); status != 0 {
		t.Fatalf("init ended %d:\n%s", status, out)
	}
	out, status := run(t, "", "init", "my-bot", "--bot", "hal", "--services", "telegram")
	if status != 0 {
		t.Fatalf("init ended %d:\n%s", status, out)
	}
	step := "my-bot holds another bot, so hal needs a conversation of its own, as corbie rook manual rook-project says under Common changes: " +
		"copy conversation.yaml to hal/conversation.yaml with the other bot's name in it changed to hal, change conversation to hal/conversation " +
		"in the send: and allow: of hal's routers, and add hal/conversation to the receive: of each agent it calls. Then:\n\n"
	secret := "  corbie controller secret set TELEGRAM_TOKEN --at /my-bot/hal/telegram\n"
	if !strings.Contains(out, secret+"\n"+step+"  cd my-bot && corbie test .\n") {
		t.Errorf("init does not print the step after the secrets:\n%s", out)
	}
	if out, _ := run(t, "", "init", "my-bot", "--bot", "gromit", "--services", "jira"); strings.Contains(out, "another bot") {
		t.Errorf("init of a service of the same bot prints the step:\n%s", out)
	}
}
