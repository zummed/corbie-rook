package test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zummed/corbie/client"
	"github.com/zummed/corbie/client/cli"
	"github.com/zummed/corbie/controller"
	"github.com/zummed/corbie/identity"
	"github.com/zummed/corbie/instance"
	"github.com/zummed/corbie/machine"
	"github.com/zummed/corbie/permission"
	"github.com/zummed/corbie/role"
	"github.com/zummed/corbie/runner"
	"github.com/zummed/corbie/secret"
	"github.com/zummed/corbie/store"

	_ "github.com/zummed/corbie/machine/agent"
	_ "github.com/zummed/corbie/machine/flow"
	_ "github.com/zummed/corbie/machine/statechart"
	_ "github.com/zummed/corbie/plugin/bundled"
)

// The test binary stands in for the process a runner starts for a job.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == runner.JobArg {
		runner.JobMain()
		return
	}
	os.Exit(m.Run())
}

const interval = 20 * time.Millisecond

// A world is a controller, a runner and a client in the test's process,
// with the store in memory and one clock the test moves. client.NewLocal
// takes no clock, so the world is put together here as it puts it
// together.
type world struct {
	*client.Client
	t          *testing.T
	controller *controller.Controller
	mem        *store.Memory
	me         *identity.Identity // the client's identity
	ahead      atomic.Int64       // how far the clock is ahead of the computer's
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t}
	mem := store.NewMemory()
	w.mem = mem
	ctl, err1 := identity.New("controller", identity.Controller)
	run, err2 := identity.New("runner", identity.Runner)
	me, err3 := identity.New("client", identity.Client)
	for _, err := range []error{err1, err2, err3} {
		if err != nil {
			t.Fatal(err)
		}
	}
	as := func(kind store.Kind, name string) store.Store {
		return mem.As(store.Party{Kind: kind, Name: name, Controller: ctl.Name})
	}
	ctlStore := as(store.Controller, ctl.Name)
	ctx, stop := context.WithCancel(context.Background())
	if err := store.SetUp(ctx, ctlStore); err != nil {
		t.Fatal(err)
	}
	ceiling := permission.Layer{Allow: []string{"all"}}
	for _, trust := range []identity.Trust{{Card: run.Card}, {Card: me.Card, Ceiling: ceiling}} {
		if err := identity.WriteTrust(ctx, ctlStore, ctl, trust); err != nil {
			t.Fatal(err)
		}
	}
	c := controller.New(ctl, ctlStore)
	c.Now, c.AnyRunner = w.now, true
	w.controller = c
	r := runner.New(run, runner.Served{Controller: ctl.Card, Ceiling: ceiling, Store: as(store.Runner, run.Name)})
	r.Now, r.Command = w.now, []string{os.Args[0], runner.JobArg}
	w.me = me
	w.Client = client.New(me, ctl.Card, as(store.Client, me.Name))
	w.Client.Now = w.now
	inbox, stopInbox := ctlStore.Notice(store.Inbox(ctl.Name))
	jobs, stopJobs := ctlStore.Notice(store.Inbox(run.Name))
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer stopInbox()
		defer stopJobs()
		role.Run(ctx, w.now, func(role string, err error) { t.Logf("%s: %v", role, err) },
			role.Role{Name: "controller", Pass: c.Pass, Interval: interval, Notice: inbox},
			role.Role{Name: "runner", Pass: r.Pass, Interval: interval, Notice: jobs})
	}()
	t.Cleanup(func() { stop(); <-done })
	return w
}

func (w *world) now() time.Time { return time.Now().Add(time.Duration(w.ahead.Load())) }

// newClient enrols another client with a ceiling that allows everything, as
// a gateway's client would be: it owns no tree of the project's.
func (w *world) newClient(name string) *client.Client {
	w.t.Helper()
	id, err := identity.New(name, identity.Client)
	if err != nil {
		w.t.Fatal(err)
	}
	ctl := w.controller.ID
	trust := identity.Trust{Card: id.Card, Ceiling: permission.Layer{Allow: []string{"all"}}}
	if err := identity.WriteTrust(context.Background(), w.controller.Store, ctl, trust); err != nil {
		w.t.Fatal(err)
	}
	c := client.New(id, ctl.Card, w.mem.As(store.Party{Kind: store.Client, Name: id.Name, Controller: ctl.Name}))
	c.Now = w.now
	return c
}

