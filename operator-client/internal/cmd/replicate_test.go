package cmd

import (
	"bytes"
	"context"
	"encoding/json"
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

// seedReplication stores a Replication on side as if create had run.
func seedReplication(t *testing.T, side fakeSide, obj *unstructured.Unstructured) {
	t.Helper()
	if _, err := side.dyn.Resource(k8s.ReplicationGVR).Namespace(obj.GetNamespace()).Create(t.Context(), obj, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestReplicateStatusShowsBothSides(t *testing.T) {
	src := newFakeSide("us", "prod", nil)
	dst := newFakeSide("eu", "dr", nil)
	up := newUpstream("prod", "my-db", "my-copy")
	publishUpstream(up)
	seedReplication(t, src, up)
	down := newDownstream("dr", "my-copy", "pw", up)
	setConditions(down, "Ready", "True", "Healthy", "False")
	_ = unstructured.SetNestedSlice(down.Object, []any{
		map[string]any{"type": "Ready", "status": "True"},
		map[string]any{"type": "Healthy", "status": "False", "message": "cannot reach 10.0.0.5:5432"},
	}, "status", "conditions")
	seedReplication(t, dst, down)
	opts := replicateOpts(t, src, dst)

	var out, errOut bytes.Buffer
	if err := runReplicateStatus(scaleCmd(&out, &errOut, ""), opts, "my-copy", usToEU); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("want a header and two rows, got:\n%s", out.String())
	}
	for _, want := range []string{"upstream", "my-copy-upstream", "us", "prod", "True"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("upstream row missing %q: %s", want, lines[1])
		}
	}
	for _, want := range []string{"downstream", "my-copy-downstream", "eu", "dr", "False", "cannot reach"} {
		if !strings.Contains(lines[2], want) {
			t.Errorf("downstream row missing %q: %s", want, lines[2])
		}
	}
}

func TestReplicateStatusMissingSide(t *testing.T) {
	// After a promote with cleanup the upstream is gone; status still shows the downstream.
	src := newFakeSide("us", "prod", nil)
	dst := newFakeSide("eu", "dr", nil)
	down := newDownstream("dr", "my-copy", "pw", newUpstream("prod", "my-db", "my-copy"))
	setConditions(down, "Ready", "True", "Healthy", "True")
	seedReplication(t, dst, down)
	opts := replicateOpts(t, src, dst)

	var out, errOut bytes.Buffer
	if err := runReplicateStatus(scaleCmd(&out, &errOut, ""), opts, "my-copy", usToEU); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "not found") {
		t.Errorf("missing upstream should say not found:\n%s", out.String())
	}
}

func TestReplicateStatusNothingFound(t *testing.T) {
	opts := replicateOpts(t, newFakeSide("us", "prod", nil), newFakeSide("eu", "dr", nil))
	var out, errOut bytes.Buffer
	err := runReplicateStatus(scaleCmd(&out, &errOut, ""), opts, "nope", usToEU)
	if err == nil || !strings.Contains(err.Error(), "no replication found") || !strings.Contains(err.Error(), `"eu"`) {
		t.Fatalf("err = %v, want it to say where it looked", err)
	}
}

func TestReplicateStatusJSON(t *testing.T) {
	src := newFakeSide("us", "prod", nil)
	dst := newFakeSide("eu", "dr", nil)
	up := newUpstream("prod", "my-db", "my-copy")
	seedReplication(t, src, up)
	opts := replicateOpts(t, src, dst)
	opts.Output = output.OutputJSON

	var out, errOut bytes.Buffer
	if err := runReplicateStatus(scaleCmd(&out, &errOut, ""), opts, "my-copy", usToEU); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if len(got) != 1 {
		t.Errorf("want only the existing upstream, got %d objects", len(got))
	}
}

// linkedSides returns a source with a published upstream and a target with a
// healthy downstream, as after a successful create.
func linkedSides(t *testing.T) (fakeSide, fakeSide) {
	t.Helper()
	src := newFakeSide("us", "prod", nil)
	dst := newFakeSide("eu", "dr", nil)
	up := newUpstream("prod", "my-db", "my-copy")
	publishUpstream(up)
	seedReplication(t, src, up)
	down := newDownstream("dr", "my-copy", "my-copy-replication-pw", up)
	down.SetGeneration(1)
	setConditions(down, "Ready", "True", "Healthy", "True")
	_ = unstructured.SetNestedField(down.Object, int64(1), "status", "observedGeneration")
	seedReplication(t, dst, down)
	return src, dst
}

func TestPromoteDone(t *testing.T) {
	down := newDownstream("dr", "my-copy", "pw", newUpstream("prod", "my-db", "my-copy"))
	setConditions(down, "Ready", "True")
	for _, tc := range []struct {
		og   int64
		done bool
	}{{1, false}, {2, true}, {3, true}} {
		_ = unstructured.SetNestedField(down.Object, tc.og, "status", "observedGeneration")
		if done, _ := promoteDone(2)(down); done != tc.done {
			t.Errorf("observedGeneration %d: done=%v, want %v", tc.og, done, tc.done)
		}
	}
	setConditions(down, "Ready", "False")
	if done, _ := promoteDone(2)(down); done {
		t.Error("caught up but not Ready must not be done")
	}
}

