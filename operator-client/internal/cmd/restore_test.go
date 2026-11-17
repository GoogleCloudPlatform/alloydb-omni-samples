package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"
)

// testBackup is a Backup of cluster with the given phase and backup ID.
func testBackup(name, cluster, phase, id string) *unstructured.Unstructured {
	b := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"dbclusterRef": cluster},
		"status": map[string]any{
			"phase":                phase,
			"completeTime":         "2026-11-16T15:00:00Z",
			"physicalBackupStatus": map[string]any{"backupID": id},
		},
	}}
	b.SetAPIVersion(k8s.APIVersion)
	b.SetKind("Backup")
	b.SetNamespace("default")
	b.SetName(name)
	return b
}

func pwSecret(cluster string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "db-pw-" + cluster, Namespace: "default"},
		Data:       map[string][]byte{cluster: []byte("s3cret")},
	}
}

// restoreOpts is fakeOpts plus Backups, Restores and a typed client for
// Secrets. The fake API server does not fill in generateName, so a reactor
// does it.
func restoreOpts(objs []runtime.Object, secrets ...runtime.Object) (*GlobalOptions, *dynamicfake.FakeDynamicClient) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			k8s.DBClusterGVR: "DBClusterList",
			k8s.BackupGVR:    "BackupList",
			k8s.RestoreGVR:   "RestoreList",
		}, objs...)
	dyn.PrependReactor("create", "restores", func(a k8stesting.Action) (bool, runtime.Object, error) {
		obj := a.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		if obj.GetName() == "" {
			obj.SetName(obj.GetGenerateName() + "abcde")
		}
		return false, nil, nil
	})
	typed := typedfake.NewSimpleClientset(secrets...)
	return &GlobalOptions{
		Output:  output.OutputTable,
		Timeout: 5 * time.Second,
		NewClients: func() (*k8s.Clients, error) {
			return &k8s.Clients{Dynamic: dyn, Typed: typed, Namespace: "default", Context: "test-ctx"}, nil
		},
	}, dyn
}

func restoreCmd(out, errOut *bytes.Buffer, stdin string) *cobra.Command {
	cmd := testCommand(out)
	cmd.SetErr(errOut)
	cmd.SetIn(strings.NewReader(stdin))
	return cmd
}

func listRestores(t *testing.T, opts *GlobalOptions) []unstructured.Unstructured {
	t.Helper()
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	list, err := c.In(k8s.RestoreGVR).List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func TestNewRestoreInPlace(t *testing.T) {
	r := newRestore("default", "my-db", "b1", "", "2026-11-16T15:00:00Z")
	if got := k8s.StringField(r, "spec", "backup"); got != "b1" {
		t.Errorf("spec.backup = %q, want b1", got)
	}
	if got := k8s.StringField(r, "spec", "sourceDBCluster"); got != "my-db" {
		t.Errorf("spec.sourceDBCluster = %q, want my-db", got)
	}
	if _, found, _ := unstructured.NestedMap(r.Object, "spec", "clonedDBClusterConfig"); found {
		t.Error("in-place restore must not set clonedDBClusterConfig")
	}
	if r.GetGenerateName() != "my-db-restore-" || r.GetNamespace() != "default" {
		t.Errorf("metadata = %q/%q", r.GetNamespace(), r.GetGenerateName())
	}
}

func TestNewRestoreClone(t *testing.T) {
	r := newRestore("default", "my-db", "b1", "my-copy", "2026-11-16T15:00:00Z")
	// The CRD forbids backup together with clonedDBClusterConfig.
	if _, found, _ := unstructured.NestedString(r.Object, "spec", "backup"); found {
		t.Error("clone must not set spec.backup")
	}
	if got := k8s.StringField(r, "spec", "pointInTime"); got != "2026-11-16T15:00:00Z" {
		t.Errorf("spec.pointInTime = %q", got)
	}
	if got := k8s.StringField(r, "spec", "clonedDBClusterConfig", "dbclusterName"); got != "my-copy" {
		t.Errorf("clone name = %q, want my-copy", got)
	}
}

func TestNormalizePointInTime(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"2026-11-16T10:00:00-05:00", "2026-11-16T15:00:00Z"},
		{"2026-11-16T15:00:00Z", "2026-11-16T15:00:00Z"},
		{"2026-11-16 15:00:00Z", "2026-11-16T15:00:00Z"},
		{`  "2026-11-16T15:00:00Z" `, "2026-11-16T15:00:00Z"},
		{"'2026-11-16T15:00:00Z'", "2026-11-16T15:00:00Z"},
	} {
		got, err := normalizePointInTime(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("normalizePointInTime(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "yesterday", "2026-11-16", "2026-11-16T10:00:00"} {
		if _, err := normalizePointInTime(bad); err == nil {
			t.Errorf("normalizePointInTime(%q) should fail", bad)
		}
	}
}

func TestRestoreDone(t *testing.T) {
	r := func(phase, complete string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{}}
		u.SetName("r1")
		if phase != "" {
			_ = unstructured.SetNestedField(u.Object, phase, "status", "phase")
		}
		if complete != "" {
			_ = unstructured.SetNestedField(u.Object, complete, "status", "completeTime")
		}
		return u
	}
	for _, tc := range []struct {
		phase, complete string
		done, fails     bool
	}{
		{"", "", false, false},
		{"RestoreInProgress", "", false, false},
		{"RestoreSucceeded", "", true, false},
		{"Completed", "", true, false},
		{"InProgress", "2026-11-16T15:00:00Z", true, false},
		{"RestoreFailed", "", false, true},
	} {
		done, err := restoreDone(r(tc.phase, tc.complete))
		var failed *wait.FailedError
		if done != tc.done || errors.As(err, &failed) != tc.fails {
			t.Errorf("phase %q complete %q: done=%v err=%v", tc.phase, tc.complete, done, err)
		}
	}
}

