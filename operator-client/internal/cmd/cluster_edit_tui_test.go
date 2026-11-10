package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// stubBackend records what the TUI sends instead of talking to a cluster.
type stubBackend struct {
	dryRuns, applies []*unstructured.Unstructured
	dryRunErr        error
	applyErr         error
	fresh            *unstructured.Unstructured
}

func (b *stubBackend) dryRun(_ context.Context, obj *unstructured.Unstructured) error {
	b.dryRuns = append(b.dryRuns, obj)
	return b.dryRunErr
}

func (b *stubBackend) apply(_ context.Context, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	b.applies = append(b.applies, obj)
	if b.applyErr != nil {
		return nil, b.applyErr
	}
	out := obj.DeepCopy()
	out.SetGeneration(obj.GetGeneration() + 1)
	return out, nil
}

func (b *stubBackend) get(context.Context) (*unstructured.Unstructured, error) {
	return b.fresh, nil
}

// tui drives an editModel the way Bubble Tea would: each message goes
// through Update, and the commands it returns run and feed their messages
// back in.
type tui struct {
	t    *testing.T
	m    *editModel
	quit bool
}

func newTUI(t *testing.T, live *unstructured.Unstructured, b editBackend) *tui {
	t.Helper()
	d := &tui{t: t, m: newEditModel(context.Background(), newEditSession(live), b, `namespace "omni-test", context "dev-gke"`)}
	d.send(tea.WindowSizeMsg{Width: 120, Height: 40})
	return d
}

func (d *tui) send(msg tea.Msg) {
	d.t.Helper()
	m, cmd := d.m.Update(msg)
	d.m = m.(*editModel)
	d.run(cmd, 0)
}

func (d *tui) run(cmd tea.Cmd, depth int) {
	if cmd == nil || depth > 20 {
		return
	}
	switch msg := cmd().(type) {
	case nil:
	case tea.QuitMsg:
		d.quit = true
	case tea.BatchMsg:
		for _, c := range msg {
			d.run(c, depth+1)
		}
	default:
		m, next := d.m.Update(msg)
		d.m = m.(*editModel)
		d.run(next, depth+1)
	}
}

