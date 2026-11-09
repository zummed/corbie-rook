package test

// Rook's filters for a message that does not name the bot: one matches words,
// and one asks a small model. Each gives { reply: { answer } }.

import (
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// The words filter answers true when a text holds one of its words or phrases
// as a whole word, ignoring case, in any script.
func TestWordsFilter(t *testing.T) {
	w := newWorld(t)
	dir := filepath.Join(t.TempDir(), "project")
	if err := exampleProject(dir, nil); err != nil {
		t.Fatal(err)
	}
	write(t, dir, map[string]string{"filters/words.yaml": "include: .rook/filters/words.yaml\n"})
	w.sync(dir)
	words := []any{"deploy", "build failed", "Größe", ""}
	for text, want := range map[string]bool{
		"Can you DEPLOY orders?":    true,
		"deploy":                    true,
		"I deployed it yesterday":   false,
		"redeploy":                  false,
		"the Build \n failed":       true,
		"the build fails":           false,
		"why has the build failed?": true,
		"die größe stimmt nicht":    true,
		"die größere stimmt nicht":  false,
		"nothing for the bot here":  false,
		"(deploy)":                  true,
	} {
		run, err := w.Wait(context.Background(), w.start("filters/words.yaml", map[string]any{"text": text, "words": words}), interval)
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprint(w.show(run.ID).Output); run.Status != "done" || got != fmt.Sprintf("map[reply:map[answer:%v]]", want) {
			t.Errorf("for %q the filter ended %s with %s: %s", text, run.Status, got, w.show(run.ID).Error)
		}
	}
}

// An agent that includes the model filter asks its model, shown the thread and
// the message and what the bot is for, and gives its answer as reply.
func TestModelFilterAsksAModel(t *testing.T) {
	w := newWorld(t)
	w.secret("TEST_KEY", "sk-test")
	dir := filepath.Join(t.TempDir(), "project")
	if err := exampleProject(dir, nil); err != nil {
		t.Fatal(err)
	}
	model := &llmstandin.Server{Answer: func(llmstandin.Call) llmstandin.Reply { return llmstandin.Reply{Text: `{"answer": true}`} }}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	write(t, dir, map[string]string{"filters/interested.yaml": `
		agent: interested
		include: .rook/filters/model.yaml
		model: { name: test-model, service: anthropic, address: ` + web.URL + `, secret: TEST_KEY }
		system:
		  - { include: .rook/filters/model.yaml#system }
		  - The bot answers questions about the build.
	`})
	w.sync(dir)
	id := w.start("filters/interested.yaml", map[string]any{"text": "please look", "name": "alice", "thread": "you: earlier words"})
	w.until("the answer", func() bool { return w.show(id).HasEnded() })
	if run := w.show(id); run.Status != "done" || fmt.Sprint(run.Output.(map[string]any)["reply"]) != "map[answer:true]" {
		t.Fatalf("the filter ended %s with %v: %s", run.Status, run.Output, run.Error)
	}
	shown := fmt.Sprint(model.Calls[0].Messages)
	for _, want := range []string{"you: earlier words", "from alice", "please look"} {
		if !strings.Contains(shown, want) {
			t.Errorf("%q is not in %s", want, shown)
		}
	}
	if system := model.Calls[0].System; !strings.Contains(system, "meant for the") || !strings.Contains(system, "about the build") {
		t.Errorf("the system text is %q", system)
	}
}