func TestRestoreInPlaceAsksAndCreatesRestore(t *testing.T) {
	opts, _ := restoreOpts([]runtime.Object{
		dbCluster("default", "my-db", "DBClusterReady"),
		testBackup("b1", "my-db", "Succeeded", "id-1"),
	})
	var out, errOut bytes.Buffer
	if err := runRestore(restoreCmd(&out, &errOut, "y\n"), opts, "my-db", restoreFlags{backup: "b1"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`overwrite ALL data in cluster "my-db"`, `namespace "default"`, `context "test-ctx"`, `backup "b1"`} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("prompt missing %q:\n%s", want, errOut.String())
		}
	}
	items := listRestores(t, opts)
	if len(items) != 1 {
		t.Fatalf("want 1 Restore, got %d", len(items))
	}
	if got := k8s.StringField(&items[0], "spec", "backup"); got != "b1" {
		t.Errorf("spec.backup = %q, want b1", got)
	}
	if !strings.Contains(out.String(), "Started restore") {
		t.Errorf("want no-wait message, got %q", out.String())
	}
}

func TestRestoreDeclinedCreatesNothing(t *testing.T) {
	opts, _ := restoreOpts([]runtime.Object{
		dbCluster("default", "my-db", "DBClusterReady"),
		testBackup("b1", "my-db", "Succeeded", "id-1"),
	})
	var out, errOut bytes.Buffer
	err := runRestore(restoreCmd(&out, &errOut, "n\n"), opts, "my-db", restoreFlags{backup: "b1"})
	if !errors.Is(err, errDeclined) {
		t.Fatalf("want errDeclined, got %v", err)
	}
	if n := len(listRestores(t, opts)); n != 0 {
		t.Errorf("declined restore created %d Restores", n)
	}
}

func TestRestoreFindsBackupByID(t *testing.T) {
	opts, _ := restoreOpts([]runtime.Object{
		dbCluster("default", "my-db", "DBClusterReady"),
		testBackup("b1", "my-db", "Succeeded", "id-1"),
	})
	opts.Yes = true
	var out, errOut bytes.Buffer
	if err := runRestore(restoreCmd(&out, &errOut, ""), opts, "my-db", restoreFlags{backup: "id-1"}); err != nil {
		t.Fatal(err)
	}
	items := listRestores(t, opts)
	if len(items) != 1 || k8s.StringField(&items[0], "spec", "backup") != "b1" {
		t.Errorf("want a Restore of backup b1 (found by ID), got %v", items)
	}
}

func TestRestoreRejects(t *testing.T) {
	for _, tc := range []struct {
		name, backup, want string
		objs               []runtime.Object
	}{
		{"unknown cluster", "b1", "not found", []runtime.Object{testBackup("b1", "my-db", "Succeeded", "id-1")}},
		{"unknown backup", "nope", `backup "nope" not found`, nil},
		{"other cluster's backup", "b2", `belongs to cluster "other-db"`, []runtime.Object{testBackup("b2", "other-db", "Succeeded", "id-2")}},
		{"unfinished backup", "b3", "only finished backups", []runtime.Object{testBackup("b3", "my-db", "InProgress", "id-3")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objs := tc.objs
			if tc.name != "unknown cluster" {
				objs = append(objs, dbCluster("default", "my-db", "DBClusterReady"))
			}
			opts, _ := restoreOpts(objs)
			opts.Yes = true
			var out, errOut bytes.Buffer
			err := runRestore(restoreCmd(&out, &errOut, ""), opts, "my-db", restoreFlags{backup: tc.backup})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
			if n := len(listRestores(t, opts)); n != 0 {
				t.Errorf("rejected restore created %d Restores", n)
			}
		})
	}
}

