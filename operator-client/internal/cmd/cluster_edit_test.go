package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fieldpath "k8s.io/apimachinery/pkg/util/validation/field"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// readyDBCluster is a healthy single-instance cluster with no standbys.
func readyDBCluster() *unstructured.Unstructured {
	obj := stuckDBCluster()
	_ = unstructured.SetNestedField(obj.Object, int64(0), "spec", "availability", "numberOfStandbys")
	_ = unstructured.SetNestedField(obj.Object, int64(2), "spec", "primarySpec", "resources", "cpu")
	_ = unstructured.SetNestedField(obj.Object, "8Gi", "spec", "primarySpec", "resources", "memory")
	_ = unstructured.SetNestedField(obj.Object, "DBClusterReady", "status", "phase")
	unstructured.RemoveNestedField(obj.Object, "status", "criticalIncidents")
	// What the API server returns: managedFields the user shouldn't have to edit.
	obj.SetManagedFields([]metav1.ManagedFieldsEntry{{Manager: "kubectl-client-side-apply", Operation: metav1.ManagedFieldsOperationUpdate}})
	return obj
}

func editEnv(t *testing.T, objs ...runtime.Object) (*GlobalOptions, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	opts := fakeClusterOpts(objs)
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	return opts, c.Dynamic.(*dynamicfake.FakeDynamicClient)
}

