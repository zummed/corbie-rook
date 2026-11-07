// Package rook is what Rook ships: ready-made machines, and what writes them into
// a project.
package rook

import (
	"embed"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/zummed/corbie/manual"
)

//go:embed all:shipped all:starter pages
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

// botWord is the word that a starting file holds, in its path and its text,
// where the bot's name goes.
const botWord = "BOT"

// Starting gives the text of each file this version starts a project with, by
// its path in the project, for a bot of that name: the files every project
// starts with, the conversation, and each service's router and placed calls
// under <bot>/<service>/.
func Starting(bot string) map[string]string {
	out := map[string]string{}
	fs.WalkDir(files, "starter", func(name string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			text, _ := files.ReadFile(name)
			rel := strings.ReplaceAll(strings.TrimPrefix(name, "starter/"), botWord, bot)
			out[rel] = strings.ReplaceAll(string(text), botWord, bot)
		}
		return err
	})
	return out
}

// botFile says whether a starting file, by its path with the bot unnamed, is
// written only for a bot, and for which service: the conversation is written
// for any, and a file under BOT/<service>/ for that service.
func botFile(rel string) (service string, ok bool) {
	if rel == "conversation.yaml" {
		return "", true
	}
	after, ok := strings.CutPrefix(rel, botWord+"/")
	service, _, _ = strings.Cut(after, "/")
	return service, ok
}

// starterPage gives every starting file, under its path in a project.
func starterPage() string {
	var b strings.Builder
	b.WriteString("Rook starting files: the files this version writes into a new project, and each service's router and placed calls.\n\nCompare a project's files with these after corbie rook init updates it. <bot> is the name given with --bot.\n")
	starting := Starting("<bot>")
	for _, rel := range slices.Sorted(maps.Keys(starting)) {
		fmt.Fprintf(&b, "\n%s\n%s\n\n", rel, strings.Repeat("-", len(rel)))
		for _, line := range strings.Split(strings.TrimRight(starting[rel], "\n"), "\n") {
			fmt.Fprintf(&b, "    %s\n", line)
		}
	}
	return b.String()
}

// Differ gives the project's files that are not as this version starts them, of
// those it has, sorted. A bot's files are compared as written for each bot
// the project has.
func Differ(dir string) ([]string, error) {
	starting := map[string][]string{}
	for rel, text := range Starting(botWord) {
		if _, ok := botFile(rel); !ok {
			starting[rel] = append(starting[rel], text)
			continue
		}
		for _, bot := range Bots(dir) {
			path := strings.ReplaceAll(rel, botWord, bot)
			starting[path] = append(starting[path], strings.ReplaceAll(text, botWord, bot))
		}
	}
	var out []string
	for _, rel := range slices.Sorted(maps.Keys(starting)) {
		mine, err := os.ReadFile(filepath.Join(dir, rel))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !slices.Contains(starting[rel], string(mine)) {
			out = append(out, rel)
		}
	}
	return out, nil
}

// Bots gives the name of each bot in a project, sorted: a directory <bot> that
// holds a router <bot>/<service>/<bot>.yaml.
func Bots(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		for _, service := range Services() {
			if _, err := os.Stat(filepath.Join(dir, e.Name(), service, e.Name()+".yaml")); err == nil && e.IsDir() {
				out = append(out, e.Name())
				break
			}
		}
	}
	return out
}

// Shipped is the directory in a project that holds Rook's machines. Rook rewrites
// it, and a person changes nothing in it.
const Shipped = ".rook"

// Secrets gives the secrets each service needs, set at /<project>/<bot>/<service>.
var Secrets = map[string][]string{
	"telegram": {"TELEGRAM_TOKEN"},
	"slack":    {"SLACK_TOKEN", "SLACK_APP_TOKEN"},
	"gitlab":   {"GITLAB_TOKEN"},
	"jira":     {"JIRA_TOKEN"},
}

// A Value is a value a service's machines read, set as corbie controller value
// set Name Shown, with what to write for Shown.
type Value struct{ Name, Shown string }

// Values gives the values each service needs.
var Values = map[string][]Value{
	"gitlab": {{"GITLAB_SITE", "<host>"}},
	"jira":   {{"JIRA_SITE", "<host>"}, {"JIRA_EMAIL", "<email>"}},
}

