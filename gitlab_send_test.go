package rook_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// An agent sends a text as a file, typed by its name: it is uploaded to the
// issue's project, and the bot's first comment on the issue links to it.
func TestGitLabAgentSendsATextAsAFile(t *testing.T) {
	gl := newGitLab(t)
	w := newWorld(t)
	_, model := gitlabProject(t, w, gl)
	model.Answer = sending(`{"name": "report.csv", "text": "a,b\n1,2\n"}`)
	gl.note(2, 2, "al", "@bot the report, please")
	answered(w, gl)
	want := upload{Method: "uploads", Field: "file", To: "group/project", Name: "report.csv", Type: "text/csv", Body: "a,b\n1,2\n"}
	if got := gl.uploads; len(got) != 1 || got[0] != want {
		t.Fatalf("the bot uploaded %+v; want %+v", got, want)
	}
	if got := gl.said(); got[0] != "[report.csv](/uploads/"+secret2+"/report.csv)" {
		t.Errorf("the bot said %q", got)
	}
}

// An agent sends back a picture it was shown, and the comment shows it.
func TestGitLabAgentSendsAPicture(t *testing.T) {
	gl := newGitLab(t)
	picture := "\x89PNG picture"
	gl.files = map[string]string{"/uploads/" + secret1 + "/shot.png": picture}
	w := newWorld(t)
	_, model := gitlabProject(t, w, gl)
	model.Answer = sending(`{"name": "shot.png", "blob": "blob1"}`)
	gl.note(2, 2, "al", "@bot send it back\n\n![shot](/uploads/"+secret1+"/shot.png)")
	answered(w, gl)
	want := upload{Method: "uploads", Field: "file", To: "group/project", Name: "shot.png", Type: "image/png", Body: picture}
	if got := gl.uploads; len(got) != 1 || got[0] != want {
		t.Fatalf("the bot uploaded %+v; want %+v", got, want)
	}
	if got := gl.said(); got[0] != "![shot.png](/uploads/"+secret2+"/shot.png)" {
		t.Errorf("the bot said %q", got)
	}
}

// When GitLab refuses the upload, no comment links to it, and the model is
// told GitLab's status.
func TestGitLabRefusedUploadIsNotCommented(t *testing.T) {
	gl := newGitLab(t)
	gl.full = true
	w := newWorld(t)
	_, model := gitlabProject(t, w, gl)
	model.Answer = sending(`{"name": "report.csv", "text": "a,b\n"}`)
	gl.note(2, 2, "al", "@bot the report, please")
	answered(w, gl)
	if got := gl.said(); !slices.Equal(got, []string{"sent"}) {
		t.Errorf("the bot said %q", got)
	}
	if told := fmt.Sprint(model.Calls[1].Messages); !strings.Contains(told, "413") {
		t.Errorf("the model was told %s", told)
	}
}
