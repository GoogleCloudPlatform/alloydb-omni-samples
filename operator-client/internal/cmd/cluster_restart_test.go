package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// phaseFollowsIsStopped makes the fake behave like the operator for the one
// thing restart waits on: the cluster reports DBClusterStopped while
// spec.primarySpec.isStopped is set, and DBClusterReady once it is cleared.
// Only List needs it -- that is what internal/wait reads before watching, so
// a phase that is already right when the wait starts is not missed.
//
// The reactor runs on the informer's goroutine, so it reports trouble as an
// error rather than calling t.Fatal.
func phaseFollowsIsStopped(dyn *dynamicfake.FakeDynamicClient, name string) {
	dyn.PrependReactor("list", "dbclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		obj, err := dyn.Tracker().Get(k8s.DBClusterGVR, testNamespace, name)
		if err != nil {
			return true, nil, err
		}
		db, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return true, nil, fmt.Errorf("tracker holds %T, want *unstructured.Unstructured", obj)
		}
		db = db.DeepCopy()
		phase := clusterReadyPhase
		if stopped, _, _ := unstructured.NestedBool(db.Object, isStoppedPath...); stopped {
			phase = clusterStoppedPhase
		}
		if err := unstructured.SetNestedField(db.Object, phase, "status", "phase"); err != nil {
			return true, nil, err
		}
		list := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*db}}
		list.SetAPIVersion(k8s.APIVersion)
		list.SetKind("DBClusterList")
		return true, list, nil
	})
}

// patchBodies is every patch the command sent, in order.
func patchBodies(dyn *dynamicfake.FakeDynamicClient) []string {
	var bodies []string
	for _, a := range dyn.Actions() {
		if p, ok := a.(k8stesting.PatchAction); ok && a.GetVerb() == "patch" {
			bodies = append(bodies, string(p.GetPatch()))
		}
	}
	return bodies
}

// haDBCluster is a running cluster with standbys and an HAReady condition.
func haDBCluster(name string, standbys int64, haReady bool, reason string) *unstructured.Unstructured {
	u := poweredDBCluster(name, clusterReadyPhase, false)
	status := "False"
	if haReady {
		status = "True"
	}
	if err := unstructured.SetNestedField(u.Object, standbys, "spec", "availability", "numberOfStandbys"); err != nil {
		panic(err)
	}
	conds := []any{map[string]any{
		"type":   haReadyConditionType,
		"status": status,
		"reason": reason,
	}}
	if err := unstructured.SetNestedSlice(u.Object, conds, "status", "conditions"); err != nil {
		panic(err)
	}
	return u
}

// shortHAPolling keeps the HA wait from sleeping between reads.
func shortHAPolling(t *testing.T) {
	t.Helper()
	prev := haReadyPollInterval
	haReadyPollInterval = time.Millisecond
	t.Cleanup(func() { haReadyPollInterval = prev })
}

func TestClusterRestartStopsThenStarts(t *testing.T) {
	opts, dyn, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterReadyPhase, false)})
	phaseFollowsIsStopped(dyn, "my-db")

	out, err := runCRUDCmd(t, newClusterRestartCommand(opts), "my-db")
	if err != nil {
		t.Fatal(err)
	}
	if storedIsStopped(t, opts, "my-db") {
		t.Fatal("restart must leave the cluster running, not stopped")
	}

	bodies := patchBodies(dyn)
	if len(bodies) != 2 {
		t.Fatalf("want a stop patch then a start patch, got %d: %v", len(bodies), bodies)
	}
	if !strings.Contains(bodies[0], `"isStopped":true`) {
		t.Fatalf("first patch should stop the cluster: %s", bodies[0])
	}
	if !strings.Contains(bodies[1], `"isStopped":false`) {
		t.Fatalf("second patch should start it again: %s", bodies[1])
	}

	// The start must not be requested before the operator reports the stop
	// finished, or the operator sees both flips at once and nothing restarts.
	stopping, starting := strings.Index(out, "Stopping cluster"), strings.Index(out, "Starting cluster")
	if stopping < 0 || starting < 0 || stopping > starting {
		t.Fatalf("restart should report the stop, then the start:\n%s", out)
	}
	if !strings.Contains(out, "is "+clusterStoppedPhase) {
		t.Fatalf("restart should wait for %s before starting:\n%s", clusterStoppedPhase, out)
	}
	if !strings.Contains(out, "adbo cluster start my-db") {
		t.Fatalf("restart should say how to recover an interrupted run:\n%s", out)
	}
	if !strings.Contains(out, `Restarted cluster "my-db"`) {
		t.Fatalf("restart should confirm it finished:\n%s", out)
	}
	// No standbys, so there is no HA to wait for.
	if strings.Contains(out, "standbys to catch up") {
		t.Fatalf("a cluster with no standbys should not wait for HA:\n%s", out)
	}
}