// withPasswordRef points db at its db-pw-<name> Secret, as clusters
// created with a password do.
func withPasswordRef(db *unstructured.Unstructured) *unstructured.Unstructured {
	_ = unstructured.SetNestedField(db.Object, "db-pw-"+db.GetName(), "spec", "primarySpec", "adminUser", "passwordRef", "name")
	return db
}

func TestRestoreCloneCopiesSecretWithoutPrompt(t *testing.T) {
	opts, _ := restoreOpts([]runtime.Object{
		withPasswordRef(dbCluster("default", "my-db", "DBClusterReady")),
		testBackup("b1", "my-db", "Succeeded", "id-1"),
	}, pwSecret("my-db"))
	var out, errOut bytes.Buffer
	// No -y and no stdin: a clone does not touch my-db, so it must not ask.
	if err := runRestore(restoreCmd(&out, &errOut, ""), opts, "my-db", restoreFlags{backup: "b1", to: "my-copy"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut.String(), "Continue?") {
		t.Errorf("clone should not prompt:\n%s", errOut.String())
	}
	c, _ := opts.Clients()
	s, err := c.Typed.CoreV1().Secrets("default").Get(t.Context(), "db-pw-my-copy", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("password secret not copied: %v", err)
	}
	if string(s.Data["my-copy"]) != "s3cret" {
		t.Errorf("copied secret data = %v", s.Data)
	}
	items := listRestores(t, opts)
	if len(items) != 1 {
		t.Fatalf("want 1 Restore, got %d", len(items))
	}
	if got := k8s.StringField(&items[0], "spec", "clonedDBClusterConfig", "dbclusterName"); got != "my-copy" {
		t.Errorf("clone name = %q", got)
	}
	if got := k8s.StringField(&items[0], "spec", "pointInTime"); got != "2026-11-16T15:00:00Z" {
		t.Errorf("pointInTime = %q, want the backup's completeTime", got)
	}
}

func TestRestoreCloneRefusesExistingTarget(t *testing.T) {
	opts, _ := restoreOpts([]runtime.Object{
		dbCluster("default", "my-db", "DBClusterReady"),
		dbCluster("default", "my-copy", "DBClusterReady"),
		testBackup("b1", "my-db", "Succeeded", "id-1"),
	}, pwSecret("my-db"))
	var out, errOut bytes.Buffer
	err := runRestore(restoreCmd(&out, &errOut, ""), opts, "my-db", restoreFlags{backup: "b1", to: "my-copy"})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want already exists error, got %v", err)
	}
	if n := len(listRestores(t, opts)); n != 0 {
		t.Errorf("created %d Restores", n)
	}
}

func TestRestorePointInTime(t *testing.T) {
	opts, _ := restoreOpts([]runtime.Object{dbCluster("default", "my-db", "DBClusterReady")}, pwSecret("my-db"))
	var out, errOut bytes.Buffer
	f := restoreFlags{pointInTime: "2026-11-16T10:00:00-05:00", to: "my-copy"}
	if err := runRestore(restoreCmd(&out, &errOut, ""), opts, "my-db", f); err != nil {
		t.Fatal(err)
	}
	items := listRestores(t, opts)
	if len(items) != 1 {
		t.Fatalf("want 1 Restore, got %d", len(items))
	}
	if got := k8s.StringField(&items[0], "spec", "pointInTime"); got != "2026-11-16T15:00:00Z" {
		t.Errorf("pointInTime = %q, want normalized UTC", got)
	}

	f.pointInTime = "yesterday"
	f.to = "my-copy-2"
	if err := runRestore(restoreCmd(&out, &errOut, ""), opts, "my-db", f); err == nil || !strings.Contains(err.Error(), "not a valid time") {
		t.Errorf("want invalid time error, got %v", err)
	}
}

