package cmd

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestDashCommandsListsEveryCommand(t *testing.T) {
	cmds := dashCommands(NewRootCommand())
	paths := map[string]dashCommand{}
	for _, c := range cmds {
		paths[c.path] = c
	}
	for _, want := range []string{"cluster describe", "cluster edit", "cluster scale", "cluster list", "logs", "backup list", "version"} {
		if _, ok := paths[want]; !ok {
			t.Errorf("missing %q in %v", want, cmds)
		}
	}
	for _, not := range []string{"cluster", "backup", "ui", "help"} {
		if _, ok := paths[not]; ok {
			t.Errorf("%q should not be listed: groups and the dashboard itself can't be run on a cluster", not)
		}
	}
	if !paths["cluster scale"].takesCluster || paths["version"].takesCluster || paths["cluster list"].takesCluster {
		t.Errorf("takesCluster should follow [CLUSTER_NAME] in Use: %+v", cmds)
	}
}

func TestDashCommandArgsFillsInTheSelectedCluster(t *testing.T) {
	cmds := dashCommands(NewRootCommand())
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"cluster scale --cpu 4", []string{"cluster", "scale", "big-db", "--cpu", "4"}},
		{"cluster scale other-db --cpu 4", []string{"cluster", "scale", "other-db", "--cpu", "4"}},
		{"backup list", []string{"backup", "list", "big-db"}},
		{"version", []string{"version"}},
		{"  logs   -f ", []string{"logs", "big-db", "-f"}},
	} {
		got, err := commandArgs(cmds, tc.line, "big-db")
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("commandArgs(%q) = %q, %v; want %q", tc.line, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "frobnicate", "cluster"} {
		if _, err := commandArgs(cmds, bad, "big-db"); err == nil {
			t.Errorf("commandArgs(%q) should fail", bad)
		}
	}
}

func TestDashboardOnlyBindsKeysForCommandsInThisBuild(t *testing.T) {
	d, err := newDashboard(NewRootCommand(), fakeClusterOpts(nil), &fakeDashRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.key('s'); !ok {
		t.Error("s should scale: cluster scale is in this build")
	}
	// status and connect come in other PRs; their keys appear once they land.
	for _, r := range []rune{'t', 'c'} {
		if k, ok := d.key(r); ok {
			t.Errorf("%q is bound to %q, which this build doesn't have", r, k.path)
		}
	}
}

func TestUINeedsATerminal(t *testing.T) {
	_, err := runCmd(t, NewRootCommand(), "ui")
	if err == nil || !strings.Contains(err.Error(), "needs a terminal") {
		t.Fatalf("err = %v, want a message that it needs a terminal", err)
	}
}

func TestRootWithoutArgsPrintsHelpWhenPiped(t *testing.T) {
	out, err := runCmd(t, NewRootCommand())
	help, _ := runCmd(t, NewRootCommand(), "--help")
	if err != nil || out == "" || out != help {
		t.Fatalf("bare adbo without a terminal should print the same as --help, got %v:\n%s", err, out)
	}
}

func TestDashboardListsClusters(t *testing.T) {
	d := newDashDriver(t, readyDBCluster())
	d.waitFor("Context:", "dev-gke", "omni-test", "big-db", "DBClusterReady", "8Gi", "DBClusters(omni-test)[1]")
}

func TestDashboardKeysRunCommandsOnTheSelectedCluster(t *testing.T) {
	d := newDashDriver(t, readyDBCluster())
	d.waitFor("big-db")

	d.press('e')
	d.wantCall("interactive", "cluster edit big-db -n omni-test --context dev-gke")
	d.press('i')
	d.wantCall("interactive", "cluster edit big-db -i -n omni-test --context dev-gke")

	d.press(tcell.KeyEnter)
	d.wantCall("output", "cluster describe big-db -n omni-test --context dev-gke")
	d.waitFor("Describe(omni-test/big-db)", "Name:  big-db")
	d.press(tcell.KeyEscape)
	d.waitFor("DBClusters(omni-test)")
}

func TestDashboardRunsAnyCommandFromTheCommandLine(t *testing.T) {
	d := newDashDriver(t, readyDBCluster())
	d.waitFor("big-db")
	d.press(':', "cluster scale --cpu 4", tcell.KeyEnter)
	d.wantCall("interactive", "cluster scale big-db --cpu 4 -n omni-test --context dev-gke")
}

func TestDashboardScaleSendsOnlyWhatChanged(t *testing.T) {
	d := newDashDriver(t, readyDBCluster())
	d.waitFor("big-db")
	d.press('s')
	d.waitFor("Scale(omni-test/big-db)", "Memory")
	// CPU is 2: make it 4, then tab past memory, disk and standbys to Apply.
	d.press(tcell.KeyBackspace2, "4", tcell.KeyTab, tcell.KeyTab, tcell.KeyTab, tcell.KeyTab, tcell.KeyEnter)
	d.wantCall("output", "cluster scale big-db --cpu 4 -y --no-wait -n omni-test --context dev-gke")
}

func TestDashboardFollowsLogs(t *testing.T) {
	d := newDashDriver(t, readyDBCluster())
	d.waitFor("big-db")
	d.press('l')
	d.wantCall("stream", "logs big-db -f --tail 200 --source postgres -n omni-test --context dev-gke")
	d.waitFor("Logs(omni-test/big-db)", "a log line")
	d.press('2')
	d.wantCall("stream", "logs big-db -f --tail 200 --source internal -n omni-test --context dev-gke")
}

func TestDashboardHelpListsEveryCommand(t *testing.T) {
	d := newDashDriver(t, readyDBCluster())
	d.waitFor("big-db")
	d.press('?')
	d.waitFor("Run any command with :", "cluster scale", "backup list", "version")
}

func TestDashboardFilters(t *testing.T) {
	other := readyDBCluster()
	other.SetName("orders-db")
	d := newDashDriver(t, readyDBCluster(), other)
	d.waitFor("big-db", "orders-db")
	d.press('/', "orders", tcell.KeyEnter)
	d.waitFor("DBClusters(omni-test)[1]", "</orders>")
	if strings.Contains(d.screen(), "big-db") {
		t.Errorf("the filter should hide big-db:\n%s", d.screen())
	}
}

// fakeDashRunner records the adbo commands the dashboard runs.
type fakeDashRunner struct {
	mu    sync.Mutex
	calls map[string][]string
}

func (f *fakeDashRunner) record(kind string, args []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string][]string{}
	}
	f.calls[kind] = append(f.calls[kind], strings.Join(args, " "))
}