// project is the prefix a test's project is submitted at: its directory is
// named project, and a submit's prefix is by default / and that name.
const project = "/project"

// secret sets a secret at the project, as rook-project says to.
func (w *world) secret(name, value string) { w.secretAt(project, name, value) }

// secretAt sets a secret at a location.
func (w *world) secretAt(at, name, value string) {
	w.t.Helper()
	if err := secret.Set(context.Background(), w.controller.Store, w.controller.ID, at, name, value); err != nil {
		w.t.Fatal(err)
	}
}

// value sets a value at the project.
func (w *world) value(name, value string) { w.valueAt(project, name, value) }

// valueAt sets a value at a location.
func (w *world) valueAt(at, name, value string) {
	w.t.Helper()
	if err := secret.SetValue(context.Background(), w.controller.Store, w.controller.ID, at, name, value); err != nil {
		w.t.Fatal(err)
	}
}

// corbie runs a command of the program corbie as the world's client, and
// gives what it printed. The CLI opens a store by its address, so the store
// in memory is copied to a directory first, as it stands.
func (w *world) corbie(args ...string) string {
	w.t.Helper()
	ctx := context.Background()
	dir := w.t.TempDir()
	to, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		w.t.Fatal(err)
	}
	from := w.mem.As(store.Party{Kind: store.Owner})
	listed, err := from.List(ctx, "")
	if err != nil {
		w.t.Fatal(err)
	}
	for _, l := range listed {
		value, _, err := from.Read(ctx, l.Name)
		if err == nil {
			_, err = to.Write(ctx, l.Name, value, store.Overwrite)
		}
		if err != nil {
			w.t.Fatal(err)
		}
	}
	cfg := identity.Config{Dir: filepath.Join(dir, "config")}
	err = w.me.Save(filepath.Join(cfg.Dir, "client", "identity"))
	if err == nil {
		err = cfg.SetTrust(identity.Client, identity.Trust{Card: w.controller.ID.Card, Store: filepath.Join(dir, "store")})
	}
	if err != nil {
		w.t.Fatal(err)
	}
	var out, said bytes.Buffer
	if status := cli.Run(ctx, append(args, "--config", cfg.Dir), strings.NewReader(""), &out, &said); status != 0 {
		w.t.Fatalf("corbie %s ended %d:\n%s%s", strings.Join(args, " "), status, out.String(), said.String())
	}
	return out.String()
}

// set moves the clock to a time.
func (w *world) set(at time.Time) { w.ahead.Store(int64(time.Until(at))) }

// sync submits a project's directory as the tree at /project, as corbie run
// submit . does in it. It fails the test on a file with a problem.
func (w *world) sync(dir string) {
	w.t.Helper()
	if r := w.syncAt(w.Client, dir, project); !r.Taken {
		w.t.Fatalf("the submit was refused: %s", r.Refused)
	}
}

// syncAt submits a directory as a tree at a prefix, by a client, and stays
// until each run it cancelled has ended and each run it started is taken.
func (w *world) syncAt(c *client.Client, dir, at string) *instance.Receipt {
	w.t.Helper()
	tree, err := client.ReadTree(dir, at)
	if err != nil {
		w.t.Fatal(err)
	}
	for _, m := range tree.Machines {
		if m.Problem != nil {
			w.t.Fatalf("%s: %v", m.Path, m.Problem)
		}
	}
	ctx := context.Background()
	message, err := c.Submit(ctx, tree, 0)
	if err != nil {
		w.t.Fatal(err)
	}
	receipt, err := c.WaitReceipt(ctx, message, 0, interval)
	if err != nil {
		w.t.Fatal(err)
	}
	if !receipt.Taken {
		return receipt
	}
	for _, id := range receipt.Tree.Cancelled {
		if _, err := c.Wait(ctx, id, interval); err != nil {
			w.t.Fatal(err)
		}
	}
	for _, row := range receipt.Tree.Locations {
		if row.Run != "" {
			if err := c.Started(ctx, row.Run, interval); err != nil {
				w.t.Fatal(err)
			}
		}
	}
	return receipt
}

// location gives the location of a file of the project.
func location(path string) string { return project + "/" + strings.TrimSuffix(path, ".yaml") }

// at gives every run at the location of a file of the project, running or
// ended, roots and children alike.
func (w *world) at(path string) []*client.Run {
	w.t.Helper()
	runs, err := w.List(context.Background(), client.Filter{Location: location(path), Finished: true})
	if err != nil {
		w.t.Fatal(err)
	}
	return runs
}