func TestRestoreWaitsForRestoreAndCluster(t *testing.T) {
	opts, dyn := restoreOpts([]runtime.Object{
		dbCluster("default", "my-db", "DBClusterReady"),
		testBackup("b1", "my-db", "Succeeded", "id-1"),
	})
	opts.Yes = true

	// Play the operator: mark the Restore finished once it appears.
	go func() {
		for {
			list, err := dyn.Resource(k8s.RestoreGVR).Namespace("default").List(t.Context(), metav1.ListOptions{})
			if err != nil {
				return
			}
			if len(list.Items) == 1 {
				r := &list.Items[0]
				_ = unstructured.SetNestedField(r.Object, "RestoreSucceeded", "status", "phase")
				_, _ = dyn.Resource(k8s.RestoreGVR).Namespace("default").Update(t.Context(), r, metav1.UpdateOptions{})
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	var out, errOut bytes.Buffer
	if err := runRestore(restoreCmd(&out, &errOut, ""), opts, "my-db", restoreFlags{backup: "b1", wait: true}); err != nil {
		t.Fatalf("%v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), `Restored cluster "my-db" from backup "b1"`) {
		t.Errorf("want completion message, got %q", out.String())
	}
	if !strings.Contains(errOut.String(), "Waiting for cluster") {
		t.Errorf("want cluster wait on stderr, got:\n%s", errOut.String())
	}
}

// runRestoreCommand runs the full cobra command so flag rules and RunE checks apply.
func runRestoreCommand(t *testing.T, opts *GlobalOptions, stdin *os.File, args ...string) error {
	t.Helper()
	cmd := newRestoreCommand(opts)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if stdin != nil {
		cmd.SetIn(stdin)
	} else {
		cmd.SetIn(strings.NewReader(""))
	}
	cmd.SetArgs(args)
	return cmd.ExecuteContext(t.Context())
}

func TestRestoreCommandFlagRules(t *testing.T) {
	opts, _ := restoreOpts(nil)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"my-db"}, "at least one of the flags"},
		{[]string{"my-db", "--backup", "b1", "--point-in-time", "2026-11-16T15:00:00Z"}, "none of the others can be"},
		{[]string{"my-db", "--point-in-time", "2026-11-16T15:00:00Z"}, "--point-in-time needs --to"},
	} {
		err := runRestoreCommand(t, opts, nil, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: want error containing %q, got %v", tc.args, tc.want, err)
		}
	}
}

func TestRestoreYesNeedsExplicitCluster(t *testing.T) {
	opts, _ := restoreOpts([]runtime.Object{
		dbCluster("default", "my-db", "DBClusterReady"),
		testBackup("b1", "my-db", "Succeeded", "id-1"),
	})
	opts.SelectedCluster = "my-db"
	opts.Yes = true // -y lives on the root command, so set it directly
	err := runRestoreCommand(t, opts, nil, "--backup", "b1", "--no-wait")
	if err == nil || !strings.Contains(err.Error(), "needs an explicit CLUSTER_NAME") {
		t.Fatalf("want explicit name error, got %v", err)
	}
	if n := len(listRestores(t, opts)); n != 0 {
		t.Errorf("created %d Restores", n)
	}
}

func TestRestoreFailsFastWithoutTerminal(t *testing.T) {
	opts, _ := restoreOpts([]runtime.Object{
		dbCluster("default", "my-db", "DBClusterReady"),
		testBackup("b1", "my-db", "Succeeded", "id-1"),
	})
	// A pipe is an *os.File that is not a terminal, like stdin in CI.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	err = runRestoreCommand(t, opts, r, "my-db", "--backup", "b1", "--no-wait")
	if err == nil || !strings.Contains(err.Error(), "stdin is not a terminal") {
		t.Fatalf("want not-a-terminal error, got %v", err)
	}
}

func TestRestoreCloneSkipsSecretWhenSourceHasNone(t *testing.T) {
	// No passwordRef and no db-pw-my-db Secret: the clone must still go ahead.
	opts, _ := restoreOpts([]runtime.Object{
		dbCluster("default", "my-db", "DBClusterReady"),
		testBackup("b1", "my-db", "Succeeded", "id-1"),
	})
	var out, errOut bytes.Buffer
	if err := runRestore(restoreCmd(&out, &errOut, ""), opts, "my-db", restoreFlags{backup: "b1", to: "my-copy"}); err != nil {
		t.Fatal(err)
	}
	c, _ := opts.Clients()
	if _, err := c.Typed.CoreV1().Secrets("default").Get(t.Context(), "db-pw-my-copy", metav1.GetOptions{}); err == nil {
		t.Error("no source secret, so no secret should be created")
	}
	if n := len(listRestores(t, opts)); n != 1 {
		t.Fatalf("want 1 Restore, got %d", n)
	}
}

func TestRestorePointInTimeWithoutToIsReportedFirst(t *testing.T) {
	// Without a terminal and without -y, the flag mistake must still be the
	// error the user sees, not "pass -y".
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	opts, _ := restoreOpts([]runtime.Object{dbCluster("default", "my-db", "DBClusterReady")})
	err = runRestoreCommand(t, opts, r, "my-db", "--point-in-time", "2026-11-16T10:00:00Z")
	if err == nil || !strings.Contains(err.Error(), "--point-in-time needs --to") {
		t.Fatalf("err = %v, want the --point-in-time needs --to error", err)
	}
}

// noCompleteTime is a Succeeded backup whose status has no completeTime.
func noCompleteTime() *unstructured.Unstructured {
	b := testBackup("b1", "my-db", "Succeeded", "id-1")
	unstructured.RemoveNestedField(b.Object, "status", "completeTime")
	return b
}

func TestRestoreBackupWithoutCompleteTime(t *testing.T) {
	// A clone needs the time, so it refuses instead of sending pointInTime "-".
	opts, _ := restoreOpts([]runtime.Object{dbCluster("default", "my-db", "DBClusterReady"), noCompleteTime()})
	var out, errOut bytes.Buffer
	err := runRestore(restoreCmd(&out, &errOut, ""), opts, "my-db", restoreFlags{backup: "b1", to: "my-copy"})
	if err == nil || !strings.Contains(err.Error(), "no completeTime") {
		t.Fatalf("clone: err = %v, want a no completeTime error", err)
	}
	if n := len(listRestores(t, opts)); n != 0 {
		t.Errorf("clone: created %d Restores, want 0", n)
	}

	// In place does not need the time; the prompt just says it is unknown.
	opts, _ = restoreOpts([]runtime.Object{dbCluster("default", "my-db", "DBClusterReady"), noCompleteTime()})
	errOut.Reset()
	err = runRestore(restoreCmd(&out, &errOut, "n\n"), opts, "my-db", restoreFlags{backup: "b1"})
	if !errors.Is(err, errDeclined) {
		t.Fatalf("in place: err = %v, want errDeclined", err)
	}
	if !strings.Contains(errOut.String(), "taken at an unknown time") || strings.Contains(errOut.String(), "taken -") {
		t.Errorf("in place prompt should say the time is unknown, got:\n%s", errOut.String())
	}
}

func TestRestoreCloneRemovesSecretWhenRestoreFails(t *testing.T) {
	opts, dyn := restoreOpts([]runtime.Object{
		withPasswordRef(dbCluster("default", "my-db", "DBClusterReady")),
		testBackup("b1", "my-db", "Succeeded", "id-1"),
	}, pwSecret("my-db"))
	dyn.PrependReactor("create", "restores", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("admission webhook said no")
	})
	var out, errOut bytes.Buffer
	err := runRestore(restoreCmd(&out, &errOut, ""), opts, "my-db", restoreFlags{backup: "b1", to: "my-copy"})
	if err == nil || !strings.Contains(err.Error(), "admission webhook said no") {
		t.Fatalf("err = %v, want the Restore create error", err)
	}
	c, _ := opts.Clients()
	if _, err := c.Typed.CoreV1().Secrets("default").Get(t.Context(), "db-pw-my-copy", metav1.GetOptions{}); err == nil {
		t.Error("db-pw-my-copy was left behind after the Restore failed")
	}
	if _, err := c.Typed.CoreV1().Secrets("default").Get(t.Context(), "db-pw-my-db", metav1.GetOptions{}); err != nil {
		t.Errorf("the source secret must not be touched: %v", err)
	}
}

