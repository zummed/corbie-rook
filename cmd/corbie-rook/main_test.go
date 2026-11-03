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

func TestAModelIsToldThePageRook(t *testing.T) {
	out, status := run(t, `{"jsonrpc":"2.0","id":1,"method":"initialize"}`+"\n", "mcp")
	if status != 0 {
		t.Fatal(out)
	}
	var answer struct {
		Result struct{ Instructions string }
	}
	if err := json.Unmarshal([]byte(out), &answer); err != nil {
		t.Fatal(err)
	}
	page, status := run(t, "", "manual", "rook")
	if status != 0 {
		t.Fatal(page)
	}
	if strings.TrimSpace(answer.Result.Instructions) != strings.TrimSpace(page) || !strings.Contains(page, "rook-design") {
		t.Fatalf("told %q, and the page is %q", answer.Result.Instructions, page)
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
