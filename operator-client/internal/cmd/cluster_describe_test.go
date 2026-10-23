package cmd

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// stuckDBCluster mirrors a DBCluster that can't schedule its primary because
// the node is out of memory: the default `kubectl get` view only shows
// DBClusterReconciling, and the reason lives in status.criticalIncidents.
func stuckDBCluster() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "alloydbomni.dbadmin.goog/v1",
		"kind":       "DBCluster",
		"metadata": map[string]any{
			"name":              "big-db",
			"namespace":         "omni-test",
			"uid":               "0b7c5f2e-1d0c-4c55-9a9e-3c1f6a1d2b10",
			"generation":        int64(1),
			"resourceVersion":   "4242",
			"creationTimestamp": "2026-10-02T21:59:00Z",
		},
		"spec": map[string]any{
			"databaseVersion": "17.5.0",
			"availability": map[string]any{
				"numberOfStandbys":   int64(1),
				"enableAutoFailover": true,
				"enableAutoHeal":     true,
			},
			"primarySpec": map[string]any{
				"adminUser": map[string]any{
					"passwordRef": map[string]any{"name": "db-pw-big-db"},
				},
				"parameters": map[string]any{"max_connections": "200"},
				"resources": map[string]any{
					"cpu":    int64(4),
					"memory": "64Gi",
					"disks": []any{
						map[string]any{"name": "DataDisk", "size": "10Gi", "storageClass": "standard-rwo"},
					},
				},
			},
		},
		"status": map[string]any{
			"phase":              "DBClusterReconciling",
			"observedGeneration": int64(1),
			"conditions": []any{
				map[string]any{
					"type":               "Ready",
					"status":             "False",
					"reason":             "Reconciling",
					"message":            "Waiting for primary instance to become ready",
					"lastTransitionTime": "2026-10-02T21:59:05Z",
				},
			},
			"criticalIncidents": []any{
				map[string]any{
					"code":       "DBSE0084",
					"message":    "Platform: Insufficient resources. Insufficient resources to create Pod",
					"createTime": "2026-10-02T21:59:30Z",
					"resource": map[string]any{
						"component": "dataplane",
						"location":  map[string]any{"cluster": "big-db", "namespace": "omni-test"},
					},
				},
			},
			"primary": map[string]any{
				"phase":    "InProgress",
				"endpoint": "10.98.6.183",
				"url":      "10.98.6.183:5432",
			},
		},
	}}
}

func dbclusterEvent(name, kind, object, reason, msg string) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: "omni-test"},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Name: object, Namespace: "omni-test"},
		Type:           corev1.EventTypeWarning,
		Reason:         reason,
		Message:        msg,
		Source:         corev1.EventSource{Component: "alloydb-omni-operator"},
		LastTimestamp:  metav1.NewTime(time.Date(2026, 10, 2, 22, 0, 0, 0, time.UTC)),
	}
}

func fakeClusterOpts(dyn []runtime.Object, typed ...runtime.Object) *GlobalOptions {
	listKinds := map[schema.GroupVersionResource]string{
		k8s.DBClusterGVR: "DBClusterList",
	}
	typedClient := kubefake.NewClientset(typed...)
	// The fake clientset ignores field selectors. Apply the one adbo sends so
	// tests check the query it builds, not just client-side filtering.
	typedClient.PrependReactor("list", "events", func(a clienttesting.Action) (bool, runtime.Object, error) {
		sel := a.(clienttesting.ListAction).GetListRestrictions().Fields
		list := &corev1.EventList{}
		for _, o := range typed {
			e, ok := o.(*corev1.Event)
			if ok && sel.Matches(fields.Set{"involvedObject.kind": e.InvolvedObject.Kind, "involvedObject.name": e.InvolvedObject.Name}) {
				list.Items = append(list.Items, *e)
			}
		}
		return true, list, nil
	})
	// Discovery reports the DBCluster CRD as installed.
	typedClient.Resources = []*metav1.APIResourceList{{
		GroupVersion: "alloydbomni.dbadmin.goog/v1",
		APIResources: []metav1.APIResource{{Name: "dbclusters", Namespaced: true, Kind: "DBCluster"}},
	}}
	clients := &k8s.Clients{
		Dynamic:   dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, dyn...),
		Typed:     typedClient,
		Namespace: "omni-test",
		Context:   "dev-gke",
	}
	return &GlobalOptions{
		Output:     output.OutputTable,
		Timeout:    time.Minute,
		NewClients: func() (*k8s.Clients, error) { return clients, nil },
	}
}

