package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

func fakeClients(objects ...runtime.Object) *k8s.Clients {
	return &k8s.Clients{
		Dynamic: dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
			runtime.NewScheme(),
			map[schema.GroupVersionResource]string{
				k8s.DBClusterGVR:  "DBClusterList",
				k8s.BackupPlanGVR: "BackupPlanList",
				k8s.BackupGVR:     "BackupList",
			},
			objects...,
		),
		Namespace: "adbo-eugene",
		Context:   "test",
	}
}

func dbcluster(name string, stopped bool, phase string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "DBCluster",
		"metadata":   map[string]any{"name": name, "namespace": "adbo-eugene"},
		"spec": map[string]any{
			"primarySpec": map[string]any{"isStopped": stopped},
		},
		"status": map[string]any{"phase": phase},
	}}
}

func TestRunBackupTriggerClusterMissing(t *testing.T) {
	c := fakeClients()
	c.Context = ""
	opts := &GlobalOptions{
		NewClients: func() (*k8s.Clients, error) { return c, nil },
	}
	cmd := newBackupTriggerCommand(opts)
	cmd.SetArgs([]string{"my-db"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected missing cluster to fail")
	}
	// FormatErr owns this 404. An empty context must not be printed, and a
	// missing CRD must not be rewritten as "cluster not found".
	if !strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), `context ""`) {
		t.Fatalf("got %v", err)
	}
}

func TestRunBackupTriggerStopped(t *testing.T) {
	opts := &GlobalOptions{
		NewClients: func() (*k8s.Clients, error) {
			return fakeClients(dbcluster("my-db", true, "DBClusterStopped")), nil
		},
	}
	cmd := newBackupTriggerCommand(opts)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"my-db", "--no-wait"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected stopped cluster to fail")
	}
}

func TestResolveBackupPlanCreatesDefault(t *testing.T) {
	c := fakeClients()
	var errOut bytes.Buffer
	name, err := resolveBackupPlan(context.Background(), c, "my-db", "", false, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if name != "my-db-backupplan" {
		t.Fatalf("plan name = %q", name)
	}
	got, getErr := c.In(k8s.BackupPlanGVR).Get(context.Background(), name, metav1.GetOptions{})
	if getErr != nil {
		t.Fatal(getErr)
	}
	if paused, _, _ := unstructured.NestedBool(got.Object, "spec", "paused"); paused {
		t.Fatal("default plan should schedule backups")
	}
	if strings.Contains(errOut.String(), "paused") {
		t.Fatalf("default message: %s", errOut.String())
	}
	if errOut.Len() == 0 {
		t.Fatal("expected a note about creating the plan")
	}
}

func TestResolveBackupPlanAlreadyExistsWrongCluster(t *testing.T) {
	existing := defaultBackupPlan("adbo-eugene", "other", false)
	existing.SetName("my-db-backupplan")
	c := fakeClients(existing)
	if _, err := resolveBackupPlan(context.Background(), c, "my-db", "", false, &bytes.Buffer{}); err == nil {
		t.Fatal("expected error when the default plan name belongs to another cluster")
	}
}

func TestRunBackupTriggerNoWait(t *testing.T) {
	opts := &GlobalOptions{
		NewClients: func() (*k8s.Clients, error) {
			return fakeClients(dbcluster("my-db", false, "Ready"), readyPlan("my-db", "my-db-backupplan")), nil
		},
	}
	cmd := newBackupTriggerCommand(opts)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"my-db", "--no-wait", "--type", "incr"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	// The fake client does not expand generateName, so the name cell is blank.
	// A real API server returns metadata.name; here we check the table shape.
	if !bytes.Contains(stdout.Bytes(), []byte("NAME")) || !bytes.Contains(stdout.Bytes(), []byte("TYPE")) || !bytes.Contains(stdout.Bytes(), []byte("incr")) {
		t.Fatalf("stdout %q", stdout.String())
	}
	if !bytes.Contains(stderr.Bytes(), []byte("created")) {
		t.Fatalf("stderr %q", stderr.String())
	}
}

func TestRunBackupTriggerNoWaitJSON(t *testing.T) {
	opts := &GlobalOptions{
		Output: output.OutputJSON,
		NewClients: func() (*k8s.Clients, error) {
			return fakeClients(dbcluster("my-db", false, "Ready"), readyPlan("my-db", "my-db-backupplan")), nil
		},
	}
	cmd := newBackupTriggerCommand(opts)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"my-db", "--no-wait", "--type", "diff"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte(`"kind": "Backup"`)) || !bytes.Contains(stdout.Bytes(), []byte(`"backupType": "diff"`)) {
		t.Fatalf("stdout %s", stdout.String())
	}
	if bytes.Contains(stdout.Bytes(), []byte("created")) {
		t.Fatalf("json stdout should be the Backup object, got %s", stdout.String())
	}
}

