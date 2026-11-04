package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

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
	named := regexp.MustCompile(`"([a-z-]+)"`).FindAllStringSubmatch(text, -1)
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