// start starts a run of a transient location of the project, and gives its
// ID.
func (w *world) start(path string, input any) string {
	w.t.Helper()
	id, err := w.Start(context.Background(), client.Start{At: location(path), Input: input})
	if err != nil {
		w.t.Fatal(err)
	}
	return id
}

// sendTo sends an event to the run of a key at a keyed location of the
// project, which starts one when none is running.
func (w *world) sendTo(path, key, event string, data any) {
	w.t.Helper()
	if _, err := w.Send(context.Background(), machine.Address{At: location(path), Key: &key}, event, data, 0); err != nil {
		w.t.Fatal(err)
	}
}

// record gives every entry of a run's record so far, its children's included.
func (w *world) record(id string) []client.Entry {
	w.t.Helper()
	var out []client.Entry
	if _, err := w.Record(context.Background(), id, func(e client.Entry) { out = append(out, e) }); err != nil {
		w.t.Fatal(err)
	}
	return out
}

// asked gives what the steps of a run and its children asked for, of one
// kind.
func (w *world) asked(id, kind string) []instance.Ask {
	w.t.Helper()
	var out []instance.Ask
	for _, e := range w.record(id) {
		for _, a := range e.Asks {
			if a.Kind == kind {
				out = append(out, a)
			}
		}
	}
	return out
}

// until waits for a condition, and fails the test after a few seconds, naming
// the runs that failed.
func (w *world) until(what string, cond func() bool) {
	w.t.Helper()
	for end := time.Now().Add(10 * time.Second); !cond(); {
		if time.Now().After(end) {
			if runs, err := w.List(context.Background(), client.Filter{Finished: true}); err == nil {
				for _, run := range runs {
					if os.Getenv("ROOK_RUNS") != "" {
						full := w.show(run.ID)
						w.t.Logf("%s %s %s in=%v out=%v", run.Machine, run.Status, run.Error, full.Input, full.Output)
					}
					if run.Status == "failed" {
						w.t.Logf("%s failed: %s", run.Machine, w.show(run.ID).Error)
					}
				}
			}
			w.t.Fatalf("waited for %s", what)
		}
		time.Sleep(interval)
	}
}

// kept waits for the running root at the location of a file of the project,
// and gives its ID.
func (w *world) kept(path string) string {
	w.t.Helper()
	var id string
	w.until("the run of "+path, func() bool {
		id = ""
		for _, run := range w.at(path) {
			if run.Parent == "" && run.Status == "running" {
				id = run.ID
			}
		}
		return id != ""
	})
	return id
}

// send sends an event to a run by its ID.
func (w *world) send(run, event string, data any) {
	w.t.Helper()
	to, err := w.Address(context.Background(), run)
	if err != nil {
		w.t.Fatal(err)
	}
	if _, err := w.Send(context.Background(), to, event, data, 0); err != nil {
		w.t.Fatal(err)
	}
}

// show gives a run as it stands.
func (w *world) show(id string) *client.Run {
	w.t.Helper()
	run, err := w.Show(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	return run
}

// titles gives the title of every run of a machine, in order.
func (w *world) titles(machine string) []string {
	w.t.Helper()
	runs, err := w.List(context.Background(), client.Filter{Finished: true})
	if err != nil {
		w.t.Fatal(err)
	}
	var out []string
	for _, run := range runs {
		if run.Machine == machine {
			out = append(out, w.show(run.ID).Title)
		}
	}
	slices.Sort(out)
	return out
}

// titled fails the test unless the runs of each machine have these titles.
func (w *world) titled(want map[string][]string) {
	w.t.Helper()
	for machine, titles := range want {
		if got := w.titles(machine); !slices.Equal(got, titles) {
			w.t.Errorf("the runs of %s have the titles %q", machine, got)
		}
	}
}

func dedent(text string) string {
	lines := strings.Split(strings.Trim(text, "\n"), "\n")
	indent := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		n := len(line) - len(strings.TrimLeft(line, "\t"))
		if indent < 0 || n < indent {
			indent = n
		}
	}
	for i, line := range lines {
		if indent > 0 && len(line) >= indent {
			lines[i] = line[indent:]
		}
	}
	return strings.ReplaceAll(strings.Join(lines, "\n"), "\t", "  ") + "\n"
}