func TestRunBackupTriggerDeleting(t *testing.T) {
	obj := dbcluster("my-db", false, "Ready")
	now := metav1.Now()
	obj.SetDeletionTimestamp(&now)
	assertTriggerRejected(t, obj, "being deleted")
}

func TestRunBackupTriggerSpecIsDeleted(t *testing.T) {
	obj := dbcluster("my-db", false, "Ready")
	obj.Object["spec"].(map[string]any)["isDeleted"] = true
	assertTriggerRejected(t, obj, "spec.isDeleted")
}

func assertTriggerRejected(t *testing.T, obj *unstructured.Unstructured, want string) {
	t.Helper()
	opts := &GlobalOptions{
		NewClients: func() (*k8s.Clients, error) { return fakeClients(obj), nil },
	}
	cmd := newBackupTriggerCommand(opts)
	cmd.SetArgs([]string{"my-db", "--no-wait"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("got %v", err)
	}
}

func TestRunBackupTriggerBadType(t *testing.T) {
	opts := &GlobalOptions{
		NewClients: func() (*k8s.Clients, error) { return fakeClients(), nil },
	}
	cmd := newBackupTriggerCommand(opts)
	cmd.SetArgs([]string{"my-db", "--type", "weekly"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected --type to fail")
	}
}

func TestResolveBackupPlanExplicitMatch(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "BackupPlan",
		"metadata":   map[string]any{"name": "other-plan", "namespace": "adbo-eugene"},
		"spec":       map[string]any{"dbclusterRef": "my-db"},
	}}
	c := fakeClients(existing)
	name, err := resolveBackupPlan(context.Background(), c, "my-db", "other-plan", false, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if name != "other-plan" {
		t.Fatalf("plan name = %q", name)
	}
}

func TestResolveBackupPlanExplicitWrongCluster(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "BackupPlan",
		"metadata":   map[string]any{"name": "other-plan", "namespace": "adbo-eugene"},
		"spec":       map[string]any{"dbclusterRef": "other"},
	}}
	c := fakeClients(existing)
	_, err := resolveBackupPlan(context.Background(), c, "my-db", "other-plan", false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "other") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveBackupPlanExplicitMissing(t *testing.T) {
	c := fakeClients()
	_, err := resolveBackupPlan(context.Background(), c, "my-db", "missing", false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveBackupPlanReusesExisting(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "BackupPlan",
		"metadata":   map[string]any{"name": "already", "namespace": "adbo-eugene"},
		"spec":       map[string]any{"dbclusterRef": "my-db"},
	}}
	c := fakeClients(existing)
	name, err := resolveBackupPlan(context.Background(), c, "my-db", "", false, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if name != "already" {
		t.Fatalf("plan name = %q", name)
	}
}

func readyPlan(clusterName, name string) *unstructured.Unstructured {
	plan := defaultBackupPlan("adbo-eugene", clusterName, false)
	plan.SetName(name)
	plan.Object["status"] = map[string]any{"phase": "Ready"}
	return plan
}

func TestResolveBackupPlanRequiresChoice(t *testing.T) {
	first := readyPlan("my-db", "plan-a")
	second := readyPlan("my-db", "plan-b")
	c := fakeClients(first, second)
	_, err := resolveBackupPlan(context.Background(), c, "my-db", "", false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "--plan") || !strings.Contains(err.Error(), "plan-a") || !strings.Contains(err.Error(), "plan-b") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveBackupPlanPaused(t *testing.T) {
	c := fakeClients()
	name, err := resolveBackupPlan(context.Background(), c, "my-db", "", true, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.In(k8s.BackupPlanGVR).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	paused, _, _ := unstructured.NestedBool(got.Object, "spec", "paused")
	if !paused {
		t.Fatal("expected --paused to set spec.paused")
	}
}

func TestRunBackupTriggerNamedBackup(t *testing.T) {
	opts := &GlobalOptions{
		NewClients: func() (*k8s.Clients, error) {
			return fakeClients(dbcluster("my-db", false, "Ready"), readyPlan("my-db", "my-db-backupplan")), nil
		},
	}
	cmd := newBackupTriggerCommand(opts)
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stdout)
	cmd.SetArgs([]string{"my-db", "--no-wait", "--name", "nightly"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("nightly")) {
		t.Fatalf("stdout %q", stdout.String())
	}
}

func TestRunBackupTriggerRejectsName(t *testing.T) {
	cmd := newBackupTriggerCommand(&GlobalOptions{})
	cmd.SetArgs([]string{"my-db", "--name", "Not_Valid"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "DNS subdomain") {
		t.Fatalf("got %v", err)
	}
}
