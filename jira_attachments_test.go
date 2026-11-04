package rook_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// A jiraFiles stands in for Jira with files on its one issue. It adds them, and
// any other fields, to what the search and the issue's read give, serves each
// file's contents, and passes everything else to the jira.
type jiraFiles struct {
	*httptest.Server
	j         *jira
	files     []map[string]any  // the issue's fields.attachment
	contents  map[string]string // each file's contents, by id
	mu        sync.Mutex
	fields    map[string]any // other fields, given instead of the jira's
	downloads []string       // each file's id, as it is downloaded
}

func newJiraFiles(t *testing.T, files []map[string]any, contents map[string]string, fields map[string]any) *jiraFiles {
	f := &jiraFiles{j: newJira(t), files: files, contents: contents, fields: fields}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *jiraFiles) serve(w http.ResponseWriter, r *http.Request) {
	if id, ok := strings.CutPrefix(r.URL.Path, "/rest/api/2/attachment/content/"); ok {
		if user, token, ok := r.BasicAuth(); !ok || user != "bot@example.com" || token != "jira-token" {
			w.WriteHeader(401)
			return
		}
		if r.URL.Query().Get("redirect") != "false" {
			http.Redirect(w, r, "http://media.example.com/file", http.StatusSeeOther)
			return
		}
		f.mu.Lock()
		f.downloads = append(f.downloads, id)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, f.contents[id])
		return
	}
	issue := r.Method == "GET" && r.URL.Path == "/rest/api/2/issue/OPS-12"
	if r.URL.Path != "/rest/api/2/search/jql" && !issue {
		f.j.serve(w, r)
		return
	}
	got := httptest.NewRecorder()
	f.j.serve(got, r)
	var out map[string]any
	if err := json.Unmarshal(got.Body.Bytes(), &out); err != nil {
		w.WriteHeader(got.Code)
		return
	}
	fields := out["fields"]
	if !issue {
		fields = out["issues"].([]any)[0].(map[string]any)["fields"]
	}
	f.mu.Lock()
	fields.(map[string]any)["attachment"] = f.files
	for name, value := range f.fields {
		fields.(map[string]any)[name] = value
	}
	f.mu.Unlock()
	json.NewEncoder(w).Encode(out)
}

func (f *jiraFiles) downloaded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.downloads)
}

// attachment is one of the issue's files, made by author at a time.
func attachment(id, name, kind string, size int, author string, at time.Time) map[string]any {
	return map[string]any{"id": id, "filename": name, "mimeType": kind, "size": size, "created": jiraTime(at), "author": map[string]any{"accountId": author}}
}