func (d *tui) typeText(s string) {
	d.t.Helper()
	for _, r := range s {
		d.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func (d *tui) press(keys ...tea.KeyType) {
	d.t.Helper()
	for _, k := range keys {
		d.send(tea.KeyMsg{Type: k})
	}
}

// find filters the list down to rows matching text and selects the first.
func (d *tui) find(text string) {
	d.t.Helper()
	d.typeText("/")
	d.typeText(text)
	d.press(tea.KeyEnter)
}

// change selects the field and replaces its value.
func (d *tui) change(label, value string) {
	d.t.Helper()
	d.find(label)
	d.press(tea.KeyEnter, tea.KeyCtrlU)
	d.typeText(value)
	d.press(tea.KeyEnter)
}

// wantView checks the screen for each text, ignoring case and how the
// text is wrapped.
func (d *tui) wantView(wants ...string) {
	d.t.Helper()
	v := d.m.View()
	flat := strings.ToLower(strings.Join(strings.Fields(v), " "))
	for _, w := range wants {
		if !strings.Contains(flat, strings.ToLower(w)) {
			d.t.Errorf("view is missing %q:\n%s", w, v)
		}
	}
}

func lastSpec(t *testing.T, objs []*unstructured.Unstructured, path ...string) any {
	t.Helper()
	if len(objs) == 0 {
		t.Fatal("nothing was sent")
	}
	v, _, _ := unstructured.NestedFieldNoCopy(objs[len(objs)-1].Object, append([]string{"spec"}, path...)...)
	return v
}

func TestEditTUIChecksEachChangeWithTheServer(t *testing.T) {
	b := &stubBackend{}
	d := newTUI(t, readyDBCluster(), b)
	d.change("primarySpec.resources.cpu", "8")

	if v := lastSpec(t, b.dryRuns, "primarySpec", "resources", "cpu"); v != int64(8) {
		t.Errorf("dry run sent cpu %#v, want int64 8", v)
	}
	if len(b.applies) != 0 {
		t.Fatal("a change must not be applied before the user confirms")
	}
	d.wantView("primarySpec.resources.cpu", "8", "was 2", "1 change", "accepted by the API server")
}

func TestEditTUIShowsWhyTheServerRejectsAChange(t *testing.T) {
	b := &stubBackend{dryRunErr: errors.New("spec.availability.numberOfStandbys: Invalid value: 9: must be less than or equal to 5")}
	d := newTUI(t, readyDBCluster(), b)
	d.change("availability.numberOfStandbys", "9")
	d.wantView("rejected", "must be less than or equal to 5")
}

func TestEditTUIIgnoresResultsOfOlderChecks(t *testing.T) {
	d := newTUI(t, readyDBCluster(), &stubBackend{})
	d.change("primarySpec.resources.cpu", "8")
	// A slow dry run of an earlier change answers after the newer one.
	d.send(checkedMsg{seq: d.m.checkSeq - 1, err: errors.New("result for an older change")})
	if strings.Contains(d.m.View(), "older change") {
		t.Errorf("an older check's result must not replace the newer one:\n%s", d.m.View())
	}
	d.wantView("accepted by the API server")
}

func TestEditTUIWontApplyWhatTheServerRejected(t *testing.T) {
	b := &stubBackend{dryRunErr: errors.New("spec.primarySpec.resources.memory: Invalid value")}
	d := newTUI(t, readyDBCluster(), b)
	d.change("primarySpec.resources.memory", "lots")
	d.typeText("a")
	d.wantView("fix the error")
	d.typeText("y")
	if len(b.applies) != 0 {
		t.Fatal("a change the API server rejected must not be applied")
	}
}

func TestEditTUIAppliesOnlyAfterConfirming(t *testing.T) {
	b := &stubBackend{}
	d := newTUI(t, readyDBCluster(), b)
	d.change("primarySpec.resources.memory", "16Gi")

	d.typeText("a")
	d.wantView("Apply 1 change", "restarts the database", "no standbys")
	d.typeText("n")
	if len(b.applies) != 0 || d.quit {
		t.Fatal("answering n must not apply or quit")
	}

	d.typeText("a")
	d.typeText("y")
	if v := lastSpec(t, b.applies, "primarySpec", "resources", "memory"); v != "16Gi" {
		t.Errorf("applied memory %#v, want 16Gi", v)
	}
	if !d.quit || d.m.applied == nil {
		t.Errorf("want the TUI to exit with the applied cluster (quit=%v)", d.quit)
	}
}

func TestEditTUIAsksBeforeDiscardingChanges(t *testing.T) {
	d := newTUI(t, readyDBCluster(), &stubBackend{})
	d.typeText("q")
	if !d.quit {
		t.Fatal("q without changes should quit right away")
	}

	d = newTUI(t, readyDBCluster(), &stubBackend{})
	d.change("primarySpec.resources.cpu", "8")
	d.typeText("q")
	d.wantView("Discard 1 change")
	d.typeText("n")
	if d.quit {
		t.Fatal("answering n must keep the TUI open")
	}
	d.press(tea.KeyCtrlC)
	d.typeText("y")
	if !d.quit || d.m.applied != nil {
		t.Errorf("want quit without applying (quit=%v)", d.quit)
	}
}

func TestEditTUIConflictReloadsAndKeepsChanges(t *testing.T) {
	b := &stubBackend{applyErr: apierrors.NewConflict(schema.GroupResource{Group: "alloydbomni.dbadmin.goog", Resource: "dbclusters"}, "big-db", errors.New("the object has been modified"))}
	d := newTUI(t, readyDBCluster(), b)
	d.change("primarySpec.resources.cpu", "8")
	d.typeText("a")
	d.typeText("y")
	if d.quit {
		t.Fatal("a conflict must keep the TUI open")
	}
	d.wantView("changed since", "press r")

	fresh := readyDBCluster()
	fresh.SetResourceVersion("5000")
	_ = unstructured.SetNestedField(fresh.Object, "128Gi", "spec", "primarySpec", "resources", "memory")
	b.fresh, b.applyErr = fresh, nil
	d.typeText("r")
	d.wantView("re-applied")

	d.typeText("a")
	d.typeText("y")
	got := b.applies[len(b.applies)-1]
	if got.GetResourceVersion() != "5000" {
		t.Errorf("applied resourceVersion %q, want the reloaded 5000", got.GetResourceVersion())
	}
	if v := lastSpec(t, b.applies, "primarySpec", "resources", "cpu"); v != int64(8) {
		t.Errorf("cpu = %#v, want my 8", v)
	}
	if v := lastSpec(t, b.applies, "primarySpec", "resources", "memory"); v != "128Gi" {
		t.Errorf("memory = %#v, want the reloaded 128Gi", v)
	}
}

func TestEditTUIStatusTabIsReadOnly(t *testing.T) {
	d := newTUI(t, readyDBCluster(), &stubBackend{})
	d.press(tea.KeyTab)
	d.wantView("phase", "DBClusterReady")
	d.press(tea.KeyEnter)
	d.typeText("x")
	d.wantView("read-only")
	if d.m.session.dirty() {
		t.Fatal("the status tab must not change anything")
	}
}

func TestEditTUITogglesBooleans(t *testing.T) {
	b := &stubBackend{}
	d := newTUI(t, readyDBCluster(), b)
	d.find("availability.enableAutoHeal")
	d.typeText(" ")
	if v := lastSpec(t, b.dryRuns, "availability", "enableAutoHeal"); v != false {
		t.Errorf("enableAutoHeal = %#v, want false", v)
	}
}

func TestEditTUIAddsRemovesAndRestoresFields(t *testing.T) {
	b := &stubBackend{}
	d := newTUI(t, readyDBCluster(), b)

	d.typeText("+")
	d.press(tea.KeyCtrlU)
	d.typeText("primarySpec.parameters.work_mem = 64MB")
	d.press(tea.KeyEnter)
	if v := lastSpec(t, b.dryRuns, "primarySpec", "parameters", "work_mem"); v != "64MB" {
		t.Errorf("work_mem = %#v, want 64MB", v)
	}

	d.find("max_connections")
	d.typeText("x")
	if _, found, _ := unstructured.NestedFieldNoCopy(b.dryRuns[len(b.dryRuns)-1].Object, "spec", "primarySpec", "parameters", "max_connections"); found {
		t.Error("x should remove max_connections")
	}
	d.wantView("removed")

	d.typeText("u")
	if v := lastSpec(t, b.dryRuns, "primarySpec", "parameters", "max_connections"); v != "200" {
		t.Errorf("u should restore max_connections, got %#v", v)
	}
}

func TestEditTUIEditsTheYAMLInEditor(t *testing.T) {
	b := &stubBackend{}
	d := newTUI(t, readyDBCluster(), b)
	d.typeText("e")
	path := d.m.editPath
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(raw)
	if strings.Contains(content, "status:") || !strings.Contains(content, "memory: 8Gi") {
		t.Fatalf("editor file should hold the spec without status:\n%s", content)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(content, "memory: 8Gi", "memory: 16Gi", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	d.send(editorDoneMsg{})

	if v := lastSpec(t, b.dryRuns, "primarySpec", "resources", "memory"); v != "16Gi" {
		t.Errorf("memory = %#v, want 16Gi from the editor", v)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("the temp file should be removed, stat: %v", err)
	}
}

func TestEditTUIKeepsStateWhenTheEditorFileIsBroken(t *testing.T) {
	d := newTUI(t, readyDBCluster(), &stubBackend{})
	d.typeText("e")
	if err := os.WriteFile(d.m.editPath, []byte("spec: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	d.send(editorDoneMsg{})
	d.wantView("invalid YAML")
	if d.m.session.dirty() {
		t.Error("a broken file must not change anything")
	}
}

func TestEditBackendDryRunSendsDryRunAll(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	var dryRun []string
	dyn.PrependReactor("update", "dbclusters", func(a clienttesting.Action) (bool, runtime.Object, error) {
		u := a.(clienttesting.UpdateActionImpl)
		dryRun = u.UpdateOptions.DryRun
		return true, u.Object, nil // the API server doesn't store dry runs
	})
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	s := newEditSession(readyDBCluster())
	if err := s.set(fieldPath{"primarySpec", "resources", "cpu"}, "8"); err != nil {
		t.Fatal(err)
	}
	if err := (clusterEditBackend{c: c, name: "big-db"}).dryRun(context.Background(), s.object()); err != nil {
		t.Fatal(err)
	}
	if len(dryRun) != 1 || dryRun[0] != metav1.DryRunAll {
		t.Errorf("DryRun = %v, want [All]", dryRun)
	}
}

func TestEditBackendErrorsSayWhereToFixRBAC(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	dyn.PrependReactor("update", "dbclusters", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "alloydbomni.dbadmin.goog", Resource: "dbclusters"}, "big-db", nil)
	})
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	err = (clusterEditBackend{c: c, name: "big-db"}).dryRun(context.Background(), readyDBCluster())
	for _, want := range []string{"permission denied", `"omni-test"`, `"dev-gke"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want error mentioning %s, got %v", want, err)
		}
	}
}

func TestEditInteractiveNeedsATerminal(t *testing.T) {
	opts, _ := editEnv(t, readyDBCluster())
	_, err := runCmd(t, newClusterEditCommand(opts), "big-db", "-i")
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("want a terminal error, got %v", err)
	}
}

// withFakeTerminal makes -i run without a terminal, with run standing in
// for the user's session.
func withFakeTerminal(t *testing.T, run func(m *editModel)) {
	t.Helper()
	oldTerm, oldRun := isTerminal, runEditTUI
	t.Cleanup(func() { isTerminal, runEditTUI = oldTerm, oldRun })
	isTerminal = func(any) bool { return true }
	runEditTUI = func(m *editModel, _ io.Reader, _ io.Writer) (*editModel, error) {
		run(m)
		return m, nil
	}
}

func TestEditInteractivePrintsTheAppliedCluster(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	opts.Output = output.OutputJSON
	withFakeTerminal(t, func(m *editModel) {
		if err := m.session.set(fieldPath{"primarySpec", "resources", "memory"}, "12Gi"); err != nil {
			t.Fatal(err)
		}
		applied, err := m.backend.apply(context.Background(), m.session.object())
		if err != nil {
			t.Fatal(err)
		}
		m.applied = applied
	})
	cmd := newClusterEditCommand(opts)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"big-db", "-i"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("-o json stdout isn't JSON (%v):\n%s", err, stdout.String())
	}
	if mem, _, _ := unstructured.NestedString(got, "spec", "primarySpec", "resources", "memory"); mem != "12Gi" {
		t.Errorf("printed memory %q, want 12Gi", mem)
	}
	if mem, _, _ := unstructured.NestedString(storedCluster(t, dyn).Object, "spec", "primarySpec", "resources", "memory"); mem != "12Gi" {
		t.Errorf("stored memory %q, want 12Gi", mem)
	}
}

func TestEditInteractiveQuitWithoutApplyingSaysSo(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	withFakeTerminal(t, func(*editModel) {})
	out, err := runCmd(t, newClusterEditCommand(opts), "big-db", "-i")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no changes made") {
		t.Errorf("want a no-changes message, got:\n%s", out)
	}
	if n := writes(dyn); n != 0 {
		t.Errorf("%d writes, want none", n)
	}
}
