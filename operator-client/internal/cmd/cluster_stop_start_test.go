package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/spf13/cobra"
)

// poweredDBCluster is a cluster whose spec.primarySpec.isStopped is set.
func poweredDBCluster(name, phase string, stopped bool) *unstructured.Unstructured {
	u := testDBCluster(name, phase)
	if err := unstructured.SetNestedField(u.Object, stopped, isStoppedPath...); err != nil {
		panic(err)
	}
	return u
}

// storedIsStopped reads the flag back off the fake API server.
func storedIsStopped(t *testing.T, opts *GlobalOptions, name string) bool {
	t.Helper()
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	db, err := c.In(k8s.DBClusterGVR).Get(context.TODO(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v, _, _ := unstructured.NestedBool(db.Object, isStoppedPath...)
	return v
}

func TestClusterStopSetsIsStopped(t *testing.T) {
	opts, _, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterReadyPhase, false)})
	out, err := runCRUDCmd(t, newClusterStopCommand(opts), "my-db", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	if !storedIsStopped(t, opts, "my-db") {
		t.Fatal("stop should set spec.primarySpec.isStopped=true")
	}
	if !strings.Contains(out, `Requested stop of cluster "my-db"`) {
		t.Fatalf("output should name the action:\n%s", out)
	}
}

func TestClusterStartClearsIsStopped(t *testing.T) {
	opts, _, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterStoppedPhase, true)})
	if _, err := runCRUDCmd(t, newClusterStartCommand(opts), "my-db", "--no-wait"); err != nil {
		t.Fatal(err)
	}
	if storedIsStopped(t, opts, "my-db") {
		t.Fatal("start should clear spec.primarySpec.isStopped")
	}
}

func TestClusterStopAsksForConfirmation(t *testing.T) {
	opts, _, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterReadyPhase, false)})
	opts.Yes = false // force the prompt

	cmd := newClusterStopCommand(opts)
	cmd.SetIn(strings.NewReader("n\n"))
	out, err := runCRUDCmd(t, cmd, "my-db", "--no-wait")
	// EOF on a script's stdin answers no, so this has to be a non-zero exit
	// or the script reads an aborted shutdown as a completed one.
	if !errors.Is(err, errDeclined) {
		t.Fatalf("answering no should fail the command, got %v", err)
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Fatalf("the error should say the cluster is untouched: %v", err)
	}
	if storedIsStopped(t, opts, "my-db") {
		t.Fatal("a cancelled stop must not touch the cluster")
	}
	// The prompt should say what the user is about to lose, and what they keep.
	for _, want := range []string{"my-db", testNamespace, "connections will be dropped", "data is kept"} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Fatalf("prompt missing %q:\n%s", want, out)
		}
	}
}

func TestClusterStopWaitsOutAnInFlightTransition(t *testing.T) {
	// `stop` straight after `stop --no-wait`: the flag is already set, but the
	// operator has not finished. Reporting "nothing to do" and exiting 0 there
	// claims a shutdown that has not happened yet.
	opts, dyn, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", "DBClusterReconciling", true)})

	// The stored object stays mid-transition, which is what the command Gets
	// before deciding. The waiter Lists, and by then the operator has
	// finished -- so a command that waits completes, and one that returns
	// early never prints the lines below.
	dyn.PrependReactor("list", "dbclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		db := poweredDBCluster("my-db", clusterStoppedPhase, true)
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*db}}
		list.SetAPIVersion(k8s.APIVersion)
		list.SetKind("DBClusterList")
		return true, list, nil
	})

	out, err := runCRUDCmd(t, newClusterStopCommand(opts), "my-db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "has not finished yet") {
		t.Fatalf("a mid-transition stop should keep waiting:\n%s", out)
	}
	if !strings.Contains(out, "is "+clusterStoppedPhase) {
		t.Fatalf("it should report the phase it waited for:\n%s", out)
	}
}

func TestClusterStopNoOpDoesNotWaitWithNoWait(t *testing.T) {
	// --no-wait means do not block, even to confirm a transition in flight.
	opts, _, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", "DBClusterReconciling", true)})
	out, err := runCRUDCmd(t, newClusterStopCommand(opts), "my-db", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "has not finished yet") {
		t.Fatalf("--no-wait should return immediately:\n%s", out)
	}
}

func TestClusterPowerJSONSummary(t *testing.T) {
	opts, _, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterReadyPhase, false)})
	opts.Output = output.OutputJSON

	stdout, stderr, err := runCRUDCmdSplit(t, newClusterStopCommand(opts), "my-db", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("-o json should leave stdout parseable: %v\nstdout: %s", err, stdout)
	}
	if got["action"] != "stop" || got["applied"] != true || got["name"] != "my-db" {
		t.Fatalf("summary = %v", got)
	}
	if !strings.Contains(stderr, "Requested stop") {
		t.Fatalf("commentary should go to stderr:\n%s", stderr)
	}
}

