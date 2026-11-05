// Command corbie-rook is Rook, which corbie rook runs.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	rook "github.com/zummed/corbie-rook"
	"github.com/zummed/corbie/client/cli"
	"github.com/zummed/corbie/manual"

	_ "github.com/zummed/corbie/agent"
	_ "github.com/zummed/corbie/flow"
	_ "github.com/zummed/corbie/plugin/bundled"
	_ "github.com/zummed/corbie/statechart"
)

func init() { manual.Add(rook.Pages) }

func main() {
	os.Exit(cli.Program("rook", []cli.Command{initCommand}, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

var initCommand = cli.Command{
	Name:    "init",
	Usage:   "<directory> [--bridges <names>]",
	Summary: "make a directory a project, or bring its ready-made machines up to date",
	Run:     cmdInit,
	Help: `
It rewrites .rook, Rook's ready-made machines. The first time, it writes the
starting files. It changes no file of yours, so run it again after updating
Rook.

  --bridges <names>   the bridges to add, separated by commas: telegram,
                      slack, gitlab, jira. Each gets a file in bridges/

Then set the secrets it names, and run corbie run submit bridges in the
directory. A bridge runs from then while its file is there.`,
}

func cmdInit(e *cli.Env, args []string) error {
	var list string
	e.Flags.StringVar(&list, "bridges", "", "")
	rest, err := e.Parse(args)
	switch {
	case err != nil:
		return err
	case len(rest) != 1:
		return cli.Mistake("name one directory")
	}
	var bridges []string
	if list != "" {
		bridges = strings.Split(list, ",")
	}
	dir := rest[0]
	_, err = os.Stat(filepath.Join(dir, "rules.yaml"))
	updating := err == nil
	if err := rook.Init(dir, bridges); err != nil {
		return err
	}
	in := ""
	if filepath.Clean(dir) != "." {
		in = "cd " + dir + " && "
	}
	if slices.Contains(bridges, "slack") {
		fmt.Fprintln(e.Out, "Make the Slack app first, as corbie rook manual rook-project says under The Slack app.")
	}
	if updating {
		differ, err := rook.Differ(dir)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.Out, "%s has this version's .rook.\n\n", dir)
		if len(differ) > 0 {
			fmt.Fprintf(e.Out, "These of your files are not as this version starts a project:\n\n  %s\n\ncorbie rook manual rook-starter shows how it starts them. Take what you want of them, then:\n\n", strings.Join(differ, "\n  "))
		} else {
			fmt.Fprintln(e.Out, "Then:")
			fmt.Fprintln(e.Out)
		}
	} else {
		fmt.Fprintf(e.Out, "%s is ready, and answers nobody until it has an agent. Add one as corbie rook manual rook-project says under Add an agent, or ask a model in %s to. Then:\n\n", dir, dir)
		fmt.Fprintln(e.Out, "  corbie controller secret set CLAUDE_TOKEN")
	}
	for _, name := range bridges {
		for _, secret := range rook.Secrets[name] {
			fmt.Fprintf(e.Out, "  corbie controller secret set %s\n", secret)
		}
	}
	// corbie run submit needs each directory it is given, so a project with
	// no bridge is told to add one, and schedules is named once it is there.
	if _, err := os.Stat(filepath.Join(dir, "bridges")); err != nil {
		fmt.Fprintf(e.Out, "  corbie rook init %s --bridges <names>\n", dir)
	}
	if updating {
		fmt.Fprintf(e.Out, "  %scorbie test .\n", in)
	}
	submit := "bridges"
	if _, err := os.Stat(filepath.Join(dir, "schedules")); err == nil {
		submit += " schedules"
	}
	fmt.Fprintf(e.Out, "  %scorbie run submit %s\n", in, submit)
	return nil
}