// Placeholders gives the fields of a listener's input that a service's
// starting router holds a placeholder for, which a person sets.
var Placeholders = map[string]string{
	"gitlab": "projects: or group:",
	"jira":   "projects:",
}

// Services gives the name of every service, sorted.
func Services() []string {
	return slices.Sorted(maps.Keys(Secrets))
}

// Named are the project's files that init writes whenever one is not there:
// AGENTS.md, which points at .rook/AGENTS.md, .corbieignore, and later.yaml,
// which places .rook/later.yaml for the agent's tool later.
var Named = []string{"AGENTS.md", ".corbieignore", "later.yaml"}

// Listed are the lines init adds to a project's .corbieignore when it does not
// hold them: Rook's directory, and the table Rook's lookup reads.
var Listed = []string{Shipped + "/", "people/scopes.yaml"}

// IsProject says whether a directory is a Rook project: its .corbieignore
// lists .rook/, or it has a .rook.
func IsProject(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, Shipped)); err == nil {
		return true
	}
	text, _ := os.ReadFile(filepath.Join(dir, ".corbieignore"))
	return slices.Contains(strings.Split(string(text), "\n"), Shipped+"/")
}

var botName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// Init makes dir a project, or brings one up to date. In a directory that is
// no project it writes the starting files; in a project, each of Named that is
// not there. With a bot named it writes the conversation, and for each service
// named the service's router and placed calls under <bot>/<service>/, each that
// is not there, so it changes no file of the person's. It adds the lines of
// Listed that .corbieignore lacks, and rewrites .rook, with the page rook as
// .rook/AGENTS.md.
func Init(dir, bot string, services []string) error {
	for _, name := range services {
		if _, ok := Secrets[name]; !ok {
			return fmt.Errorf("there is no service %q: there are %v", name, Services())
		}
	}
	switch {
	case bot == "" && len(services) > 0:
		return fmt.Errorf("a service needs a bot: name it with --bot")
	case bot != "" && (!botName.MatchString(bot) || bot == "ask"):
		return fmt.Errorf("%q is not a bot's name: use letters, digits, '_' and '-', starting with a letter", bot)
	}
	project := IsProject(dir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	templates := Starting(botWord)
	for _, rel := range slices.Sorted(maps.Keys(templates)) {
		service, forBot := botFile(rel)
		write := !project || slices.Contains(Named, rel)
		if forBot {
			write = bot != "" && (service == "" || slices.Contains(services, service))
		}
		if !write {
			continue
		}
		to := filepath.Join(dir, strings.ReplaceAll(rel, botWord, bot))
		if err := writeFile(to, strings.ReplaceAll(templates[rel], botWord, bot), false); err != nil {
			return err
		}
	}
	if err := list(filepath.Join(dir, ".corbieignore")); err != nil {
		return err
	}
	shipped := filepath.Join(dir, Shipped)
	if err := os.RemoveAll(shipped); err != nil {
		return err
	}
	if err := copyTree("shipped", shipped); err != nil {
		return err
	}
	text, err := files.ReadFile("pages/rook.txt")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(shipped, "AGENTS.md"), string(text), true)
}

// list adds to a .corbieignore each line of Listed it does not hold.
func list(name string) error {
	text, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	have := strings.Split(string(text), "\n")
	add := ""
	for _, line := range Listed {
		if !slices.Contains(have, line) {
			add += line + "\n"
		}
	}
	if add == "" {
		return nil
	}
	if len(text) > 0 && !strings.HasSuffix(string(text), "\n") {
		add = "\n" + add
	}
	return os.WriteFile(name, append(text, add...), 0o644)
}

// copyTree copies a tree of Rook's files to a directory, replacing any there.
func copyTree(from, to string) error {
	return fs.WalkDir(files, from, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(from, name)
		if err != nil {
			return err
		}
		text, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		return writeFile(filepath.Join(to, rel), string(text), true)
	})
}

// writeFile writes a file, and its directory, and with replace false leaves a
// file that is there as it is.
func writeFile(to, text string, replace bool) error {
	if _, err := os.Stat(to); err == nil && !replace {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	return os.WriteFile(to, []byte(text), 0o644)
}
