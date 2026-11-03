// Command corbie-rook is Rook, which corbie rook runs.
package main

import (
	"fmt"
	"os"
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

  --bridges <names>   the bridges to start, separated by commas: telegram,
                      slack, gitlab, jira. Each gets a file in bridges/, and a
                      bridge runs while its file is there

Then set the secrets it names, and run corbie run submit bridges in the directory.`,
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
	if err := rook.Init(rest[0], bridges); err != nil {
		return err
	}
	if slices.Contains(bridges, "slack") {
		fmt.Fprintln(e.Out, "Make the Slack app first, as corbie rook manual rook-project says under The Slack app.")
	}
	fmt.Fprintf(e.Out, "%s is ready. Change people.yaml and rules.yaml, then:\n\n", rest[0])
	fmt.Fprintln(e.Out, "  corbie controller secret set ANTHROPIC_KEY")
	for _, name := range bridges {
		for _, secret := range rook.Secrets[name] {
			fmt.Fprintf(e.Out, "  corbie controller secret set %s\n", secret)
		}
	}
	fmt.Fprintf(e.Out, "  cd %s && corbie run submit bridges\n", rest[0])
	return nil
}