func storedCluster(t *testing.T, dyn *dynamicfake.FakeDynamicClient) *unstructured.Unstructured {
	t.Helper()
	obj, err := dyn.Resource(k8s.DBClusterGVR).Namespace("omni-test").Get(context.Background(), "big-db", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return obj
}

func resources(obj *unstructured.Unstructured) (cpu, mem any) {
	cpu, _, _ = unstructured.NestedFieldNoCopy(obj.Object, "spec", "primarySpec", "resources", "cpu")
	mem, _, _ = unstructured.NestedFieldNoCopy(obj.Object, "spec", "primarySpec", "resources", "memory")
	return cpu, mem
}

func writes(dyn *dynamicfake.FakeDynamicClient) int {
	n := 0
	for _, a := range dyn.Actions() {
		switch a.GetVerb() {
		case "patch", "update":
			n++
		}
	}
	return n
}

// withEditor replaces $EDITOR for the test. Each call to the editor runs the
// next step, which can rewrite the file the way a user would.
func withEditor(t *testing.T, steps ...func(t *testing.T, path, content string) string) *int {
	t.Helper()
	calls := 0
	old := editFile
	t.Cleanup(func() { editFile = old })
	editFile = func(path string) error {
		if calls >= len(steps) {
			t.Fatalf("editor opened %d times, want %d", calls+1, len(steps))
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		next := steps[calls](t, path, string(b))
		calls++
		return os.WriteFile(path, []byte(next), 0o600)
	}
	return &calls
}

func TestEditMemoryFlagPatchesOnlyMemory(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	opts.Yes = true

	out, err := runCmd(t, newClusterEditCommand(opts), "big-db", "--memory", "16Gi")
	if err != nil {
		t.Fatalf("edit --memory: %v\n%s", err, out)
	}
	cpu, mem := resources(storedCluster(t, dyn))
	if mem != "16Gi" || cpu != int64(2) {
		t.Fatalf("after edit: cpu=%v memory=%v, want cpu=2 memory=16Gi", cpu, mem)
	}
}

func TestEditCPUFlagKeepsIntegerType(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	opts.Yes = true

	if out, err := runCmd(t, newClusterEditCommand(opts), "big-db", "--cpu", "4"); err != nil {
		t.Fatalf("edit --cpu: %v\n%s", err, out)
	}
	if cpu, _ := resources(storedCluster(t, dyn)); cpu != int64(4) {
		t.Fatalf("cpu = %#v, want int64(4) like the samples", cpu)
	}
}

func TestEditFractionalCPUIsString(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	opts.Yes = true

	if out, err := runCmd(t, newClusterEditCommand(opts), "big-db", "--cpu", "500m"); err != nil {
		t.Fatalf("edit --cpu 500m: %v\n%s", err, out)
	}
	if cpu, _ := resources(storedCluster(t, dyn)); cpu != "500m" {
		t.Fatalf("cpu = %#v, want \"500m\"", cpu)
	}
}

func TestEditRejectsInvalidQuantityWithoutWriting(t *testing.T) {
	for _, args := range [][]string{
		{"--memory", "8Gb"},
		{"--cpu", "two"},
		{"--memory", "0"},
		{"--cpu", "-1"},
		{"--memory", "16"},    // 16 bytes, almost certainly meant 16Gi
		{"--memory", "512Mi"}, // too small to run the database
		{"--cpu", "4Gi"},      // a memory unit
		{"--cpu", "2k"},
	} {
		opts, dyn := editEnv(t, readyDBCluster())
		opts.Yes = true

		_, err := runCmd(t, newClusterEditCommand(opts), append([]string{"big-db"}, args...)...)
		if err == nil {
			t.Errorf("edit %v should fail", args)
			continue
		}
		if !strings.Contains(err.Error(), args[1]) {
			t.Errorf("error %q should quote the bad value %q", err, args[1])
		}
		if n := writes(dyn); n != 0 {
			t.Errorf("edit %v wrote %d times; invalid input must not touch the cluster", args, n)
		}
	}
}

func TestEditAsksBeforeApplying(t *testing.T) {
	for _, tc := range []struct {
		answer  string
		wantMem string
		wantErr bool // a declined prompt must not look like success to scripts
	}{
		{"n\n", "8Gi", true},
		{"", "8Gi", true},
		{"y\n", "16Gi", false},
	} {
		opts, dyn := editEnv(t, readyDBCluster())
		cmd := newClusterEditCommand(opts)
		cmd.SetIn(strings.NewReader(tc.answer))

		out, err := runCmd(t, cmd, "big-db", "--memory", "16Gi")
		if (err != nil) != tc.wantErr {
			t.Fatalf("answer %q: err = %v, wantErr %v\n%s", tc.answer, err, tc.wantErr, out)
		}
		if _, mem := resources(storedCluster(t, dyn)); mem != tc.wantMem {
			t.Fatalf("answer %q: memory = %v, want %s", tc.answer, mem, tc.wantMem)
		}
		if !strings.Contains(out, "8Gi") || !strings.Contains(out, "16Gi") {
			t.Fatalf("prompt should show the old and new memory:\n%s", out)
		}
	}
}

func TestEditWarnsAboutOutageOnlyWithoutStandbys(t *testing.T) {
	single := readyDBCluster()
	ha := readyDBCluster()
	_ = unstructured.SetNestedField(ha.Object, int64(1), "spec", "availability", "numberOfStandbys")

	for _, tc := range []struct {
		obj        *unstructured.Unstructured
		wantOutage bool
	}{
		{single, true},
		{ha, false},
	} {
		opts, _ := editEnv(t, tc.obj)
		opts.Yes = true
		out, err := runCmd(t, newClusterEditCommand(opts), "big-db", "--cpu", "4")
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(out, "no standbys"); got != tc.wantOutage {
			t.Fatalf("outage warning shown=%v, want %v:\n%s", got, tc.wantOutage, out)
		}
	}
}

func TestEditNoOpDoesNotWrite(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	opts.Yes = true

	out, err := runCmd(t, newClusterEditCommand(opts), "big-db", "--cpu", "2", "--memory", "8Gi")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if n := writes(dyn); n != 0 {
		t.Fatalf("unchanged values caused %d writes", n)
	}
}

func TestEditorStripsStatusAndManagedFields(t *testing.T) {
	opts, _ := editEnv(t, readyDBCluster())
	withEditor(t, func(t *testing.T, path, content string) string {
		if !strings.Contains(content, "databaseVersion: 17.5.0") {
			t.Errorf("editor file is missing the spec:\n%s", content)
		}
		for _, hidden := range []string{"status:", "managedFields:", "DBClusterReady"} {
			if strings.Contains(content, hidden) {
				t.Errorf("editor file should not contain %q:\n%s", hidden, content)
			}
		}
		return content
	})

	if out, err := runCmd(t, newClusterEditCommand(opts), "big-db"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestEditorAppliesChanges(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	withEditor(t, func(t *testing.T, path, content string) string {
		return strings.Replace(content, "max_connections: \"200\"", "max_connections: \"500\"", 1)
	})

	out, err := runCmd(t, newClusterEditCommand(opts), "big-db")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	got, _, _ := unstructured.NestedString(storedCluster(t, dyn).Object, "spec", "primarySpec", "parameters", "max_connections")
	if got != "500" {
		t.Fatalf("max_connections = %q, want 500\n%s", got, out)
	}
}

func TestEditorCancelledWhenUnchangedOrEmpty(t *testing.T) {
	for name, step := range map[string]func(*testing.T, string, string) string{
		"unchanged": func(_ *testing.T, _, content string) string { return content },
		"empty":     func(_ *testing.T, _, _ string) string { return "# only a comment\n\n" },
	} {
		opts, dyn := editEnv(t, readyDBCluster())
		withEditor(t, step)

		out, err := runCmd(t, newClusterEditCommand(opts), "big-db")
		if err != nil {
			t.Fatalf("%s: %v\n%s", name, err, out)
		}
		if n := writes(dyn); n != 0 {
			t.Fatalf("%s: %d writes, want none", name, n)
		}
		if !strings.Contains(out, "cancelled") {
			t.Fatalf("%s: want a cancelled message:\n%s", name, out)
		}
	}
}

func TestEditorReopensWithServerError(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	dyn.PrependReactor("update", "dbclusters", func(a clienttesting.Action) (bool, runtime.Object, error) {
		obj := a.(clienttesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		if mem, _, _ := unstructured.NestedString(obj.Object, "spec", "primarySpec", "resources", "memory"); mem == "1Mi" {
			return true, nil, apierrors.NewInvalid(schema.GroupKind{Group: "alloydbomni.dbadmin.goog", Kind: "DBCluster"}, "big-db",
				fieldpath.ErrorList{fieldpath.Invalid(fieldpath.NewPath("spec", "primarySpec", "resources", "memory"), "1Mi", "memory must be at least 1Gi")})
		}
		return false, nil, nil
	})
	calls := withEditor(t,
		func(_ *testing.T, _, content string) string {
			return strings.Replace(content, "memory: 8Gi", "memory: 1Mi", 1)
		},
		func(t *testing.T, _, content string) string {
			if !strings.Contains(content, "memory must be at least 1Gi") {
				t.Errorf("reopened file should show the server error:\n%s", content)
			}
			return strings.Replace(content, "memory: 1Mi", "memory: 4Gi", 1)
		},
	)

	out, err := runCmd(t, newClusterEditCommand(opts), "big-db")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if *calls != 2 {
		t.Fatalf("editor opened %d times, want 2", *calls)
	}
	if _, mem := resources(storedCluster(t, dyn)); mem != "4Gi" {
		t.Fatalf("memory = %v, want 4Gi", mem)
	}
}

func TestEditorAbortsWhenErrorIsNotFixed(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	calls := withEditor(t,
		func(_ *testing.T, _, content string) string {
			return strings.Replace(content, "name: big-db", "name: renamed-db", 1)
		},
		func(_ *testing.T, _, content string) string { return content }, // user quits without fixing
	)

	_, err := runCmd(t, newClusterEditCommand(opts), "big-db")
	if err == nil {
		t.Fatal("renaming the cluster should fail")
	}
	if *calls != 2 {
		t.Fatalf("editor opened %d times, want 2", *calls)
	}
	if n := writes(dyn); n != 0 {
		t.Fatalf("%d writes, want none", n)
	}
	path := savedPath(t, err)
	if b, readErr := os.ReadFile(path); readErr != nil || !strings.Contains(string(b), "renamed-db") {
		t.Fatalf("edits should be kept at %s (err %v)", path, readErr)
	}
}

func TestEditorConflictKeepsEdits(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	dyn.PrependReactor("update", "dbclusters", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(schema.GroupResource{Group: "alloydbomni.dbadmin.goog", Resource: "dbclusters"}, "big-db",
			errorString("the object has been modified"))
	})
	withEditor(t, func(_ *testing.T, _, content string) string {
		return strings.Replace(content, "memory: 8Gi", "memory: 12Gi", 1)
	})

	_, err := runCmd(t, newClusterEditCommand(opts), "big-db")
	if err == nil {
		t.Fatal("a conflicting update should fail")
	}
	path := savedPath(t, err)
	if b, readErr := os.ReadFile(path); readErr != nil || !strings.Contains(string(b), "12Gi") {
		t.Fatalf("edits should be kept at %s (err %v)", path, readErr)
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

// savedPath pulls the kept-edits file path out of an error message.
func savedPath(t *testing.T, err error) string {
	t.Helper()
	for _, f := range strings.Fields(err.Error()) {
		if strings.HasSuffix(f, ".yaml") {
			t.Cleanup(func() { os.Remove(f) })
			return f
		}
	}
	t.Fatalf("error should say where the edits were saved: %v", err)
	return ""
}

func TestRootRegistersClusterEdit(t *testing.T) {
	out, err := runCmd(t, NewRootCommand(), "cluster", "edit", "--help")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "--memory") {
		t.Fatalf("cluster edit not registered:\n%s", out)
	}
}

func TestRunEditorPrefersKubeEditorAndHandlesArgs(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/my cluster.yaml" // a space must survive the shell
	for name, content := range map[string]string{
		path:                      "before",
		dir + "/from-kube-editor": "from-kube-editor",
		dir + "/from-editor":      "from-editor",
	} {
		if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// "cp SRC" receives the path as an argument, like a real editor does.
	t.Setenv("EDITOR", "cp "+dir+"/from-editor")
	t.Setenv("KUBE_EDITOR", "cp "+dir+"/from-kube-editor")

	if err := runEditor(path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "from-kube-editor" {
		t.Fatalf("file = %q, want KUBE_EDITOR to win", b)
	}

	t.Setenv("KUBE_EDITOR", "")
	if err := runEditor(path); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "from-editor" {
		t.Fatalf("file = %q, want EDITOR as the fallback", b)
	}
}

func TestRunEditorReportsFailure(t *testing.T) {
	t.Setenv("KUBE_EDITOR", "false")
	if err := runEditor(t.TempDir() + "/x.yaml"); err == nil {
		t.Fatal("a failing editor should return an error")
	}
}

func TestEditPrintsUpdatedObjectForJSONAndPromptsOnStderr(t *testing.T) {
	opts, _ := editEnv(t, readyDBCluster())
	opts.Output = output.OutputJSON
	cmd := newClusterEditCommand(opts)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetIn(strings.NewReader("y\n"))
	cmd.SetArgs([]string{"big-db", "--memory", "16Gi"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("-o json stdout isn't JSON (%v):\n%s", err, stdout.String())
	}
	if mem, _, _ := unstructured.NestedString(got, "spec", "primarySpec", "resources", "memory"); mem != "16Gi" {
		t.Fatalf("printed object has memory %q, want 16Gi", mem)
	}
	if !strings.Contains(stderr.String(), "Apply these changes?") {
		t.Fatalf("the prompt belongs on stderr, got stderr:\n%s", stderr.String())
	}
}

func TestEditorInterruptedKeepsEdits(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	withEditor(t, func(_ *testing.T, _, content string) string {
		cancel() // Ctrl+C while the editor is open; main turns SIGINT into a cancelled context
		return strings.Replace(content, "memory: 8Gi", "memory: 12Gi", 1)
	})
	cmd := newClusterEditCommand(opts)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"big-db"})

	err := cmd.ExecuteContext(ctx)
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("want an error that says the edit was interrupted, got %v", err)
	}
	if n := writes(dyn); n != 0 {
		t.Fatalf("%d writes after the interrupt, want none", n)
	}
	path := savedPath(t, err)
	if b, readErr := os.ReadFile(path); readErr != nil || !strings.Contains(string(b), "12Gi") {
		t.Fatalf("edits should be kept at %s (err %v)", path, readErr)
	}
}

// interruptedAnswer is a user who presses Ctrl+C at the prompt, then types y.
// The prompt keeps reading after SIGINT because main catches the signal.
type interruptedAnswer struct {
	cancel context.CancelFunc
	sent   bool
}

func (a *interruptedAnswer) Read(p []byte) (int, error) {
	if a.sent {
		return 0, io.EOF
	}
	a.cancel()
	a.sent = true
	return copy(p, "y\n"), nil
}

func TestEditPromptInterruptedAppliesNothing(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := newClusterEditCommand(opts)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetIn(&interruptedAnswer{cancel: cancel})
	cmd.SetArgs([]string{"big-db", "--memory", "16Gi"})

	err := cmd.ExecuteContext(ctx)
	if err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("want an error that says the edit was interrupted, got %v", err)
	}
	if n := writes(dyn); n != 0 {
		t.Fatalf("%d writes after the interrupt, want none", n)
	}
}

func TestEditForbiddenSaysWhereToFixRBAC(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	opts.Yes = true
	dyn.PrependReactor("patch", "dbclusters", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "alloydbomni.dbadmin.goog", Resource: "dbclusters"}, "big-db", nil)
	})

	_, err := runCmd(t, newClusterEditCommand(opts), "big-db", "--memory", "16Gi")
	if err == nil {
		t.Fatal("a Forbidden patch should fail")
	}
	for _, want := range []string{"permission denied", `"omni-test"`, `"dev-gke"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}
