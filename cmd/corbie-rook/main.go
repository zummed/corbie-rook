// Command corbie-rook is Rook, which corbie rook runs.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	rook "github.com/zummed/corbie-rook/rook"
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
	Usage:   "<directory> [--bot <name> --services <names>]",
	Summary: "make a directory a project, or bring its ready-made machines up to date",
	Run:     cmdInit,
	Help: `
It rewrites .rook, Rook's ready-made machines. The first time, it writes the
starting files. It changes no file of yours, but adds .rook/ and
people/scopes.yaml to .corbieignore when it lacks them, so run it again after
updating Rook.

  --bot <name>        the bot's name, a directory of the project
  --services <names>  the services the bot is on, separated by commas:
                      telegram, slack, gitlab, jira. Each gets a router and
                      its placed calls in <bot>/<service>/

Then set the secrets and values it names, and run corbie run submit . in the
directory. A router runs from then while its file is there.`,
}

func cmdInit(e *cli.Env, args []string) error {
	var bot, list string
	e.Flags.StringVar(&bot, "bot", "", "")
	e.Flags.StringVar(&list, "services", "", "")
	rest, err := e.Parse(args)
	switch {
	case err != nil:
		return err
	case len(rest) != 1:
		return cli.Mistake("name one directory")
	}
	// Blanks round a name and an empty name are left out, and a name given twice
	// is taken once.
	var services []string
	for _, name := range strings.Split(list, ",") {
		if name = strings.TrimSpace(name); name != "" && !slices.Contains(services, name) {
			services = append(services, name)
		}
	}
	dir := rest[0]
	updating := rook.IsProject(dir)
	others := rook.Bots(dir)
	if err := rook.Init(dir, bot, services); err != nil {
		return err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	// The project is named by its directory, as corbie run submit names its tree.
	in, name := "", filepath.Base(abs)
	if filepath.Clean(dir) != "." {
		in, name = "cd "+dir+" && ", dir
	}
	if slices.Contains(services, "slack") {
		fmt.Fprintln(e.Out, "Make the Slack app first, as corbie rook manual rook-project says under The Slack app.")
	}
	if updating {
		differ, err := rook.Differ(dir)
		if err != nil {
			return err
		}
		fmt.Fprintf(e.Out, "%s has this version's .rook.\n\n", name)
		if len(differ) > 0 {
			fmt.Fprintf(e.Out, "These of your files are not as this version starts a project:\n\n  %s\n\ncorbie rook manual rook-starter shows how it starts them. Take what you want of them, then:\n\n", strings.Join(differ, "\n  "))
		} else {
			fmt.Fprintln(e.Out, "Then:")
			fmt.Fprintln(e.Out)
		}
	} else {
		fmt.Fprintf(e.Out, "%s is ready, and answers nobody until agents/assistant.yaml is there. Add it as corbie rook manual rook-project says under Add an agent, or ask a model in %s to. Then:\n\n", name, name)
	}
	for _, service := range services {
		at := "/" + filepath.Base(abs) + "/" + bot + "/" + service
		for _, secret := range rook.Secrets[service] {
			fmt.Fprintf(e.Out, "  corbie controller secret set %s --at %s\n", secret, at)
		}
		for _, value := range rook.Values[service] {
			fmt.Fprintf(e.Out, "  corbie controller value set %s %s --at %s\n", value.Name, value.Shown, at)
		}
		if fields, ok := rook.Placeholders[service]; ok {
			fmt.Fprintf(e.Out, "  set the listener's %s in %s/%s/%s.yaml\n", fields, bot, service, bot)
		}
	}
	if bot != "" && len(others) > 0 && !slices.Contains(others, bot) {
		fmt.Fprintf(e.Out, "\n%s holds another bot, so %s needs a conversation of its own, as corbie rook manual rook-project says under Common changes: copy conversation.yaml to %s/conversation.yaml with the other bot's name in it changed to %s, change conversation to %s/conversation in the send: and allow: of %s's routers, and add %s/conversation to the receive: of each agent it calls. Then:\n\n", name, bot, bot, bot, bot, bot, bot)
	}
	if len(rook.Bots(dir)) == 0 {
		fmt.Fprintf(e.Out, "  corbie rook init %s --bot <name> --services <names>\n", dir)
	}
	if updating {
		fmt.Fprintf(e.Out, "  %scorbie test .\n", in)
	}
	fmt.Fprintf(e.Out, "  %scorbie run submit .\n", in)
	return nil
}