func TestClusterRestartRejectsNoWait(t *testing.T) {
	opts, dyn, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterReadyPhase, false)})
	_, err := runCRUDCmd(t, newClusterRestartCommand(opts), "my-db", "--no-wait")
	if err == nil {
		t.Fatal("restart cannot return early; --no-wait should be refused")
	}
	// The refusal has to explain itself and point at the verb that does
	// support --no-wait, or it is no better than "unknown flag".
	for _, want := range []string{"--no-wait", "adbo cluster stop"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal missing %q: %v", want, err)
		}
	}
	if len(patchBodies(dyn)) != 0 {
		t.Fatal("a refused restart must not touch the cluster")
	}
}

func TestClusterRestartAsksBeforeTakingTheClusterDown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		standbys int64
		want     []string
	}{
		{"no standbys", 0, []string{"my-db", testNamespace, "down until it is ready", "data is kept"}},
		// An HA cluster has somewhere to move the primary, so the prompt
		// should offer the cheaper option before the user says yes.
		{"with standbys", 2, []string{"2 standbys", "switchover", "less downtime"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := poweredDBCluster("my-db", clusterReadyPhase, false)
			if tc.standbys > 0 {
				if err := unstructured.SetNestedField(db.Object, tc.standbys, "spec", "availability", "numberOfStandbys"); err != nil {
					t.Fatal(err)
				}
			}
			opts, dyn, _ := crudFakes(t, []runtime.Object{db})
			opts.Yes = false // force the prompt

			cmd := newClusterRestartCommand(opts)
			cmd.SetIn(strings.NewReader("n\n"))
			out, err := runCRUDCmd(t, cmd, "my-db")
			if !errors.Is(err, errDeclined) {
				t.Fatalf("answering no should fail the command, got %v", err)
			}
			if len(patchBodies(dyn)) != 0 {
				t.Fatal("a cancelled restart must not touch the cluster")
			}
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Fatalf("prompt missing %q:\n%s", want, out)
				}
			}
			// `adbo switchover` does not exist yet; naming it would send the
			// user to "unknown command".
			if strings.Contains(out, "adbo switchover") {
				t.Fatalf("the prompt should not name an unlanded command:\n%s", out)
			}
		})
	}
}

