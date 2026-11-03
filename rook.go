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

//go:embed all:shipped all:starter all:bridges
var files embed.FS

//go:embed rook.txt
var briefing string

//go:embed README.md
var readme string

//go:embed DESIGN.md
var design string

// Pages are Rook's pages of the manual. A model is told the page rook when it
// connects to corbie rook mcp.
func Pages() []manual.Page {
	return []manual.Page{
		{Name: "rook", Group: "Rook", Summary: "Rook: what to do in a project, for a model.", Text: briefing},
		{Name: "rook-project", Group: "Rook", Summary: "Project: installing Rook, a project's files and the common changes.", Text: readme},
		{Name: "rook-design", Group: "Rook", Summary: "Design: the blocks, what each takes and gives, and who may do what.", Text: design},
		{Name: "rook-starter", Group: "Rook", Summary: "Starting files: the files this version writes into a new project, and each bridge's file.", Text: starterPage()},
	}
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
	b.WriteString("The files this version of Rook writes into a new project, and each bridge's file. Compare a project's files with these after corbie rook init updates it.\n")
	starting := Starting()
	for _, rel := range slices.Sorted(maps.Keys(starting)) {
		text, _ := files.ReadFile(starting[rel])
		fmt.Fprintf(&b, "\n%s\n\n%s", rel, text)
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

// Bridges gives the name of every bridge, sorted.
func Bridges() []string {
	names := make([]string, 0, len(Secrets))
	for name := range Secrets {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// Named are the project's files that a machine in .rook reads by name.
var Named = []string{"rules.yaml", "lookup.yaml", "attend.yaml", "answer.yaml", "agents/reviewer.yaml"}

// Init makes dir a project, or brings one up to date. It rewrites .rook. It writes
// the starting files if dir has no rules.yaml, each of Named that is not there,
// and the file of each bridge named that is not there, so it changes no file of
// the person's.
func Init(dir string, bridges []string) error {
	for _, name := range bridges {
		if _, ok := Secrets[name]; !ok {
			return fmt.Errorf("there is no bridge %q: there are %v", name, Bridges())
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(dir, Shipped)); err != nil {
		return err
	}
	if err := copyTree("shipped", filepath.Join(dir, Shipped), true); err != nil {
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
	return nil
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
