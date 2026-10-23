package cmd

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestEditorWorksWithLongClusterNames(t *testing.T) {
	// Object names can be 253 characters; filenames are capped at 255 bytes.
	name := strings.Repeat("a", 253)
	cluster := readyDBCluster()
	cluster.SetName(name)
	opts, _ := editEnv(t, cluster)
	calls := withEditor(t, func(_ *testing.T, _, content string) string { return content })

	if out, err := runCmd(t, newClusterEditCommand(opts), name); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if *calls != 1 {
		t.Fatalf("editor opened %d times, want 1", *calls)
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
	if !strings.Contains(out, "KUBE_EDITOR") {
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

func TestEditPointsResizingToClusterScale(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	for _, flag := range []string{"--cpu", "--memory"} {
		_, err := runCmd(t, newClusterEditCommand(opts), "big-db", flag, "4")
		if err == nil || !strings.Contains(err.Error(), "adbo cluster scale") {
			t.Errorf("%s: want a pointer to `adbo cluster scale`, got %v", flag, err)
		}
	}
	if n := writes(dyn); n != 0 {
		t.Fatalf("%d writes, want none", n)
	}
}

func TestEditorPrintsUpdatedObjectForJSON(t *testing.T) {
	opts, _ := editEnv(t, readyDBCluster())
	opts.Output = output.OutputJSON
	withEditor(t, func(_ *testing.T, _, content string) string {
		return strings.Replace(content, "memory: 8Gi", "memory: 12Gi", 1)
	})
	cmd := newClusterEditCommand(opts)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"big-db"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, stderr.String())
	}
	var got map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("-o json stdout isn't JSON (%v):\n%s", err, stdout.String())
	}
	if mem, _, _ := unstructured.NestedString(got, "spec", "primarySpec", "resources", "memory"); mem != "12Gi" {
		t.Fatalf("printed object has memory %q, want 12Gi", mem)
	}
}

func TestEditorForbiddenSaysWhereToFixRBAC(t *testing.T) {
	opts, dyn := editEnv(t, readyDBCluster())
	dyn.PrependReactor("update", "dbclusters", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "alloydbomni.dbadmin.goog", Resource: "dbclusters"}, "big-db", nil)
	})
	var reopened string
	withEditor(t,
		func(_ *testing.T, _, content string) string {
			return strings.Replace(content, "memory: 8Gi", "memory: 12Gi", 1)
		},
		func(_ *testing.T, _, content string) string {
			reopened = content
			return content // quit without fixing
		},
	)

	if _, err := runCmd(t, newClusterEditCommand(opts), "big-db"); err == nil {
		t.Fatal("a Forbidden update that isn't fixed should fail")
	}
	for _, want := range []string{"permission denied", `"omni-test"`, `"dev-gke"`} {
		if !strings.Contains(reopened, want) {
			t.Errorf("the reopened editor should mention %s:\n%s", want, reopened)
		}
	}
}
