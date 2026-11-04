package rook_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// An agent sends a text as a file, typed by its name: it is attached to the
// issue it was asked on, and a comment links it, before the reply.
func TestJiraAgentSendsATextAsAFile(t *testing.T) {
	at := time.Now().Add(time.Second)
	j := newJira(t)
	w, model, ask := jiraAnswering(t, j)
	model.Answer = sending(`{"name": "report.csv", "text": "a,b\n1,2\n"}`)
	j.comment("1", "acc-1", "[~accountid:acc-bot] the report, please", at)
	ask()
	w.until("the reply", func() bool { return len(j.saying()) >= 2 })
	want := upload{Method: "attachments", Field: "file", To: "OPS-12", Name: "report.csv", Type: "text/csv", Body: "a,b\n1,2\n"}
	if got := j.attached(); len(got) != 1 || got[0] != want {
		t.Fatalf("the bot attached %+v; want %+v", got, want)
	}
	if got := j.saying(); !slices.Equal(got, []string{"[^report.csv]", "sent"}) {
		t.Errorf("the bot said %q", got)
	}
}

// An agent sends back a picture it was shown, and a comment shows it.
func TestJiraAgentSendsAPicture(t *testing.T) {
	at := time.Now().Add(time.Second)
	picture := "\x89PNG picture"
	j := newJira(t)
	j.files = []map[string]any{attachment("10001", "cat.png", "image/png", len(picture), "acc-1", at)}
	j.contents = map[string]string{"10001": picture}
	w, model, ask := jiraAnswering(t, j)
	model.Answer = sending(`{"name": "cat.png", "blob": "blob1"}`)
	j.comment("1", "acc-1", "[~accountid:acc-bot] send it back !cat.png!", at)
	ask()
	w.until("the reply", func() bool { return len(j.saying()) >= 2 })
	want := upload{Method: "attachments", Field: "file", To: "OPS-12", Name: "cat.png", Type: "image/png", Body: picture}
	if got := j.attached(); len(got) != 1 || got[0] != want {
		t.Fatalf("the bot attached %+v; want %+v", got, want)
	}
	if got := j.saying(); got[0] != "!cat.png!" {
		t.Errorf("the bot said %q", got)
	}
}

// A file Jira refuses is named in no comment, and the model is told Jira's
// status.
func TestJiraRefusedFileIsNotNamed(t *testing.T) {
	at := time.Now().Add(time.Second)
	j := newJira(t)
	j.largest = 4
	w, model, ask := jiraAnswering(t, j)
	model.Answer = sending(`{"name": "report.csv", "text": "a,b\n1,2\n"}`)
	j.comment("1", "acc-1", "[~accountid:acc-bot] the report, please", at)
	ask()
	w.until("the reply", func() bool { return len(j.saying()) >= 1 })
	if got := j.saying(); !slices.Equal(got, []string{"sent"}) {
		t.Errorf("the bot said %q", got)
	}
	if told := fmt.Sprint(model.Calls[1].Messages); !strings.Contains(told, "Jira answered POST /issue/OPS-12/attachments with 413") {
		t.Errorf("the model was told %s", told)
	}
}

// A name is sent with each [, ], |, ! and ^ as _, and the comment names the
// file as Jira keeps it, which may differ.
func TestJiraCommentNamesTheFileAsJiraKeepsIt(t *testing.T) {
	at := time.Now().Add(time.Second)
	j := newJira(t)
	j.stored = "report (1).csv"
	w, model, ask := jiraAnswering(t, j)
	model.Answer = sending(`{"name": "report [1].csv", "text": "a,b\n1,2\n"}`)
	j.comment("1", "acc-1", "[~accountid:acc-bot] the report, please", at)
	ask()
	w.until("the reply", func() bool { return len(j.saying()) >= 2 })
	if got := j.attached(); len(got) != 1 || got[0].Name != "report _1_.csv" {
		t.Fatalf("the bot attached %+v", got)
	}
	if got := j.saying(); got[0] != "[^report (1).csv]" {
		t.Errorf("the bot said %q", got)
	}
}

// An image Jira does not show in a comment, such as an SVG, is linked.
func TestJiraOtherImageIsLinked(t *testing.T) {
	at := time.Now().Add(time.Second)
	j := newJira(t)
	w, model, ask := jiraAnswering(t, j)
	model.Answer = sending(`{"name": "logo.svg", "text": "<svg/>"}`)
	j.comment("1", "acc-1", "[~accountid:acc-bot] the logo, please", at)
	ask()
	w.until("the reply", func() bool { return len(j.saying()) >= 2 })
	if got := j.attached(); len(got) != 1 || got[0].Type != "image/svg+xml" {
		t.Fatalf("the bot attached %+v", got)
	}
	if got := j.saying(); got[0] != "[^logo.svg]" {
		t.Errorf("the bot said %q", got)
	}
}
