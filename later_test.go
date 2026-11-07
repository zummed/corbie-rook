package rook_test

import (
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// The agent's tool progress posts in the person's chat at once, before the
// reply, through the bot's chat/post for the service.
func TestProgressPostsAtOnce(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := telegramProject(t, w, tg)
	_, address := toolModel(t, "It is done.", llmstandin.ToolCall{Name: "progress", Args: `{"text": "Looking now."}`})
	realAgent(t, dir, address)
	tg.says(map[string]any{"text": "find the file"})
	w.sync(dir)
	w.until("the reply", func() bool { return len(tg.texts("1")) == 2 })
	if got := tg.texts("1"); !slices.Equal(got, []string{"Looking now.", "It is done."}) {
		t.Errorf("the chat was sent %q", got)
	}
}

// laterModel sets a reminder for `minutes` when a person asks, and answers a
// reminder that says it waited `waited` with what it read in it.
func laterModel(minutes, waited string) func(llmstandin.Call) llmstandin.Reply {
	return func(c llmstandin.Call) llmstandin.Reply {
		said, results := turnOf(c)
		switch {
		case strings.Contains(said, "This is a reminder you set "+waited+" ago, and Alice did not write it.") && strings.Contains(said, "check the download"):
			return llmstandin.Reply{Text: "The download is done."}
		case strings.Contains(said, "check the download"):
			return llmstandin.Reply{Text: "The reminder was given as the person's words."}
		case len(results) == 0:
			return llmstandin.Reply{Calls: []llmstandin.ToolCall{{ID: "l1", Name: "later", Args: `{"minutes": ` + minutes + `, "note": "check the download"}`}}}
		default:
			return llmstandin.Reply{Text: "I will look again in ten minutes."}
		}
	}
}

// The agent's tool later starts a run at `later`, which waits the minutes the
// model chose and then sends the conversation a message from the same person,
// marked later. The agent is told it is its own reminder, and its reply goes
// to the person's chat.
func TestLaterBringsTheConversationBack(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := telegramProject(t, w, tg)
	model := &llmstandin.Server{Answer: laterModel("10", "10 minutes")}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	tg.says(map[string]any{"text": "fetch the film"})
	w.sync(dir)
	w.until("the first reply", func() bool { return len(tg.texts("1")) == 1 })
	var waiting bool
	w.until("the reminder waiting", func() bool {
		for _, runs := range keyed(w, "later.yaml") {
			for _, run := range runs {
				waiting = waiting || w.show(run.ID).Timers == 1
			}
		}
		return waiting
	})
	if got := tg.texts("1"); got[0] != "I will look again in ten minutes." {
		t.Fatalf("the chat was sent %q", got)
	}
	// Nine minutes on, it still waits.
	w.set(w.now().Add(9 * time.Minute))
	quiet()
	if got := tg.texts("1"); len(got) != 1 {
		t.Fatalf("the reminder came early: %q", got)
	}
	w.set(w.now().Add(2 * time.Minute))
	w.until("the reminder answered", func() bool { return len(tg.texts("1")) == 2 })
	if got := tg.texts("1")[1]; got != "The download is done." {
		t.Errorf("the reminder was answered %q", got)
	}
	runs := keyed(w, "conversation.yaml")
	if len(runs) != 1 {
		t.Errorf("the reminder went to %d conversations: %v", len(runs), runs)
	}
}

// A reminder the model asks for in 0 minutes waits one, and is still given
// as the agent's own reminder.
func TestLaterOfNoMinutesIsStillAReminder(t *testing.T) {
	tg := newTelegram(t)
	w := newWorld(t)
	dir, _ := telegramProject(t, w, tg)
	web := httptest.NewServer(&llmstandin.Server{Answer: laterModel("0", "a minute")})
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	tg.says(map[string]any{"text": "fetch the film"})
	w.sync(dir)
	w.until("the first reply", func() bool { return len(tg.texts("1")) == 1 })
	w.until("the reminder waiting", func() bool { return len(keyed(w, "later.yaml")) == 1 })
	w.set(w.now().Add(2 * time.Minute))
	w.until("the reminder answered", func() bool { return len(tg.texts("1")) == 2 })
	if got := tg.texts("1")[1]; got != "The download is done." {
		t.Errorf("the reminder was answered %q", got)
	}
}
