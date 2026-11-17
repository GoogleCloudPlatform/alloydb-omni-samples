package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	typedfake "k8s.io/client-go/kubernetes/fake"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// fakeSide is one fake Kubernetes cluster: its clients plus the raw
// dynamic fake so a test can play the operator.
type fakeSide struct {
	c   *k8s.Clients
	dyn *dynamicfake.FakeDynamicClient
}

func newFakeSide(kubeContext, ns string, objs []runtime.Object, secrets ...runtime.Object) fakeSide {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			k8s.DBClusterGVR:   "DBClusterList",
			k8s.ReplicationGVR: "ReplicationList",
		}, objs...)
	return fakeSide{
		c:   &k8s.Clients{Dynamic: dyn, Typed: typedfake.NewSimpleClientset(secrets...), Namespace: ns, Context: kubeContext},
		dyn: dyn,
	}
}

// replicateOpts points newSideClients at src for --source-context us and
// at dst for --target-context eu, and restores it when the test ends.
func replicateOpts(t *testing.T, src, dst fakeSide) *GlobalOptions {
	t.Helper()
	orig := newSideClients
	t.Cleanup(func() { newSideClients = orig })
	newSideClients = func(_ *GlobalOptions, kubeContext, _ string) (*k8s.Clients, error) {
		switch kubeContext {
		case "us":
			return src.c, nil
		case "eu":
			return dst.c, nil
		}
		return nil, errors.New("unexpected context " + kubeContext)
	}
	return &GlobalOptions{Output: output.OutputTable, Timeout: 5 * time.Second}
}

var usToEU = replicateFlags{sourceContext: "us", targetContext: "eu"}

func setConditions(obj *unstructured.Unstructured, kv ...string) {
	var conds []any
	for i := 0; i+1 < len(kv); i += 2 {
		conds = append(conds, map[string]any{"type": kv[i], "status": kv[i+1]})
	}
	_ = unstructured.SetNestedSlice(obj.Object, conds, "status", "conditions")
}

// publishUpstream fills in what the operator writes on a ready upstream.
func publishUpstream(up *unstructured.Unstructured) {
	setConditions(up, "Ready", "True")
	_ = unstructured.SetNestedMap(up.Object, map[string]any{
		"host":                "10.0.0.5",
		"port":                int64(5432),
		"username":            "alloydbreplica",
		"replicationSlotName": "my_db_slot",
		"password":            map[string]any{"name": "ha-rep-pw-my-db"},
	}, "status", "upstream")
}

func repSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ha-rep-pw-my-db", Namespace: "prod"},
		Data:       map[string][]byte{"password": []byte("r3pl")},
	}
}

// playOperator waits for name to appear on side, applies mutate, and
// writes it back, the way the operator would.
func playOperator(t *testing.T, side fakeSide, ns, name string, mutate func(*unstructured.Unstructured)) {
	go func() {
		for {
			obj, err := side.dyn.Resource(k8s.ReplicationGVR).Namespace(ns).Get(context.Background(), name, metav1.GetOptions{})
			if err == nil {
				mutate(obj)
				_, _ = side.dyn.Resource(k8s.ReplicationGVR).Namespace(ns).Update(context.Background(), obj, metav1.UpdateOptions{})
				return
			}
			select {
			case <-t.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}()
}

func getReplication(t *testing.T, side fakeSide, ns, name string) *unstructured.Unstructured {
	t.Helper()
	obj, err := side.dyn.Resource(k8s.ReplicationGVR).Namespace(ns).Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get replication %s: %v", name, err)
	}
	return obj
}

func TestNewUpstream(t *testing.T) {
	up := newUpstream("prod", "my-db", "my-copy")
	if up.GetName() != "my-copy-upstream" || up.GetNamespace() != "prod" || up.GetKind() != "Replication" {
		t.Errorf("metadata = %s/%s %s", up.GetNamespace(), up.GetName(), up.GetKind())
	}
	if got := k8s.StringField(up, "spec", "dbcluster", "name"); got != "my-db" {
		t.Errorf("dbcluster = %q, want the source", got)
	}
	if _, found, _ := unstructured.NestedMap(up.Object, "spec", "upstream"); !found {
		t.Error("spec.upstream must be present (even empty) to mark the source side")
	}
}

func TestNewDownstream(t *testing.T) {
	up := newUpstream("prod", "my-db", "my-copy")
	publishUpstream(up)
	down := newDownstream("dr", "my-copy", "my-copy-replication-pw", up)
	if down.GetName() != "my-copy-downstream" || down.GetNamespace() != "dr" {
		t.Errorf("metadata = %s/%s", down.GetNamespace(), down.GetName())
	}
	for path, want := range map[string]string{
		"dbcluster.name":                 "my-copy",
		"downstream.host":                "10.0.0.5",
		"downstream.username":            "alloydbreplica",
		"downstream.replicationSlotName": "my_db_slot",
		"downstream.password.name":       "my-copy-replication-pw",
		"downstream.control":             "setup",
	} {
		if got := k8s.StringField(down, append([]string{"spec"}, strings.Split(path, ".")...)...); got != want {
			t.Errorf("spec.%s = %q, want %q", path, got, want)
		}
	}
	if port, _, _ := unstructured.NestedInt64(down.Object, "spec", "downstream", "port"); port != 5432 {
		t.Errorf("port = %d, want 5432", port)
	}
}

func TestUpstreamReady(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(up *unstructured.Unstructured)
		done   bool
	}{
		{"published", func(*unstructured.Unstructured) {}, true},
		{"not ready", func(up *unstructured.Unstructured) { setConditions(up, "Ready", "False") }, false},
		{"no host yet", func(up *unstructured.Unstructured) {
			unstructured.RemoveNestedField(up.Object, "status", "upstream", "host")
		}, false},
		{"no slot yet", func(up *unstructured.Unstructured) {
			unstructured.RemoveNestedField(up.Object, "status", "upstream", "replicationSlotName")
		}, false},
		{"no password yet", func(up *unstructured.Unstructured) {
			unstructured.RemoveNestedField(up.Object, "status", "upstream", "password")
		}, false},
		{"no port yet", func(up *unstructured.Unstructured) {
			unstructured.RemoveNestedField(up.Object, "status", "upstream", "port")
		}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			up := newUpstream("prod", "my-db", "my-copy")
			publishUpstream(up)
			tc.mutate(up)
			if done, err := upstreamReady(up); err != nil || done != tc.done {
				t.Fatalf("done=%v err=%v, want %v", done, err, tc.done)
			}
		})
	}
}

