package rook_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zummed/corbie/client"
	"github.com/zummed/corbie/controller"
	"github.com/zummed/corbie/identity"
	"github.com/zummed/corbie/permission"
	"github.com/zummed/corbie/role"
	"github.com/zummed/corbie/runner"
	"github.com/zummed/corbie/secret"
	"github.com/zummed/corbie/store"

	_ "github.com/zummed/corbie/agent"
	_ "github.com/zummed/corbie/flow"
	_ "github.com/zummed/corbie/plugin/bundled"
	_ "github.com/zummed/corbie/statechart"
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
	ahead      atomic.Int64 // how far the clock is ahead of the computer's
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t}
	mem := store.NewMemory()
	ctl, err1 := identity.New("controller", identity.Controller)
	run, err2 := identity.New("runner", identity.Runner)
	cli, err3 := identity.New("client", identity.Client)
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
	for _, trust := range []identity.Trust{{Card: run.Card}, {Card: cli.Card, Ceiling: ceiling}} {
		if err := identity.WriteTrust(ctx, ctlStore, ctl, trust); err != nil {
			t.Fatal(err)
		}
	}
	c := controller.New(ctl, ctlStore)
	c.Now, c.AnyRunner = w.now, true
	w.controller = c
	r := runner.New(run, runner.Served{Controller: ctl.Card, Ceiling: ceiling, Store: as(store.Runner, run.Name)})
	r.Now, r.Command = w.now, []string{os.Args[0], runner.JobArg}
	w.Client = client.New(cli, ctl.Card, as(store.Client, cli.Name))
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

// secret gives the controller a secret.
func (w *world) secret(name, value string) {
	w.t.Helper()
	if err := secret.Set(context.Background(), w.controller.Store, w.controller.ID, name, value); err != nil {
		w.t.Fatal(err)
	}
}

// set moves the clock to a time.
func (w *world) set(at time.Time) { w.ahead.Store(int64(time.Until(at))) }

// sync submits a project's bridges and the machines the test put in on/.
func (w *world) sync(dir string) {
	w.t.Helper()
	var dirs []string
	for _, name := range []string{"bridges", "on"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			dirs = append(dirs, name)
		}
	}
	s := &client.Submit{Client: w.Client, Context: dir, Dirs: dirs, Interval: interval, Say: func(format string, args ...any) {
		w.t.Logf("submit: "+format, args...)
	}}
	if err := s.Run(context.Background()); err != nil {
		w.t.Fatal(err)
	}
}

// roots gives the root runs that carry every one of the labels.
func (w *world) roots(labels map[string]string) []*client.Run {
	w.t.Helper()
	runs, err := w.List(context.Background(), client.Filter{Labels: labels, Finished: true})
	if err != nil {
		w.t.Fatal(err)
	}
	var out []*client.Run
	for _, run := range runs {
		if run.Parent == "" {
			out = append(out, run)
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
						w.t.Logf("%s failed: %s", run.Machine, run.Error)
					}
				}
			}
			w.t.Fatalf("waited for %s", what)
		}
		time.Sleep(interval)
	}
}

// kept waits for the running run a sync keeps of a machine, and gives its ID.
func (w *world) kept(path string) string {
	w.t.Helper()
	var id string
	w.until("the run of "+path, func() bool {
		id = ""
		for _, run := range w.roots(nil) {
			if run.Key == client.KeyFile+path && run.Status == "running" {
				id = run.ID
			}
		}
		return id != ""
	})
	return id
}

// started waits for the run a sync started of a machine, running or ended, and
// gives its ID.
func (w *world) started(path string) string {
	w.t.Helper()
	var id string
	w.until("the run of "+path, func() bool {
		for _, run := range w.roots(nil) {
			if run.Key == client.KeyFile+path {
				id = run.ID
			}
		}
		return id != ""
	})
	return id
}

// send sends a run an event.
func (w *world) send(run, event string, data any) {
	w.t.Helper()
	if _, err := w.Send(context.Background(), run, event, data, 0); err != nil {
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
