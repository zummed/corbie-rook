// Package rook is what Rook ships: ready-made machines, and what writes them into
// a project.
package rook

import (
	"embed"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/zummed/corbie/manual"
)

//go:embed all:shipped all:starter all:bridges pages
var files embed.FS

// Pages are Rook's pages of the manual: each file in pages/, whose first line
// is its summary, and the starting files. corbie mcp, allowed rook init, tells
// a model the page rook.
func Pages() []manual.Page {
	var out []manual.Page
	for _, name := range []string{"rook", "rook-project", "rook-design"} {
		text, _ := files.ReadFile("pages/" + name + ".txt")
		summary, _, _ := strings.Cut(string(text), "\n")
		out = append(out, manual.Page{Name: name, Group: "Rook", Summary: summary, Text: string(text)})
	}
	text := starterPage()
	summary, _, _ := strings.Cut(text, "\n")
	return append(out, manual.Page{Name: "rook-starter", Group: "Rook", Summary: summary, Text: text})
}

// Starting gives the path in a project of each file this version starts a project
// with, and of each bridge's file, sorted, with where it is kept here.
func Starting() map[string]string {
	out := map[string]string{}
	for _, top := range []string{"starter", "bridges"} {
		fs.WalkDir(files, top, func(name string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel := strings.TrimPrefix(name, "starter/")
				out[rel] = name
			}
			return err
		})
	}
	return out
}

// starterPage gives every starting file, under its path in a project.
func starterPage() string {
	var b strings.Builder
	b.WriteString("Rook starting files: the files this version writes into a new project, and each bridge's file.\n\nCompare a project's files with these after corbie rook init updates it.\n")
	starting := Starting()
	for _, rel := range slices.Sorted(maps.Keys(starting)) {
		text, _ := files.ReadFile(starting[rel])
		fmt.Fprintf(&b, "\n%s\n%s\n\n", rel, strings.Repeat("-", len(rel)))
		for _, line := range strings.Split(strings.TrimRight(string(text), "\n"), "\n") {
			fmt.Fprintf(&b, "    %s\n", line)
		}
	}
	return b.String()
}

// Differ gives the project's files that are not as this version starts them, of
// those it has, sorted.
func Differ(dir string) ([]string, error) {
	var out []string
	starting := Starting()
	for _, rel := range slices.Sorted(maps.Keys(starting)) {
		mine, err := os.ReadFile(filepath.Join(dir, rel))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		text, _ := files.ReadFile(starting[rel])
		if string(mine) != string(text) {
			out = append(out, rel)
		}
	}
	return out, nil
}

// Shipped is the directory in a project that holds Rook's machines. Rook rewrites
// it, and a person changes nothing in it.
const Shipped = ".rook"

// Secrets gives the secrets each bridge needs.
var Secrets = map[string][]string{
	"telegram": {"TELEGRAM_TOKEN"},
	"slack":    {"SLACK_TOKEN", "SLACK_APP_TOKEN"},
	"gitlab":   {"GITLAB_TOKEN"},
	"jira":     {"JIRA_TOKEN"},
}

// A Value is a value a bridge reads, set as corbie controller value set Name
// Shown, with what to write for Shown.
type Value struct{ Name, Shown string }

// Values gives the values each bridge needs.
var Values = map[string][]Value{
	"gitlab": {{"GITLAB_SITE", "<host>"}},
	"jira":   {{"JIRA_SITE", "<host>"}, {"JIRA_EMAIL", "<email>"}},
}

// Bridges gives the name of every bridge, sorted.
func Bridges() []string {
	names := make([]string, 0, len(Secrets))
	for name := range Secrets {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Named are the project's files that init writes whenever one is not there:
// those a machine in .rook reads by name, and those that point a model at
// .rook/AGENTS.md.
var Named = []string{"rules.yaml", "lookup.yaml", "AGENTS.md", "CLAUDE.md", ".mcp.json"}

// Running gives the bridges whose file is in the project's bridges/, sorted.
func Running(dir string) []string {
	var out []string
	for _, name := range Bridges() {
		if _, err := os.Stat(filepath.Join(dir, "bridges", name+".yaml")); err == nil {
			out = append(out, name)
		}
	}
	return out
}

// Init makes dir a project, or brings one up to date. It writes the starting
// files if dir has no rules.yaml, each of Named that is not there, and the file
// of each bridge named that is not there, so it changes no file of the
// person's. It rewrites .rook, with the page rook as .rook/AGENTS.md, and the
// machines of each bridge whose file is in bridges/, which it lists in
// .rook/people/running.yaml.
func Init(dir string, bridges []string) error {
	for _, name := range bridges {
		if _, ok := Secrets[name]; !ok {
			return fmt.Errorf("there is no bridge %q: there are %v", name, Bridges())
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "rules.yaml")); os.IsNotExist(err) {
		if err := copyTree("starter", dir, false); err != nil {
			return err
		}
	}
	for _, name := range Named {
		if err := copyFile("starter/"+name, filepath.Join(dir, name), false); err != nil {
			return err
		}
	}
	for _, name := range bridges {
		if err := copyFile("bridges/"+name+".yaml", filepath.Join(dir, "bridges", name+".yaml"), false); err != nil {
			return err
		}
	}
	shipped := filepath.Join(dir, Shipped)
	if err := os.RemoveAll(shipped); err != nil {
		return err
	}
	tops, err := fs.ReadDir(files, "shipped")
	if err != nil {
		return err
	}
	for _, top := range tops {
		if top.Name() == "bridges" {
			continue
		}
		if err := copyTree("shipped/"+top.Name(), filepath.Join(shipped, top.Name()), true); err != nil {
			return err
		}
	}
	running := Running(dir)
	for _, name := range running {
		if err := copyTree("shipped/bridges/"+name, filepath.Join(shipped, "bridges", name), true); err != nil {
			return err
		}
	}
	list := "# The bridges this project runs, whose machines corbie rook init wrote into\n# .rook/bridges/. file.js reaches a person only on these.\nrunning: [" + strings.Join(running, ", ") + "]\n"
	if err := os.WriteFile(filepath.Join(shipped, "people", "running.yaml"), []byte(list), 0o644); err != nil {
		return err
	}
	return copyFile("pages/rook.txt", filepath.Join(shipped, "AGENTS.md"), true)
}

// copyTree copies a tree of files to a directory.
func copyTree(from, to string, replace bool) error {
	return fs.WalkDir(files, from, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(from, name)
		if err != nil {
			return err
		}
		return copyFile(name, filepath.Join(to, rel), replace)
	})
}

func copyFile(name, to string, replace bool) error {
	if _, err := os.Stat(to); err == nil && !replace {
		return nil
	}
	text, err := files.ReadFile(path.Clean(name))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.WriteFile(to, text, 0o644)
}