func TestDownstreamReady(t *testing.T) {
	for _, tc := range []struct {
		conds []string
		done  bool
	}{
		{[]string{"Ready", "True", "Healthy", "True"}, true},
		{[]string{"Ready", "True", "Healthy", "False"}, false},
		{[]string{"Ready", "False", "Healthy", "True"}, false},
		{nil, false},
	} {
		down := newDownstream("dr", "my-copy", "pw", newUpstream("prod", "my-db", "my-copy"))
		setConditions(down, tc.conds...)
		if done, _ := downstreamReady(down); done != tc.done {
			t.Errorf("conditions %v: done=%v, want %v", tc.conds, done, tc.done)
		}
	}
}

func TestReplicateCreateAcrossClusters(t *testing.T) {
	src := newFakeSide("us", "prod", []runtime.Object{dbCluster("prod", "my-db", "DBClusterReady")}, repSecret())
	dst := newFakeSide("eu", "dr", []runtime.Object{dbCluster("dr", "my-copy", "DBClusterReady")})
	opts := replicateOpts(t, src, dst)
	playOperator(t, src, "prod", "my-copy-upstream", publishUpstream)
	playOperator(t, dst, "dr", "my-copy-downstream", func(d *unstructured.Unstructured) {
		setConditions(d, "Ready", "True", "Healthy", "True")
	})

	f := usToEU
	f.wait = true
	var out, errOut bytes.Buffer
	if err := runReplicateCreate(scaleCmd(&out, &errOut, "y\n"), opts, "my-db", "my-copy", f); err != nil {
		t.Fatalf("%v\nstderr:\n%s", err, errOut.String())
	}
	for _, want := range []string{"my-copy", `in namespace "dr" (context "eu")`, `in namespace "prod" (context "us")`, "ALL data", "Continue?"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("prompt missing %q:\n%s", want, errOut.String())
		}
	}
	if !strings.Contains(out.String(), `Replicating "my-db" to "my-copy".`) {
		t.Errorf("out = %q", out.String())
	}

	// The upstream lives with the source, the downstream with the target.
	getReplication(t, src, "prod", "my-copy-upstream")
	down := getReplication(t, dst, "dr", "my-copy-downstream")
	if got := k8s.StringField(down, "spec", "downstream", "host"); got != "10.0.0.5" {
		t.Errorf("downstream host = %q, want the upstream's", got)
	}
	if got := k8s.StringField(down, "spec", "downstream", "password", "name"); got != "my-copy-replication-pw" {
		t.Errorf("downstream password = %q", got)
	}

	// The password was copied to the target side, where the downstream can read it.
	s, err := dst.c.Typed.CoreV1().Secrets("dr").Get(t.Context(), "my-copy-replication-pw", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("secret not copied to the target: %v", err)
	}
	if string(s.Data["password"]) != "r3pl" {
		t.Errorf("copied secret data = %v", s.Data)
	}
}