func TestClusterRestartOnAStoppedClusterOnlyStarts(t *testing.T) {
	// Finishing a restart that died after the stop is the main reason to run
	// this on a stopped cluster, and the downtime is already underway: no
	// prompt, no second stop. opts.Yes is off and stdin is empty, so a prompt
	// here would read EOF and cancel.
	opts, dyn, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterStoppedPhase, true)})
	opts.Yes = false
	phaseFollowsIsStopped(dyn, "my-db")

	cmd := newClusterRestartCommand(opts)
	cmd.SetIn(strings.NewReader(""))
	out, err := runCRUDCmd(t, cmd, "my-db")
	if err != nil {
		t.Fatal(err)
	}
	if storedIsStopped(t, opts, "my-db") {
		t.Fatal("restart should have started the stopped cluster")
	}
	bodies := patchBodies(dyn)
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"isStopped":false`) {
		t.Fatalf("want exactly one start patch, got %v", bodies)
	}
	if !strings.Contains(out, "already stopped") {
		t.Fatalf("restart should say it skipped the stop:\n%s", out)
	}
}

func TestClusterRestartSaysHowToRecoverWhenTheStartFails(t *testing.T) {
	opts, dyn, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterReadyPhase, false)})
	phaseFollowsIsStopped(dyn, "my-db")

	// The stop lands; the start does not. That leaves the cluster down, which
	// is the one outcome the error message has to explain.
	patches := 0
	dyn.PrependReactor("patch", "dbclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		patches++
		if patches > 1 {
			return true, nil, errBoom
		}
		return false, nil, nil
	})

	_, err := runCRUDCmd(t, newClusterRestartCommand(opts), "my-db")
	if err == nil {
		t.Fatal("a failed start should fail the command")
	}
	if !strings.Contains(err.Error(), "adbo cluster start my-db") {
		t.Fatalf("error should say how to bring the cluster back: %v", err)
	}
	if !strings.Contains(err.Error(), "still stopped") {
		t.Fatalf("error should say the cluster is still down: %v", err)
	}
}

func TestClusterRestartPatchesCarryResourceVersionUnlessForced(t *testing.T) {
	// The fake tracker does not assign resourceVersions, so set the one both
	// patches are expected to echo.
	db := poweredDBCluster("my-db", clusterReadyPhase, false)
	db.SetResourceVersion("42")
	opts, dyn, _ := crudFakes(t, []runtime.Object{db})
	phaseFollowsIsStopped(dyn, "my-db")
	if _, err := runCRUDCmd(t, newClusterRestartCommand(opts), "my-db"); err != nil {
		t.Fatal(err)
	}
	for i, body := range patchBodies(dyn) {
		if !strings.Contains(body, `"resourceVersion":"42"`) {
			t.Fatalf("patch %d should carry the resourceVersion it read, so a concurrent edit conflicts: %s", i, body)
		}
	}

	// --force-conflicts is the opt-out: no precondition, so the restart lands
	// on a cluster someone else has edited.
	forced := poweredDBCluster("my-db", clusterReadyPhase, false)
	forced.SetResourceVersion("42")
	opts2, dyn2, _ := crudFakes(t, []runtime.Object{forced})
	phaseFollowsIsStopped(dyn2, "my-db")
	if _, err := runCRUDCmd(t, newClusterRestartCommand(opts2), "my-db", "--force-conflicts"); err != nil {
		t.Fatal(err)
	}
	bodies := patchBodies(dyn2)
	if len(bodies) == 0 {
		t.Fatal("--force-conflicts should still patch")
	}
	for i, body := range bodies {
		if strings.Contains(body, "resourceVersion") {
			t.Fatalf("--force-conflicts patch %d should send no precondition: %s", i, body)
		}
	}
}

func TestClusterRestartConflictPointsAtForceConflicts(t *testing.T) {
	opts, dyn, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterReadyPhase, false)})
	dyn.PrependReactor("patch", "dbclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(k8s.DBClusterGVR.GroupResource(), "my-db", errBoom)
	})

	_, err := runCRUDCmd(t, newClusterRestartCommand(opts), "my-db")
	if err == nil {
		t.Fatal("a conflicting patch should fail the restart")
	}
	if !strings.Contains(err.Error(), "--force-conflicts") {
		t.Fatalf("a conflict should mention the flag that overrides it: %v", err)
	}
}

func TestClusterRestartWaitsForHAReady(t *testing.T) {
	opts, _, _ := crudFakes(t, []runtime.Object{haDBCluster("my-db", 1, true, "")})
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := waitForHAReady(context.Background(), waitTestCmd(&buf), opts, c, "my-db"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "HA-ready again") {
		t.Fatalf("the HA wait should confirm the standbys caught up:\n%s", buf.String())
	}
}

func TestClusterRestartHAWaitTimeoutSaysTheClusterIsServing(t *testing.T) {
	shortHAPolling(t)
	opts, _, _ := crudFakes(t, []runtime.Object{haDBCluster("my-db", 1, false, "StandbyCatchingUp")})
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	var buf bytes.Buffer
	err = waitForHAReady(ctx, waitTestCmd(&buf), opts, c, "my-db")
	if err == nil {
		t.Fatal("standbys that never catch up should time out")
	}
	// The database is up: the error must not read like a failed restart, and
	// it should carry the operator's own reason.
	for _, want := range []string{"serving", "StandbyCatchingUp", "adbo status my-db"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("HA timeout missing %q: %v", want, err)
		}
	}
}

func TestClusterRestartHAWaitInterruptIsNotATimeout(t *testing.T) {
	shortHAPolling(t)
	opts, _, _ := crudFakes(t, []runtime.Object{haDBCluster("my-db", 1, false, "StandbyCatchingUp")})
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	var buf bytes.Buffer
	err = waitForHAReady(ctx, waitTestCmd(&buf), opts, c, "my-db")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a Ctrl+C should surface as a cancellation, got %v", err)
	}
	if strings.Contains(fmt.Sprint(err), "--timeout") {
		t.Fatalf("a cancelled wait must not be reported as a timeout: %v", err)
	}
}

func TestClusterRestartAcceptsWaitButRefusesTheOpposite(t *testing.T) {
	// Every sibling verb takes --wait, so it gets typed here too. It is what
	// restart already does, so it should work rather than read as a typo;
	// asking for the opposite, either way round, cannot be honoured.
	opts, dyn, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterReadyPhase, false)})
	phaseFollowsIsStopped(dyn, "my-db")
	if _, err := runCRUDCmd(t, newClusterRestartCommand(opts), "my-db", "--wait"); err != nil {
		t.Fatalf("--wait is what restart does anyway: %v", err)
	}

	for _, args := range [][]string{{"my-db", "--no-wait"}, {"my-db", "--wait=false"}} {
		o, d, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterReadyPhase, false)})
		_, err := runCRUDCmd(t, newClusterRestartCommand(o), args...)
		if err == nil {
			t.Fatalf("%v should be refused", args)
		}
		if !strings.Contains(err.Error(), "adbo cluster stop") {
			t.Fatalf("the refusal should point at the verb that can return early: %v", err)
		}
		if len(patchBodies(d)) != 0 {
			t.Fatalf("%v must not touch the cluster", args)
		}
	}
}

func TestClusterRestartHAWaitFailsFastOnAStuckStandby(t *testing.T) {
	// HA that never comes back is usually a standby that cannot start. Without
	// this the command sat here until --timeout with nothing to show.
	shortHAPolling(t)
	stuck := haDBCluster("my-db", 1, false, "StandbyUnhealthy")
	if err := unstructured.SetNestedSlice(stuck.Object, []any{
		map[string]any{"code": "DBSE0013", "message": "Database instance is unhealthy"},
	}, "status", "criticalIncidents"); err != nil {
		t.Fatal(err)
	}
	opts, _, _ := crudFakes(t, []runtime.Object{stuck})
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var buf bytes.Buffer
	start := time.Now()
	err = waitForHAReady(ctx, waitTestCmd(&buf), opts, c, "my-db")
	if err == nil {
		t.Fatal("a fatal incident should end the HA wait")
	}
	if !strings.Contains(err.Error(), "DBSE0013") {
		t.Fatalf("error should carry the incident: %v", err)
	}
	// The database is up, so the message must not read as a failed restart.
	if !strings.Contains(err.Error(), "is serving") {
		t.Fatalf("error should say the cluster is serving: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("expected to fail fast, took %s", elapsed)
	}
}

func TestClusterRestartHAWaitRidesOutATransientReadFailure(t *testing.T) {
	// The cluster is already serving by now: one failed read should not turn a
	// finished restart into a failed command.
	shortHAPolling(t)
	opts, dyn, _ := crudFakes(t, []runtime.Object{haDBCluster("my-db", 1, true, "Ready")})
	gets := 0
	dyn.PrependReactor("get", "dbclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets == 1 {
			return true, nil, errBoom
		}
		return false, nil, nil
	})

	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := waitForHAReady(context.Background(), waitTestCmd(&buf), opts, c, "my-db"); err != nil {
		t.Fatalf("a transient read failure should be retried, got %v", err)
	}
	if !strings.Contains(buf.String(), "retrying") {
		t.Fatalf("the retry should be visible to the user:\n%s", buf.String())
	}
}

func TestClusterRestartHAWaitStopsIfTheClusterIsDeleted(t *testing.T) {
	// Retrying is right for a blip, but not for a cluster that is gone.
	shortHAPolling(t)
	opts, dyn, _ := crudFakes(t, []runtime.Object{haDBCluster("my-db", 1, false, "SetupInProgress")})
	dyn.PrependReactor("get", "dbclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewNotFound(k8s.DBClusterGVR.GroupResource(), "my-db")
	})

	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	err = waitForHAReady(context.Background(), waitTestCmd(&buf), opts, c, "my-db")
	if err == nil {
		t.Fatal("a deleted cluster should end the wait")
	}
	if strings.Contains(buf.String(), "retrying") {
		t.Fatalf("a deleted cluster is not a blip to retry:\n%s", buf.String())
	}
}

func TestClusterRestartJSONSummary(t *testing.T) {
	opts, dyn, _ := crudFakes(t, []runtime.Object{poweredDBCluster("my-db", clusterReadyPhase, false)})
	phaseFollowsIsStopped(dyn, "my-db")
	opts.Output = output.OutputJSON

	stdout, stderr, err := runCRUDCmdSplit(t, newClusterRestartCommand(opts), "my-db")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("-o json should leave stdout parseable: %v\nstdout: %s", err, stdout)
	}
	if got["name"] != "my-db" || got["phase"] != clusterReadyPhase || got["haReady"] != false {
		t.Fatalf("summary = %v", got)
	}
	if !strings.Contains(stderr, "Restarted cluster") {
		t.Fatalf("commentary should go to stderr:\n%s", stderr)
	}
}

func TestClusterGroupRegistersRestart(t *testing.T) {
	var found bool
	for _, c := range newClusterCommand(&GlobalOptions{}).Commands() {
		if c.Name() == "restart" {
			found = true
		}
	}
	if !found {
		t.Fatal("`adbo cluster restart` should be registered")
	}
}