func TestClusterStartDoesNotConfirm(t *testing.T) {
	// Starting is additive, so it should not block on a prompt.
	opts, _, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterStoppedPhase, true)})
	opts.Yes = false

	cmd := newClusterStartCommand(opts)
	cmd.SetIn(strings.NewReader("")) // no answer available
	if _, err := runCRUDCmd(t, cmd, "my-db", "--no-wait"); err != nil {
		t.Fatal(err)
	}
	if storedIsStopped(t, opts, "my-db") {
		t.Fatal("start should proceed without a prompt")
	}
}

func TestClusterPowerNoOpWhenAlreadyInState(t *testing.T) {
	// stop on an already-stopped cluster
	opts, dyn, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterStoppedPhase, true)})
	out, err := runCRUDCmd(t, newClusterStopCommand(opts), "my-db", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "already stopped") {
		t.Fatalf("stop on a stopped cluster should be a no-op:\n%s", out)
	}
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "patch" {
			t.Fatal("a no-op stop must not patch")
		}
	}

	// start on an already-running cluster
	opts2, dyn2, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterReadyPhase, false)})
	out2, err := runCRUDCmd(t, newClusterStartCommand(opts2), "my-db", "--no-wait")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "already running") {
		t.Fatalf("start on a running cluster should be a no-op:\n%s", out2)
	}
	for _, a := range dyn2.Actions() {
		if a.GetVerb() == "patch" {
			t.Fatal("a no-op start must not patch")
		}
	}
}

func TestClusterStopPatchCarriesResourceVersion(t *testing.T) {
	// Guards against overwriting a change made while the user was at the prompt.
	db := poweredDBCluster("my-db", clusterReadyPhase, false)
	// The fake tracker does not assign resourceVersions, so set the one the
	// patch is expected to echo. Asserting the value, not just the field,
	// keeps the test honest about which version was read.
	db.SetResourceVersion("42")
	opts, dyn, _ := crudFakes(t, []runtime.Object{db})
	if _, err := runCRUDCmd(t, newClusterStopCommand(opts), "my-db", "--no-wait"); err != nil {
		t.Fatal(err)
	}
	var sawRV bool
	for _, a := range dyn.Actions() {
		if p, ok := a.(k8stesting.PatchAction); ok && a.GetVerb() == "patch" {
			if strings.Contains(string(p.GetPatch()), `"resourceVersion":"42"`) {
				sawRV = true
			}
		}
	}
	if !sawRV {
		t.Fatal("the patch should carry resourceVersion so a concurrent edit conflicts")
	}
}

func TestClusterStopNotFound(t *testing.T) {
	opts, _, _ := crudFakes(t, nil)
	_, err := runCRUDCmd(t, newClusterStopCommand(opts), "missing-db", "--no-wait")
	if err == nil {
		t.Fatal("stopping a missing cluster should fail")
	}
	for _, want := range []string{"missing-db", testNamespace} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q: %v", want, err)
		}
	}
}

func TestClusterStopWaitsForStoppedPhase(t *testing.T) {
	opts, _, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterStoppedPhase, false)})
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := waitForClusterPhase(context.Background(), waitTestCmd(&buf), opts, c, "my-db", clusterStoppedPhase); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "is "+clusterStoppedPhase) {
		t.Fatalf("stop should wait for %s:\n%s", clusterStoppedPhase, buf.String())
	}
}

func TestClusterPowerWaitTimesOut(t *testing.T) {
	opts, _, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", "DBClusterReconciling", true)})
	opts.Timeout = 50 * time.Millisecond
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = waitForClusterPhase(context.Background(), waitTestCmd(&buf), opts, c, "my-db", clusterStoppedPhase)
	if err == nil {
		t.Fatal("a cluster that never reaches the phase should time out")
	}
	if !strings.Contains(err.Error(), clusterStoppedPhase) {
		t.Fatalf("timeout error should name the phase: %v", err)
	}
}

func TestClusterGroupRegistersStopAndStart(t *testing.T) {
	want := map[string]bool{"stop": false, "start": false}
	for _, c := range newClusterCommand(&GlobalOptions{}).Commands() {
		if _, ok := want[c.Name()]; ok {
			want[c.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("`adbo cluster %s` should be registered", name)
		}
	}
}

func TestClusterPowerDryRunDoesNotWait(t *testing.T) {
	for _, tc := range []struct {
		verb    string
		stopped bool
		build   func(*GlobalOptions) *cobra.Command
	}{
		{"stop", false, newClusterStopCommand},
		{"start", true, newClusterStartCommand},
	} {
		opts, _, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", "DBClusterReady", tc.stopped)})
		// No auto-confirm and no stdin: a dry run that prompted would fail
		// here, which is what should happen to one in a script.
		opts.Yes = false
		out, err := runCRUDCmd(t, tc.build(opts), "my-db", "--dry-run")
		if err != nil {
			t.Fatalf("%s: %v", tc.verb, err)
		}
		if !strings.Contains(out, "Dry run") || !strings.Contains(out, "not applied") {
			t.Fatalf("%s --dry-run should say nothing was applied:\n%s", tc.verb, out)
		}
		if strings.Contains(out, "Requested") {
			t.Fatalf("%s --dry-run should not report a real change:\n%s", tc.verb, out)
		}
	}
}