func (f *fakeDashRunner) called(kind string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.calls[kind]...)
}

func (f *fakeDashRunner) Output(_ context.Context, args []string) ([]byte, error) {
	f.record("output", args)
	return []byte("Name:  big-db\nPhase: DBClusterReady\n"), nil
}

func (f *fakeDashRunner) Stream(ctx context.Context, args []string, w io.Writer) error {
	f.record("stream", args)
	fmt.Fprintln(w, "a log line")
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeDashRunner) Interactive(args []string) error {
	f.record("interactive", args)
	return nil
}

// dashDriver runs the dashboard on a simulated screen.
type dashDriver struct {
	t      *testing.T
	sim    tcell.SimulationScreen
	runner *fakeDashRunner
	text   atomic.Value // the last drawn screen, as text
}

func newDashDriver(t *testing.T, objs ...runtime.Object) *dashDriver {
	t.Helper()
	dd := &dashDriver{t: t, sim: tcell.NewSimulationScreen("UTF-8"), runner: &fakeDashRunner{}}
	dd.text.Store("")
	d, err := newDashboard(NewRootCommand(), fakeClusterOpts(objs), dd.runner)
	if err != nil {
		t.Fatal(err)
	}
	d.app.SetScreen(dd.sim)
	dd.sim.SetSize(170, 40)
	// Read the screen right after each draw, on the UI goroutine.
	d.app.SetAfterDrawFunc(func(tcell.Screen) {
		dd.sim.Show() // tview shows the frame after this hook; read this one, not the last
		cells, w, _ := dd.sim.GetContents()
		var b strings.Builder
		for i, c := range cells {
			if len(c.Runes) > 0 {
				b.WriteRune(c.Runes[0])
			} else {
				b.WriteByte(' ')
			}
			if (i+1)%w == 0 {
				b.WriteByte('\n')
			}
		}
		dd.text.Store(b.String())
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.run(ctx) }()
	t.Cleanup(func() {
		d.app.Stop()
		cancel()
		<-done
	})
	return dd
}

func (dd *dashDriver) screen() string { return dd.text.Load().(string) }

func (dd *dashDriver) waitFor(want ...string) {
	dd.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		missing := ""
		for _, w := range want {
			if !strings.Contains(dd.screen(), w) {
				missing = w
				break
			}
		}
		if missing == "" {
			return
		}
		if time.Now().After(deadline) {
			dd.t.Fatalf("screen never showed %q:\n%s", missing, dd.screen())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// press sends runes, strings (typed one rune at a time) and special keys.
func (dd *dashDriver) press(keys ...any) {
	dd.t.Helper()
	for _, k := range keys {
		switch k := k.(type) {
		case rune:
			dd.sim.InjectKey(tcell.KeyRune, k, tcell.ModNone)
		case string:
			for _, r := range k {
				dd.sim.InjectKey(tcell.KeyRune, r, tcell.ModNone)
			}
		case tcell.Key:
			dd.sim.InjectKey(k, 0, tcell.ModNone)
		default:
			dd.t.Fatalf("can't press %T", k)
		}
	}
}

// wantCall waits until the runner has run args the given way.
func (dd *dashDriver) wantCall(kind, args string) {
	dd.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(dd.runner.called(kind), args) {
		if time.Now().After(deadline) {
			dd.t.Fatalf("no %s call %q; got %q\n%s", kind, args, dd.runner.called(kind), dd.screen())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
