package rook_test

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	llmstandin "github.com/zummed/corbie/plugin/llm/standin"
)

// An uploads stands in for the files uploaded to GitLab's project
// group/project, and passes every other call to the gitlab it wraps.
type uploads struct {
	*httptest.Server
	gl    *gitlab
	mu    sync.Mutex
	files map[string]string // by path under the project, as /uploads/<secret>/<name>
	sizes map[string]int    // the size HEAD gives, where it is not the file's own
	asks  []string          // each call for a file, as "GET <path>"
}

func newUploads(t *testing.T, gl *gitlab, files map[string]string) *uploads {
	u := &uploads{gl: gl, files: files, sizes: map[string]int{}}
	u.Server = httptest.NewServer(http.HandlerFunc(u.serve))
	t.Cleanup(u.Close)
	return u
}

func (u *uploads) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.Contains(r.URL.Path, "/uploads/") {
		u.gl.serve(w, r)
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.asks = append(u.asks, r.Method+" "+r.URL.EscapedPath())
	path, ours := strings.CutPrefix(r.URL.EscapedPath(), "/api/v4/projects/group%2Fproject")
	body, found := u.files[path]
	if r.Header.Get("PRIVATE-TOKEN") != "gl-token" || !ours || !found {
		w.WriteHeader(404)
		return
	}
	size, given := u.sizes[path]
	if !given || r.Method != "HEAD" {
		size = len(body)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(size))
	if r.Method == "GET" {
		io.WriteString(w, body)
	}
}

func (u *uploads) asked() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string{}, u.asks...)
}

// gitlabProject makes a GitLab project whose helper is a real agent on a
// stand-in model, which answers "seen".
func gitlabProject(t *testing.T, w *world, u *uploads) (string, *llmstandin.Server) {
	t.Helper()
	w.secret("GITLAB_TOKEN", "gl-token")
	w.secret("TEST_KEY", "sk-test")
	dir := pointed(t, "gitlab", u.Server)
	setSite(t, dir, "bridges/gitlab.yaml", "gitlab.example.com", strings.TrimPrefix(u.URL, "http://"))
	model := &llmstandin.Server{Answer: func(llmstandin.Call) llmstandin.Reply { return llmstandin.Reply{Text: "seen"} }}
	web := httptest.NewServer(model)
	t.Cleanup(web.Close)
	realAgent(t, dir, web.URL)
	u.gl.note(1, 2, "al", "before")
	w.sync(dir)
	w.until("the first ask", func() bool { return u.gl.asked() > 0 })
	return dir, model
}

// answered moves time on until the bot has commented on the issue.
func answered(w *world, gl *gitlab) {
	w.until("the answer", func() bool {
		w.set(w.now().Add(31 * time.Second))
		time.Sleep(5 * interval)
		return len(gl.said()) >= 1
	})
}

const secret1 = "029d6592aaaaaaaaaaaaaaaaaaaaaaaa"
const secret2 = "0c2c754ebbbbbbbbbbbbbbbbbbbbbbbb"

// A picture in a comment is downloaded through the API when the comment is
// answered, and the model is shown it as an image beside the comment.
func TestGitLabPictureIsShownToTheModel(t *testing.T) {
	gl := newGitLab(t)
	picture := "\x89PNG picture"
	u := newUploads(t, gl, map[string]string{"/uploads/" + secret1 + "/shot.png": picture})
	w := newWorld(t)
	_, model := gitlabProject(t, w, u)
	gl.note(2, 2, "al", "@bot what is this?\n\n![shot](/uploads/"+secret1+"/shot.png)")
	answered(w, gl)
	shown := fmt.Sprint(model.Calls[0].Messages)
	for _, want := range []string{"alice asks: what is this?", "shot.png", "image/png", base64.StdEncoding.EncodeToString([]byte(picture))} {
		if !strings.Contains(shown, want) {
			t.Errorf("%q is not in %s", want, shown)
		}
	}
}

// A comment that is only the bot's name and a file is answered.
func TestGitLabFileWithNoWordsIsAnswered(t *testing.T) {
	gl := newGitLab(t)
	u := newUploads(t, gl, map[string]string{"/uploads/" + secret1 + "/build.log": "exit 1"})
	w := newWorld(t)
	_, model := gitlabProject(t, w, u)
	gl.note(2, 2, "al", "@bot [build.log](/uploads/"+secret1+"/build.log)")
	answered(w, gl)
	if shown := fmt.Sprint(model.Calls[0].Messages); !strings.Contains(shown, "build.log") || !strings.Contains(shown, "text/plain") {
		t.Errorf("the model was shown %s", shown)
	}
	if got := gl.said(); !strings.Contains(got[0], "seen") {
		t.Errorf("the bot said %v", got)
	}
}

// An opened issue's files are those its description uploaded to its own
// project, each once: a link to another project or host is not downloaded, and
// a file over 32 MiB is named to the model and not downloaded.
func TestGitLabTakesOnlyTheIssuesOwnFiles(t *testing.T) {
	gl := newGitLab(t)
	picture := "\x89PNG picture"
	shot, big := "/uploads/"+secret1+"/shot.png", "/uploads/"+secret2+"/big.zip"
	u := newUploads(t, gl, map[string]string{shot: picture, big: "a zip"})
	u.sizes[big] = 64 << 20
	gl.text = "@bot see ![shot](" + shot + ") and again ![shot](" + shot + ") and [big.zip](" + big + ")\n" +
		"![x](/group/other/uploads/" + secret2 + "/x.png) ![y](https://elsewhere.example" + shot + ") ![z](/-/project/5" + shot + ")"
	w := newWorld(t)
	_, model := gitlabProject(t, w, u)
	gl.happens(2, map[string]any{"author_id": 2, "author_username": "al", "target_type": "Issue", "action_name": "opened", "target_iid": 7})
	answered(w, gl)
	want := []string{"HEAD /api/v4/projects/group%2Fproject" + shot, "GET /api/v4/projects/group%2Fproject" + shot, "HEAD /api/v4/projects/group%2Fproject" + big}
	if got := u.asked(); !slices.Equal(got, want) {
		t.Errorf("GitLab was asked for %q; want %q", got, want)
	}
	shown := fmt.Sprint(model.Calls[0].Messages)
	if !strings.Contains(shown, "big.zip") || !strings.Contains(shown, strconv.Itoa(64<<20)) || !strings.Contains(shown, base64.StdEncoding.EncodeToString([]byte(picture))) {
		t.Errorf("the model was shown %s", shown)
	}
}
