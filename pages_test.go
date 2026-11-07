package rook_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	rook "github.com/zummed/corbie-rook"
	"github.com/zummed/corbie/client"
	"github.com/zummed/corbie/manual"
)

// examples gives the files a page shows: each block indented four spaces that
// starts with a line "# <path>", as the path and the file's text. The label
// is the whole first line, after "# ". A path is a machine, a script, a text
// an agent includes, or .corbieignore.
type example struct{ label, path, text string }

var starts = regexp.MustCompile(`^    # ([\w./-]+\.(?:yaml|js|md)|\.corbieignore)(.*)$`)

func examples(t *testing.T, page string) []example {
	t.Helper()
	text, err := os.ReadFile(filepath.Join("pages", page+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	var out []example
	var at *example
	for _, line := range strings.Split(string(text), "\n") {
		if m := starts.FindStringSubmatch(line); m != nil {
			out = append(out, example{label: m[1] + m[2], path: m[1]})
			at = &out[len(out)-1]
			continue
		}
		if at == nil {
			continue
		}
		if line != "" && !strings.HasPrefix(line, "    ") {
			at = nil
			continue
		}
		at.text += strings.TrimPrefix(line, "    ") + "\n"
	}
	for i := range out {
		out[i].text = strings.TrimRight(out[i].text, "\n") + "\n"
	}
	return out
}

// Every file the pages show, written into a project with a bot on every
// service, reads as Corbie reads a project: each *.yaml is a machine with no
// problem, but one .corbieignore lists, which is no machine. A file labelled
// with the path of a starting file is that file, without its comments.
func TestEveryExampleFileReads(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	if err := rook.Init(dir, bot, rook.Services()); err != nil {
		t.Fatal(err)
	}
	starting := rook.Starting(bot)
	var shown []string
	for _, page := range []string{"rook-project", "rook-design"} {
		for _, ex := range examples(t, page) {
			if _, ok := starting[ex.path]; ok && ex.label == ex.path {
				kept, err := os.ReadFile(filepath.Join(dir, ex.path))
				if err != nil {
					t.Fatal(err)
				}
				var lines []string
				for _, line := range strings.Split(string(kept), "\n") {
					if !strings.HasPrefix(line, "#") {
						lines = append(lines, line)
					}
				}
				if want := strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"; ex.text != want {
					t.Errorf("%s shows %s as\n%s\nand the file is\n%s", page, ex.path, ex.text, want)
				}
			}
			if slices.Contains(shown, ex.path) {
				t.Errorf("the pages show %s twice, and only the last is read", ex.path)
			}
			shown = append(shown, ex.path)
			full := filepath.Join(dir, ex.path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(ex.text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(shown) < 10 {
		t.Fatalf("the pages show only %v", shown)
	}
	tree, err := client.ReadTree(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	machines := map[string]bool{}
	for _, m := range tree.Machines {
		machines[m.Path] = true
		if m.Problem != nil {
			t.Errorf("%s: %v", m.Path, m.Problem)
		}
	}
	for _, path := range shown {
		listed := slices.ContainsFunc(tree.Listed, func(l string) bool {
			return l == path || strings.HasSuffix(l, "/") && strings.HasPrefix(path, l)
		})
		if machine := strings.HasSuffix(path, ".yaml") && !listed; machines[path] != machine {
			t.Errorf("the pages show %s, which Corbie reads as a machine: %v, and is listed in .corbieignore: %v", path, machines[path], listed)
		}
	}
}

// Each page's first line is its title and the sentence that is its summary, and
// it ends with what to read next.
func TestEveryPageIsWrittenAsCorbiesAre(t *testing.T) {
	for _, page := range rook.Pages() {
		first, _, _ := strings.Cut(page.Text, "\n")
		if title, sentence, ok := strings.Cut(first, ": "); !ok || title == "" || !strings.HasSuffix(sentence, ".") || page.Summary != first {
			t.Errorf("%s begins %q", page.Name, first)
		}
		if page.Name != "rook-starter" && !strings.Contains(page.Text, "\nReference\n---------\n") {
			t.Errorf("%s has no Reference", page.Name)
		}
		if strings.Contains(page.Text, "\n## ") || strings.Contains(page.Text, "|---") || strings.Contains(page.Text, "\n```yaml") {
			t.Errorf("%s holds Markdown", page.Name)
		}
	}
	if !slices.ContainsFunc(rook.Pages(), func(p manual.Page) bool { return p.Name == "rook-starter" }) {
		t.Fatal("there is no page rook-starter")
	}
}