func TestReplicateCreateNoWaitStillWaitsForUpstream(t *testing.T) {
	// The downstream needs the upstream's details, so even --no-wait must
	// wait for those; it only skips waiting for the target to catch up.
	src := newFakeSide("us", "prod", []runtime.Object{dbCluster("prod", "my-db", "DBClusterReady")}, repSecret())
	dst := newFakeSide("eu", "dr", []runtime.Object{dbCluster("dr", "my-copy", "DBClusterReady")})
	opts := replicateOpts(t, src, dst)
	opts.Yes = true
	playOperator(t, src, "prod", "my-copy-upstream", publishUpstream)

	var out, errOut bytes.Buffer
	if err := runReplicateCreate(scaleCmd(&out, &errOut, ""), opts, "my-db", "my-copy", usToEU); err != nil {
		t.Fatalf("%v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "Check progress") {
		t.Errorf("out = %q", out.String())
	}
	getReplication(t, dst, "dr", "my-copy-downstream")
}

func TestReplicateCreateDeclinedCreatesNothing(t *testing.T) {
	src := newFakeSide("us", "prod", []runtime.Object{dbCluster("prod", "my-db", "DBClusterReady")}, repSecret())
	dst := newFakeSide("eu", "dr", []runtime.Object{dbCluster("dr", "my-copy", "DBClusterReady")})
	opts := replicateOpts(t, src, dst)
	var out, errOut bytes.Buffer
	err := runReplicateCreate(scaleCmd(&out, &errOut, "n\n"), opts, "my-db", "my-copy", usToEU)
	if !errors.Is(err, errDeclined) {
		t.Fatalf("want errDeclined, got %v", err)
	}
	if list, _ := src.dyn.Resource(k8s.ReplicationGVR).Namespace("prod").List(t.Context(), metav1.ListOptions{}); len(list.Items) != 0 {
		t.Errorf("declining created %d upstream Replications", len(list.Items))
	}
}

func TestReplicateCreateChecksBothClustersFirst(t *testing.T) {
	// Target missing: fail before touching the source.
	src := newFakeSide("us", "prod", []runtime.Object{dbCluster("prod", "my-db", "DBClusterReady")}, repSecret())
	dst := newFakeSide("eu", "dr", nil)
	opts := replicateOpts(t, src, dst)
	opts.Yes = true
	var out, errOut bytes.Buffer
	err := runReplicateCreate(scaleCmd(&out, &errOut, ""), opts, "my-db", "my-copy", usToEU)
	if err == nil || !strings.Contains(err.Error(), "my-copy") {
		t.Fatalf("want a not-found error for the target, got %v", err)
	}
	if list, _ := src.dyn.Resource(k8s.ReplicationGVR).Namespace("prod").List(t.Context(), metav1.ListOptions{}); len(list.Items) != 0 {
		t.Errorf("created an upstream although the target does not exist")
	}
}

func TestReplicateCreateRejectsSelf(t *testing.T) {
	side := newFakeSide("us", "prod", []runtime.Object{dbCluster("prod", "my-db", "DBClusterReady")})
	opts := replicateOpts(t, side, side)
	var out, errOut bytes.Buffer
	f := replicateFlags{sourceContext: "us", targetContext: "us"}
	err := runReplicateCreate(scaleCmd(&out, &errOut, ""), opts, "my-db", "my-db", f)
	if err == nil || !strings.Contains(err.Error(), "replicate to itself") {
		t.Fatalf("err = %v", err)
	}
}

func TestReplicateCreateUpstreamTimeout(t *testing.T) {
	// The operator never publishes: the error must say where to look.
	src := newFakeSide("us", "prod", []runtime.Object{dbCluster("prod", "my-db", "DBClusterReady")}, repSecret())
	dst := newFakeSide("eu", "dr", []runtime.Object{dbCluster("dr", "my-copy", "DBClusterReady")})
	opts := replicateOpts(t, src, dst)
	opts.Yes = true
	opts.Timeout = 300 * time.Millisecond
	var out, errOut bytes.Buffer
	err := runReplicateCreate(scaleCmd(&out, &errOut, ""), opts, "my-db", "my-copy", usToEU)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "my-copy-upstream") {
		t.Fatalf("want a timeout naming the upstream, got %v", err)
	}
}

func TestReplicateCreateNeedsTwoNames(t *testing.T) {
	opts := &GlobalOptions{Output: output.OutputTable}
	for _, args := range [][]string{{"create"}, {"create", "my-db"}, {"create", "a", "b", "c"}} {
		cmd := newReplicateCommand(opts)
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(args)
		if err := cmd.ExecuteContext(t.Context()); err == nil {
			t.Errorf("args %v: want an error, replicate create needs exactly SOURCE and TARGET", args)
		}
	}
}