func jiraTime(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05.000+0000") }

// jiraAnswering starts the Jira bridge on f with a real agent and a stand-in
// model, and gives a function that waits for the next ask.
func jiraAnswering(t *testing.T, f *jiraFiles) (*world, *llmstandin.Server, func()) {
	w := newWorld(t)
	w.secret("JIRA_TOKEN", "jira-token")
	w.secret("TEST_KEY", "sk-test")
	dir := pointed(t, "jira", f.Server)
	setSite(t, dir, "bridges/jira.yaml", "example.atlassian.net", strings.TrimPrefix(f.URL, "http://"))
	model := &llmstandin.Server{Answer: func(llmstandin.Call) llmstandin.Reply { return llmstandin.Reply{Text: "seen"} }}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	w.sync(dir)
	ask := func() {
		before := f.j.asked()
		w.until("Jira asked", func() bool {
			w.set(w.now().Add(31 * time.Second))
			time.Sleep(5 * interval)
			return f.j.asked() > before
		})
	}
	ask()
	return w, model, ask
}

// A picture a comment shows is downloaded when the comment is answered, and the
// model is shown it as an image beside the comment.
func TestJiraPictureIsShownToTheModel(t *testing.T) {
	at := time.Now().Add(time.Second)
	picture := "\x89PNG picture"
	f := newJiraFiles(t, []map[string]any{attachment("10001", "cat.png", "image/png", len(picture), "acc-1", at)}, map[string]string{"10001": picture}, nil)
	w, model, ask := jiraAnswering(t, f)
	f.j.comment("1", "acc-1", "[~accountid:acc-bot] what is this? !cat.png|thumbnail!", at)
	ask()
	w.until("the answer", func() bool { return len(f.j.saying()) >= 1 })
	shown := fmt.Sprint(model.Calls[0].Messages)
	for _, want := range []string{"what is this?", "cat.png", "image/png", base64.StdEncoding.EncodeToString([]byte(picture))} {
		if !strings.Contains(shown, want) {
			t.Errorf("%q is not in %s", want, shown)
		}
	}
}

// A comment that only names the bot and links a file is answered with the file.
func TestJiraFileAloneIsAnswered(t *testing.T) {
	at := time.Now().Add(time.Second)
	f := newJiraFiles(t, []map[string]any{attachment("10001", "log.txt", "text/plain", 9, "acc-1", at)}, map[string]string{"10001": "it failed"}, nil)
	w, _, ask := jiraAnswering(t, f)
	f.j.comment("1", "acc-1", "[~accountid:acc-bot] [^log.txt]", at)
	ask()
	w.until("the answer", func() bool { return len(f.j.saying()) >= 1 })
	if got := f.downloaded(); !slices.Equal(got, []string{"10001"}) {
		t.Fatalf("Jira gave the files %v", got)
	}
}

// A comment has the files it names: of two with one name the newest made by the
// comment, and one made just after it. An external image names no file, and one
// over 32 MiB is named and not downloaded.
func TestJiraCommentHasTheFilesItNames(t *testing.T) {
	at := time.Now().Add(time.Second)
	f := newJiraFiles(t, []map[string]any{
		attachment("1", "cat.png", "image/png", 7, "acc-1", at.Add(-2*time.Second)),
		attachment("2", "cat.png", "image/png", 7, "acc-1", at.Add(-time.Second)),
		attachment("3", "cat.png", "image/png", 7, "acc-1", at.Add(time.Second)),
		attachment("4", "dog.png", "image/png", 7, "acc-1", at.Add(time.Second)),
		attachment("7", "bird.png", "image/png", 7, "acc-1", at),
		attachment("5", "x.png", "image/png", 7, "acc-1", at),
		attachment("6", "film.mp4", "video/mp4", 64<<20, "acc-1", at),
	}, map[string]string{"1": "old cat", "2": "new cat", "3": "late cat", "4": "dog", "5": "x", "7": "bird"}, nil)
	w, model, ask := jiraAnswering(t, f)
	f.j.comment("1", "acc-1", "[~accountid:acc-bot] wow! !cat.png! and !dog.png|thumbnail! and !https://example.com/x.png! [^film.mp4]", at)
	ask()
	w.until("the answer", func() bool { return len(f.j.saying()) >= 1 })
	if got := f.downloaded(); !slices.Equal(got, []string{"2", "4"}) {
		t.Fatalf("Jira gave the files %v", got)
	}
	if shown := fmt.Sprint(model.Calls[0].Messages); !strings.Contains(shown, "film.mp4") {
		t.Errorf("film.mp4 is not in %s", shown)
	}
}

// An opened issue has the files its reporter attached, and not another person's.
func TestJiraIssueHasItsReportersFiles(t *testing.T) {
	at := time.Now().Add(time.Second)
	f := newJiraFiles(t, []map[string]any{
		attachment("1", "mine.png", "image/png", 4, "acc-1", at),
		attachment("2", "theirs.png", "image/png", 6, "acc-2", at),
	}, map[string]string{"1": "mine", "2": "theirs"}, nil)
	w, _, ask := jiraAnswering(t, f)
	f.mu.Lock()
	f.fields = map[string]any{"created": jiraTime(at), "description": "[~accountid:acc-bot] it fails"}
	f.mu.Unlock()
	ask()
	w.until("the answer", func() bool { return len(f.j.saying()) >= 1 })
	if got := f.downloaded(); !slices.Equal(got, []string{"1"}) {
		t.Fatalf("Jira gave the files %v", got)
	}
}