func TestWaitForClusterReadyFailsFast(t *testing.T) {
	opts, _ := restoreOpts([]runtime.Object{dbCluster("default", "my-db", "DBClusterReconcileFailed")})
	c, _ := opts.Clients()
	var out, errOut bytes.Buffer
	start := time.Now()
	_, err := waitForClusterReady(t.Context(), restoreCmd(&out, &errOut, ""), opts, c, "my-db")
	if err == nil || !strings.Contains(err.Error(), "failed after the restore") {
		t.Fatalf("err = %v, want a failed-after-restore error", err)
	}
	if time.Since(start) > time.Second {
		t.Errorf("took %s; a Failed phase should stop the wait at once", time.Since(start))
	}
}

func TestWaitForClusterReadyUsesCallersDeadline(t *testing.T) {
	// The caller's ctx is the only clock: opts.Timeout (5s) must not restart it.
	opts, _ := restoreOpts([]runtime.Object{dbCluster("default", "my-db", "DBClusterReconciling")})
	c, _ := opts.Clients()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	var out, errOut bytes.Buffer
	start := time.Now()
	_, err := waitForClusterReady(ctx, restoreCmd(&out, &errOut, ""), opts, c, "my-db")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %s; it should stop when the caller's 200ms runs out", time.Since(start))
	}
}
