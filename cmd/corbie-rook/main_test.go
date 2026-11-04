package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	rook "github.com/zummed/corbie-rook"
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

// The page rook-project shows what corbie rook init printed.
func TestThePageShowsWhatInitPrints(t *testing.T) {
	const command = "    corbie rook init my-bot --bridges slack"
	var page string
	for _, p := range rook.Pages() {
		if p.Name == "rook-project" {
			page = p.Text
		}
	}
	_, after, ok := strings.Cut(page, command+"\n")
	if !ok {
		t.Fatalf("rook-project does not show %q", strings.TrimSpace(command))
	}
	var shown []string
	for _, line := range strings.Split(after, "\n") {
		if line != "" && !strings.HasPrefix(line, "      ") {
			break
		}
		shown = append(shown, strings.TrimPrefix(line, "      "))
	}
	t.Chdir(t.TempDir())
	out, status := run(t, "", "init", "my-bot", "--bridges", "slack")
	if want := strings.TrimRight(strings.Join(shown, "\n"), "\n") + "\n"; status != 0 || out != want {
		t.Fatalf("init ended %d and printed\n%s\nand the page shows\n%s", status, out, want)
	}
}