func runCmd(t *testing.T, cmd interface {
	SetOut(w io.Writer)
	SetErr(w io.Writer)
	SetArgs([]string)
	Execute() error
}, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestDescribeShowsSpec(t *testing.T) {
	opts := fakeClusterOpts([]runtime.Object{stuckDBCluster()})

	out, err := runCmd(t, newClusterDescribeCommand(opts), "big-db")
	if err != nil {
		t.Fatalf("describe: %v\n%s", err, out)
	}
	for _, want := range []string{"big-db", "omni-test", "17.5.0", "64Gi", "DataDisk", "10Gi", "standard-rwo", "db-pw-big-db", "max_connections"} {
		if !strings.Contains(out, want) {
			t.Errorf("describe output missing %q", want)
		}
	}
	if t.Failed() {
		t.Log(out)
	}
}

func TestDescribeSurfacesFailureReasons(t *testing.T) {
	opts := fakeClusterOpts([]runtime.Object{stuckDBCluster()})

	out, err := runCmd(t, newClusterDescribeCommand(opts), "big-db")
	if err != nil {
		t.Fatalf("describe: %v\n%s", err, out)
	}
	for _, want := range []string{
		"DBClusterReconciling",
		"InProgress",
		"DBSE0084",
		"Insufficient resources to create Pod",
		"Waiting for primary instance to become ready",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("describe output missing %q", want)
		}
	}
	if t.Failed() {
		t.Log(out)
	}
}

func TestDescribeShowsOnlyThisClustersEvents(t *testing.T) {
	opts := fakeClusterOpts(
		[]runtime.Object{stuckDBCluster()},
		dbclusterEvent("e1", "DBCluster", "big-db", "ReconcileFailed", "primary instance not ready"),
		dbclusterEvent("e2", "DBCluster", "other-db", "ReconcileFailed", "belongs to another cluster"),
	)

	out, err := runCmd(t, newClusterDescribeCommand(opts), "big-db")
	if err != nil {
		t.Fatalf("describe: %v\n%s", err, out)
	}
	if !strings.Contains(out, "primary instance not ready") {
		t.Errorf("missing this cluster's event:\n%s", out)
	}
	if strings.Contains(out, "belongs to another cluster") {
		t.Errorf("showed another cluster's event:\n%s", out)
	}
}

func TestDescribeUsesSelectedCluster(t *testing.T) {
	opts := fakeClusterOpts([]runtime.Object{stuckDBCluster()})
	opts.SelectedCluster = "big-db"

	out, err := runCmd(t, newClusterDescribeCommand(opts))
	if err != nil {
		t.Fatalf("describe with selected cluster: %v\n%s", err, out)
	}
	if !strings.Contains(out, "DBSE0084") {
		t.Fatalf("did not describe the selected cluster:\n%s", out)
	}
}

