package cmd

import (
	"bytes"
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
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
	opts := &GlobalOptions{
		NewClients: func() (*k8s.Clients, error) { return fakeClients(), nil },
	}
	cmd := newBackupTriggerCommand(opts)
	cmd.SetArgs([]string{"my-db"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected missing cluster to fail")
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
	name, err := resolveBackupPlan(context.Background(), c, "my-db", "", &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if name != "my-db-backupplan" {
		t.Fatalf("plan name = %q", name)
	}
	if errOut.Len() == 0 {
		t.Fatal("expected a note about creating the plan")
	}
}

func TestResolveBackupPlanAlreadyExistsWrongCluster(t *testing.T) {
	existing := defaultBackupPlan("adbo-eugene", "other")
	existing.SetName("my-db-backupplan")
	c := fakeClients(existing)
	if _, err := resolveBackupPlan(context.Background(), c, "my-db", "", &bytes.Buffer{}); err == nil {
		t.Fatal("expected error when the default plan name belongs to another cluster")
	}
}

func TestRunBackupTriggerNoWait(t *testing.T) {
	opts := &GlobalOptions{
		NewClients: func() (*k8s.Clients, error) {
			return fakeClients(dbcluster("my-db", false, "Ready")), nil
		},
	}
	cmd := newBackupTriggerCommand(opts)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"my-db", "--no-wait"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out.Bytes(), []byte("created")) {
		t.Fatalf("got %q", out.String())
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

func TestResolveBackupPlanReusesExisting(t *testing.T) {
	existing := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "BackupPlan",
		"metadata":   map[string]any{"name": "already", "namespace": "adbo-eugene"},
		"spec":       map[string]any{"dbclusterRef": "my-db"},
	}}
	c := fakeClients(existing)
	name, err := resolveBackupPlan(context.Background(), c, "my-db", "", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if name != "already" {
		t.Fatalf("plan name = %q", name)
	}
}