func TestReplicatePromoteKeepsUpstreamAndWarns(t *testing.T) {
	src, dst := linkedSides(t)
	opts := replicateOpts(t, src, dst)
	f := usToEU // --no-wait
	var out, errOut bytes.Buffer
	if err := runReplicatePromote(scaleCmd(&out, &errOut, "y\n"), opts, "my-copy", f); err != nil {
		t.Fatalf("%v\nstderr:\n%s", err, errOut.String())
	}
	for _, want := range []string{"stops copying", "10.0.0.5", "has not arrived yet", "Continue?", "Warning", "adbo replicate delete my-copy"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut.String())
		}
	}
	if got := k8s.StringField(getReplication(t, dst, "dr", "my-copy-downstream"), "spec", "downstream", "control"); got != "promote" {
		t.Errorf("control = %q, want promote", got)
	}
	// Other downstream fields must survive the patch.
	if got := k8s.StringField(getReplication(t, dst, "dr", "my-copy-downstream"), "spec", "downstream", "host"); got != "10.0.0.5" {
		t.Errorf("host = %q after promote", got)
	}
	getReplication(t, src, "prod", "my-copy-upstream") // still there without --cleanup-upstream
	if !strings.Contains(out.String(), `Promoting "my-copy"`) {
		t.Errorf("out = %q", out.String())
	}
}

func TestReplicatePromoteCleanupUpstream(t *testing.T) {
	src, dst := linkedSides(t)
	opts := replicateOpts(t, src, dst)
	opts.Yes = true
	f := usToEU
	f.cleanUpstream = true
	var out, errOut bytes.Buffer
	if err := runReplicatePromote(scaleCmd(&out, &errOut, ""), opts, "my-copy", f); err != nil {
		t.Fatal(err)
	}
	if _, err := src.dyn.Resource(k8s.ReplicationGVR).Namespace("prod").Get(t.Context(), "my-copy-upstream", metav1.GetOptions{}); err == nil {
		t.Error("--cleanup-upstream left the upstream on the source")
	}
	if strings.Contains(errOut.String(), "Warning") {
		t.Errorf("no warning expected after cleanup:\n%s", errOut.String())
	}
}

func TestReplicatePromoteWaits(t *testing.T) {
	src, dst := linkedSides(t)
	opts := replicateOpts(t, src, dst)
	opts.Yes = true
	// The fake does not bump generation on patch, so play both API server and operator.
	playOperator(t, dst, "dr", "my-copy-downstream", func(d *unstructured.Unstructured) {
		for k8s.StringField(d, "spec", "downstream", "control") != "promote" {
			time.Sleep(10 * time.Millisecond)
			got, err := dst.dyn.Resource(k8s.ReplicationGVR).Namespace("dr").Get(context.Background(), "my-copy-downstream", metav1.GetOptions{})
			if err != nil {
				return
			}
			*d = *got
		}
		_ = unstructured.SetNestedField(d.Object, int64(2), "status", "observedGeneration")
	})
	f := usToEU
	f.wait = true
	var out, errOut bytes.Buffer
	if err := runReplicatePromote(scaleCmd(&out, &errOut, ""), opts, "my-copy", f); err != nil {
		t.Fatalf("%v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), `Promoted "my-copy"`) {
		t.Errorf("out = %q", out.String())
	}
}

func TestReplicatePromoteDeclined(t *testing.T) {
	src, dst := linkedSides(t)
	opts := replicateOpts(t, src, dst)
	var out, errOut bytes.Buffer
	err := runReplicatePromote(scaleCmd(&out, &errOut, "n\n"), opts, "my-copy", usToEU)
	if !errors.Is(err, errDeclined) {
		t.Fatalf("want errDeclined, got %v", err)
	}
	if got := k8s.StringField(getReplication(t, dst, "dr", "my-copy-downstream"), "spec", "downstream", "control"); got != "setup" {
		t.Errorf("control = %q after declining, want setup", got)
	}
}

func TestReplicatePromoteAlreadyPromoted(t *testing.T) {
	src, dst := linkedSides(t)
	opts := replicateOpts(t, src, dst)
	down := getReplication(t, dst, "dr", "my-copy-downstream")
	_ = unstructured.SetNestedField(down.Object, "promote", "spec", "downstream", "control")
	if _, err := dst.dyn.Resource(k8s.ReplicationGVR).Namespace("dr").Update(t.Context(), down, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	// Empty stdin: it must not even ask.
	if err := runReplicatePromote(scaleCmd(&out, &errOut, ""), opts, "my-copy", usToEU); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already promoted") {
		t.Errorf("out = %q", out.String())
	}
}

func TestReplicatePromoteNoReplication(t *testing.T) {
	opts := replicateOpts(t, newFakeSide("us", "prod", nil), newFakeSide("eu", "dr", nil))
	var out, errOut bytes.Buffer
	err := runReplicatePromote(scaleCmd(&out, &errOut, ""), opts, "nope", usToEU)
	if err == nil || !strings.Contains(err.Error(), "nope-downstream does not exist") {
		t.Fatalf("err = %v", err)
	}
}
