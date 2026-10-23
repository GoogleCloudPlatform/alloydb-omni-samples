package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

const (
	testNamespace = "db"
	testContext   = "omni-vm"
)

// fakeClients injects a dynamic fake holding objs, so runStatus can be
// exercised without a kubeconfig or a live cluster.
func fakeClients(objs ...runtime.Object) func() (*k8s.Clients, error) {
	scheme := runtime.NewScheme()
	gv := k8s.DBClusterGVR.GroupVersion()
	scheme.AddKnownTypeWithName(gv.WithKind("DBCluster"), &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(gv.WithKind("DBClusterList"), &unstructured.UnstructuredList{})
	listKinds := map[schema.GroupVersionResource]string{k8s.DBClusterGVR: "DBClusterList"}

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objs...)
	return func() (*k8s.Clients, error) {
		return &k8s.Clients{Dynamic: dyn, Namespace: testNamespace, Context: testContext}, nil
	}
}

// dbCluster is a healthy single-instance DBCluster. Tests mutate the returned
// object to describe stopped, HA, or failing clusters.
func dbCluster() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "DBCluster",
		"metadata": map[string]any{
			"name":      "my-db",
			"namespace": testNamespace,
		},
		"spec": map[string]any{
			"databaseVersion": "15.7.0",
			"primarySpec": map[string]any{
				"resources": map[string]any{
					"cpu":    "2",
					"memory": "8Gi",
					"disks":  []any{map[string]any{"name": "DataDisk", "size": "50Gi"}},
				},
			},
			"availability": map[string]any{"numberOfStandbys": int64(0)},
		},
		"status": map[string]any{
			"phase": clusterReadyPhase,
			"primary": map[string]any{
				"currentDatabaseVersion": "15.7.1",
				"endpoint":               "10.0.0.5:5432",
			},
		},
	}}
}

// runStatusCmd executes `adbo status <args>` against the injected clients.
func runStatusCmd(t *testing.T, opts *GlobalOptions, args ...string) (string, error) {
	t.Helper()
	c := newStatusCommand(opts)
	var buf bytes.Buffer
	c.SetOut(&buf)
	c.SetErr(&buf)
	c.SetArgs(args)
	err := c.Execute()
	return buf.String(), err
}

func TestRootRegistersStatus(t *testing.T) {
	for _, c := range NewRootCommand().Commands() {
		if c.Name() == "status" {
			return
		}
	}
	t.Fatal("status should be registered on the root command")
}

func TestStatusHealthyCluster(t *testing.T) {
	opts := &GlobalOptions{Output: output.OutputTable, NewClients: fakeClients(dbCluster())}
	out, err := runStatusCmd(t, opts, "my-db")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"my-db (namespace db)",
		clusterReadyPhase,
		"running",
		"15.7.1",
		"10.0.0.5:5432",
		"2 CPU, 8Gi memory, 50Gi disk",
		"off (no standbys)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "still provisioning") {
		t.Fatalf("a ready cluster should not print the provisioning hint:\n%s", out)
	}
}

func TestStatusStoppedShowsStopped(t *testing.T) {
	db := dbCluster()
	if err := unstructured.SetNestedField(db.Object, true, "spec", "primarySpec", "isStopped"); err != nil {
		t.Fatal(err)
	}
	opts := &GlobalOptions{Output: output.OutputTable, NewClients: fakeClients(db)}
	out, err := runStatusCmd(t, opts, "my-db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stopped") {
		t.Fatalf("isStopped should render State: stopped:\n%s", out)
	}
}

func TestStatusStandbysShowCount(t *testing.T) {
	db := dbCluster()
	if err := unstructured.SetNestedField(db.Object, int64(2), "spec", "availability", "numberOfStandbys"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(db.Object, false, "spec", "availability", "enableAutoFailover"); err != nil {
		t.Fatal(err)
	}
	opts := &GlobalOptions{Output: output.OutputTable, NewClients: fakeClients(db)}
	out, err := runStatusCmd(t, opts, "my-db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "2 standby") {
		t.Fatalf("numberOfStandbys should render the standby count:\n%s", out)
	}
	if !strings.Contains(out, "auto-failover off") {
		t.Fatalf("enableAutoFailover=false should be called out:\n%s", out)
	}
}

func TestStatusListsCriticalIncidents(t *testing.T) {
	db := dbCluster()
	if err := unstructured.SetNestedField(db.Object, "DBClusterUnknown", "status", "phase"); err != nil {
		t.Fatal(err)
	}
	incidents := []any{
		map[string]any{"code": "DBSE0013", "message": "Database instance is unhealthy"},
		map[string]any{"code": "DBSE0004", "message": "Standby is not replicating"},
	}
	if err := unstructured.SetNestedSlice(db.Object, incidents, "status", "criticalIncidents"); err != nil {
		t.Fatal(err)
	}
	opts := &GlobalOptions{Output: output.OutputTable, NewClients: fakeClients(db)}
	out, err := runStatusCmd(t, opts, "my-db")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Critical incidents (2):",
		"[DBSE0013] Database instance is unhealthy",
		"[DBSE0004] Standby is not replicating",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "still provisioning") {
		t.Fatalf("the provisioning hint should be suppressed when incidents exist:\n%s", out)
	}
}

func TestStatusNotReadyWithoutIncidentsPrintsHint(t *testing.T) {
	db := dbCluster()
	if err := unstructured.SetNestedField(db.Object, "DBClusterBootstrapping", "status", "phase"); err != nil {
		t.Fatal(err)
	}
	opts := &GlobalOptions{Output: output.OutputTable, NewClients: fakeClients(db)}
	out, err := runStatusCmd(t, opts, "my-db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "still provisioning") {
		t.Fatalf("not-ready with no incidents should hint at provisioning:\n%s", out)
	}
	if !strings.Contains(out, "kubectl get pods -n db -w") {
		t.Fatalf("the hint should name the namespace to watch:\n%s", out)
	}
}

func TestStatusJSONReturnsRawObject(t *testing.T) {
	opts := &GlobalOptions{Output: output.OutputJSON, NewClients: fakeClients(dbCluster())}
	out, err := runStatusCmd(t, opts, "my-db")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("-o json should emit the raw object: %v\n%s", err, out)
	}
	if got["kind"] != "DBCluster" {
		t.Fatalf("kind = %v, want DBCluster:\n%s", got["kind"], out)
	}
	phase, _, _ := unstructured.NestedString(got, "status", "phase")
	if phase != clusterReadyPhase {
		t.Fatalf("status.phase = %q, want %q:\n%s", phase, clusterReadyPhase, out)
	}
	if strings.Contains(out, "Resources:") {
		t.Fatalf("-o json must not render the table:\n%s", out)
	}
}

func TestStatusNotFoundNamesNamespaceAndContext(t *testing.T) {
	opts := &GlobalOptions{Output: output.OutputTable, NewClients: fakeClients()}
	_, err := runStatusCmd(t, opts, "missing-db")
	if err == nil {
		t.Fatal("a missing cluster should fail")
	}
	for _, want := range []string{`"missing-db"`, `"db"`, `"omni-vm"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("not-found error missing %s: %v", want, err)
		}
	}
}

func TestStatusRequiresClusterName(t *testing.T) {
	opts := &GlobalOptions{Output: output.OutputTable, NewClients: fakeClients(dbCluster())}
	if _, err := runStatusCmd(t, opts); err == nil {
		t.Fatal("status with no CLUSTER_NAME should fail")
	}
}