func TestDescribeNotFoundNamesNamespaceAndContext(t *testing.T) {
	opts := fakeClusterOpts(nil)

	_, err := runCmd(t, newClusterDescribeCommand(opts), "nope")
	if err == nil {
		t.Fatal("describe of a missing cluster should fail")
	}
	for _, want := range []string{`"nope"`, `"omni-test"`, `"dev-gke"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}

func TestDescribeYAMLIsFullObject(t *testing.T) {
	opts := fakeClusterOpts([]runtime.Object{stuckDBCluster()})
	opts.Output = output.OutputYAML

	out, err := runCmd(t, newClusterDescribeCommand(opts), "big-db")
	if err != nil {
		t.Fatalf("describe -o yaml: %v\n%s", err, out)
	}
	for _, want := range []string{"kind: DBCluster", "criticalIncidents:", "passwordRef:"} {
		if !strings.Contains(out, want) {
			t.Errorf("-o yaml missing %q:\n%s", want, out)
		}
	}
}

func TestDescribeBeforeStatusIsReported(t *testing.T) {
	fresh := stuckDBCluster()
	unstructured.RemoveNestedField(fresh.Object, "status")
	opts := fakeClusterOpts([]runtime.Object{fresh})

	out, err := runCmd(t, newClusterDescribeCommand(opts), "big-db")
	if err != nil {
		t.Fatalf("describe of a cluster without status: %v\n%s", err, out)
	}
	if !strings.Contains(out, "17.5.0") {
		t.Fatalf("spec missing when status is empty:\n%s", out)
	}
}

func TestRootRegistersClusterDescribe(t *testing.T) {
	root := NewRootCommand()
	out, err := runCmd(t, root, "cluster", "describe", "--help")
	if err != nil {
		t.Fatalf("adbo cluster describe --help: %v\n%s", err, out)
	}
	if !strings.Contains(out, "describe [CLUSTER_NAME]") {
		t.Fatalf("cluster describe not registered:\n%s", out)
	}
}

func TestDescribeShowsWhetherOperatorCaughtUp(t *testing.T) {
	behind := stuckDBCluster()
	behind.SetGeneration(3)
	_ = unstructured.SetNestedField(behind.Object, int64(2), "status", "observedGeneration")
	caughtUp := stuckDBCluster() // generation 1, observed 1

	out, err := runCmd(t, newClusterDescribeCommand(fakeClusterOpts([]runtime.Object{behind})), "big-db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hasn't processed the latest spec") {
		t.Fatalf("generation 3 vs observed 2 should say the operator is behind:\n%s", out)
	}

	out, err = runCmd(t, newClusterDescribeCommand(fakeClusterOpts([]runtime.Object{caughtUp})), "big-db")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "hasn't processed the latest spec") {
		t.Fatalf("observed == generation should not say the operator is behind:\n%s", out)
	}
}

func TestDescribeShowsIncidentStackTrace(t *testing.T) {
	obj := stuckDBCluster()
	incidents, _, _ := unstructured.NestedSlice(obj.Object, "status", "criticalIncidents")
	incidents[0].(map[string]any)["stackTrace"] = []any{
		map[string]any{"component": "dbdaemon", "message": "0/1 nodes are available: 1 Insufficient memory"},
	}
	_ = unstructured.SetNestedSlice(obj.Object, incidents, "status", "criticalIncidents")

	out, err := runCmd(t, newClusterDescribeCommand(fakeClusterOpts([]runtime.Object{obj})), "big-db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "0/1 nodes are available: 1 Insufficient memory") {
		t.Fatalf("incident stack trace missing:\n%s", out)
	}
}

func TestDescribeShowsEveryOtherSpecAndStatusField(t *testing.T) {
	obj := stuckDBCluster()
	_ = unstructured.SetNestedSlice(obj.Object, []any{
		map[string]any{"key": "alloydb-node-type", "operator": "Exists", "effect": "NoSchedule"},
	}, "spec", "primarySpec", "schedulingconfig", "tolerations")
	_ = unstructured.SetNestedField(obj.Object, "gcr.io/custom/pg:17", "spec", "databaseImage")
	_ = unstructured.SetNestedField(obj.Object, "failover-7", "status", "latestFailoverStatus", "failoverName")

	out, err := runCmd(t, newClusterDescribeCommand(fakeClusterOpts([]runtime.Object{obj})), "big-db")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"alloydb-node-type", "NoSchedule", "gcr.io/custom/pg:17", "failover-7"} {
		if !strings.Contains(out, want) {
			t.Errorf("describe hides %q", want)
		}
	}
	// Fields already shown in their own rows shouldn't be repeated.
	if n := strings.Count(out, "db-pw-big-db"); n != 1 {
		t.Errorf("admin secret shown %d times, want once", n)
	}
	if t.Failed() {
		t.Log(out)
	}
}

func TestDescribeReadReplicasDefaultsToFalse(t *testing.T) {
	out, err := runCmd(t, newClusterDescribeCommand(fakeClusterOpts([]runtime.Object{stuckDBCluster()})), "big-db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Read Replicas:     false") {
		t.Fatalf("unset enableStandbyAsReadReplica means false:\n%s", out)
	}
}

func TestDescribeWhenDBClusterTypeIsNotInstalled(t *testing.T) {
	opts := fakeClusterOpts(nil)
	c, _ := opts.Clients()
	c.Typed.(*kubefake.Clientset).Resources = nil // discovery: no alloydbomni.dbadmin.goog/v1
	// client-go turns the API server's plain-text 404 for an unknown path
	// into this, with the requested name filled in.
	c.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "dbclusters", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewGenericServerResponse(404, "get", schema.GroupResource{Group: "alloydbomni.dbadmin.goog", Resource: "dbclusters"}, "big-db", "404 page not found", 0, true)
	})

	_, err := runCmd(t, newClusterDescribeCommand(opts), "big-db")
	if err == nil || !strings.Contains(err.Error(), "operator") || strings.Contains(err.Error(), "pass -n") {
		t.Fatalf("a missing DBCluster CRD should point at the operator install, not the namespace: %v", err)
	}
}

func TestDescribeForbiddenSaysWhereToFixRBAC(t *testing.T) {
	opts := fakeClusterOpts(nil)
	c, _ := opts.Clients()
	c.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "dbclusters", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "alloydbomni.dbadmin.goog", Resource: "dbclusters"}, "big-db", nil)
	})

	_, err := runCmd(t, newClusterDescribeCommand(opts), "big-db")
	if err == nil {
		t.Fatal("a Forbidden get should fail")
	}
	for _, want := range []string{"permission denied", `"omni-test"`, `"dev-gke"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %s", err, want)
		}
	}
}
