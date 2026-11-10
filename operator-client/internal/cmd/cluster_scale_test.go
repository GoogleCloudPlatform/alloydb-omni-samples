package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"
)

// sizedCluster is a DBCluster with 2 CPU, 16Gi memory, a 10Gi DataDisk
// plus a LogDisk, and no availability block.
func sizedCluster(name string) *unstructured.Unstructured {
	db := dbCluster("default", name, "DBClusterReady")
	_ = unstructured.SetNestedField(db.Object, map[string]any{
		"cpu":    int64(2),
		"memory": "16Gi",
		"disks": []any{
			map[string]any{"name": "DataDisk", "size": "10Gi"},
			map[string]any{"name": "LogDisk", "size": "5Gi"},
		},
	}, "spec", "primarySpec", "resources")
	return db
}

func scaleCmd(out, errOut *bytes.Buffer, stdin string) *cobra.Command {
	cmd := testCommand(out)
	cmd.SetErr(errOut)
	cmd.SetIn(strings.NewReader(stdin))
	return cmd
}

func getCluster(t *testing.T, opts *GlobalOptions, name string) *unstructured.Unstructured {
	t.Helper()
	c, err := opts.Clients()
	if err != nil {
		t.Fatal(err)
	}
	db, err := c.In(k8s.DBClusterGVR).Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestClusterScaleAppliesChanges(t *testing.T) {
	opts := fakeOpts(output.OutputTable, sizedCluster("my-db"))
	var out, errOut bytes.Buffer
	f := scaleFlags{cpu: "4", memory: "32Gi", disk: "50Gi", standbys: 1, standbysSet: true}
	if err := runClusterScale(scaleCmd(&out, &errOut, "y\n"), opts, "my-db", f); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"cpu       2 -> 4", "memory    16Gi -> 32Gi", "disk      10Gi -> 50Gi", "standbys  0 -> 1", "restarts the database"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("prompt missing %q:\n%s", want, errOut.String())
		}
	}

	db := getCluster(t, opts, "my-db")
	if cpu, _, _ := unstructured.NestedInt64(db.Object, "spec", "primarySpec", "resources", "cpu"); cpu != 4 {
		t.Errorf("cpu = %d, want 4", cpu)
	}
	if mem := k8s.StringField(db, "spec", "primarySpec", "resources", "memory"); mem != "32Gi" {
		t.Errorf("memory = %s, want 32Gi", mem)
	}
	disks, _, _ := unstructured.NestedSlice(db.Object, "spec", "primarySpec", "resources", "disks")
	if len(disks) != 2 || disks[0].(map[string]any)["size"] != "50Gi" || disks[1].(map[string]any)["size"] != "5Gi" {
		t.Errorf("disks = %v, want DataDisk 50Gi and LogDisk untouched", disks)
	}
	if n, _, _ := unstructured.NestedInt64(db.Object, "spec", "availability", "numberOfStandbys"); n != 1 {
		t.Errorf("numberOfStandbys = %d, want 1", n)
	}
	// Fields scale does not own must survive the patch.
	if v := k8s.StringField(db, "spec", "databaseVersion"); v != "16.8.0" {
		t.Errorf("databaseVersion = %s, want 16.8.0", v)
	}
}

func TestClusterScaleDeclinedChangesNothing(t *testing.T) {
	opts := fakeOpts(output.OutputTable, sizedCluster("my-db"))
	var out, errOut bytes.Buffer
	err := runClusterScale(scaleCmd(&out, &errOut, "n\n"), opts, "my-db", scaleFlags{cpu: "8", standbys: -1})
	if !errors.Is(err, errDeclined) {
		t.Fatalf("declining must fail so scripts notice; got %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("nothing should go to stdout when declined, got %q", out.String())
	}
	db := getCluster(t, opts, "my-db")
	if cpu, _, _ := unstructured.NestedInt64(db.Object, "spec", "primarySpec", "resources", "cpu"); cpu != 2 {
		t.Errorf("cpu changed to %d after declining", cpu)
	}
}

func TestClusterScaleRejects(t *testing.T) {
	tests := []struct {
		name string
		f    scaleFlags
		want string
	}{
		{"no flags", scaleFlags{standbys: -1}, "nothing to change"},
		{"shrink disk", scaleFlags{disk: "5Gi", standbys: -1}, "can grow but not shrink"},
		{"bad cpu", scaleFlags{cpu: "lots", standbys: -1}, "not a valid positive size"},
		{"zero memory", scaleFlags{memory: "0", standbys: -1}, "not a valid positive size"},
		{"unitless memory", scaleFlags{memory: "16", standbys: -1}, "16 bytes; add a unit"},
		{"tiny memory", scaleFlags{memory: "512Mi", standbys: -1}, "at least 1Gi"},
		{"unitless disk", scaleFlags{disk: "100", standbys: -1}, "100 bytes; add a unit"},
		{"byte unit on cpu", scaleFlags{cpu: "4Gi", standbys: -1}, "number of cores"},
		{"negative standbys", scaleFlags{standbys: -2, standbysSet: true}, "between 0 and 5"},
		{"too many standbys", scaleFlags{standbys: 6, standbysSet: true}, "between 0 and 5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := fakeOpts(output.OutputTable, sizedCluster("my-db"))
			opts.Yes = true
			var out, errOut bytes.Buffer
			err := runClusterScale(scaleCmd(&out, &errOut, ""), opts, "my-db", tc.f)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestClusterScaleNoOp(t *testing.T) {
	opts := fakeOpts(output.OutputTable, sizedCluster("my-db"))
	var out, errOut bytes.Buffer
	// "2000m" equals the current 2 cores and "16384Mi" equals 16Gi.
	f := scaleFlags{cpu: "2000m", memory: "16384Mi", disk: "10Gi", standbys: -1}
	if err := runClusterScale(scaleCmd(&out, &errOut, ""), opts, "my-db", f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("want no-op message, got %q", out.String())
	}
}

func TestClusterScaleUnknownCluster(t *testing.T) {
	opts := fakeOpts(output.OutputTable)
	var out, errOut bytes.Buffer
	err := runClusterScale(scaleCmd(&out, &errOut, ""), opts, "typo", scaleFlags{cpu: "4", standbys: -1})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("want not-found error, got %v", err)
	}
}

func TestClusterScaleStandbysZeroWhenUnsetIsNoOp(t *testing.T) {
	opts := fakeOpts(output.OutputTable, sizedCluster("my-db")) // no availability block
	var out, errOut bytes.Buffer
	if err := runClusterScale(scaleCmd(&out, &errOut, ""), opts, "my-db", scaleFlags{standbys: 0, standbysSet: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to do") || errOut.Len() != 0 {
		t.Errorf("want silent no-op, got out=%q prompt=%q", out.String(), errOut.String())
	}
}

func TestClusterScaleRestartWarningOnlyWhenCPUOrMemoryChanges(t *testing.T) {
	opts := fakeOpts(output.OutputTable, sizedCluster("my-db"))
	var out, errOut bytes.Buffer
	// cpu is already 2, so only the disk changes.
	if err := runClusterScale(scaleCmd(&out, &errOut, "y\n"), opts, "my-db", scaleFlags{cpu: "2", disk: "20Gi", standbys: -1}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut.String(), "restarts") || strings.Contains(errOut.String(), "cpu") {
		t.Errorf("disk-only change should not warn about a restart:\n%s", errOut.String())
	}
}

func TestClusterScaleWarnsOfOutageWithoutStandbys(t *testing.T) {
	opts := fakeOpts(output.OutputTable, sizedCluster("my-db")) // no standbys
	var out, errOut bytes.Buffer
	if err := runClusterScale(scaleCmd(&out, &errOut, "y\n"), opts, "my-db", scaleFlags{memory: "32Gi", standbys: -1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "no standbys, so it is unavailable until the restart finishes") {
		t.Errorf("want an outage warning for a cluster without standbys:\n%s", errOut.String())
	}
}

func TestClusterScaleNoOutageWarningWithStandbysOrWithoutRestart(t *testing.T) {
	withStandby := sizedCluster("my-db")
	_ = unstructured.SetNestedField(withStandby.Object, int64(1), "spec", "availability", "numberOfStandbys")
	tests := []struct {
		name string
		db   *unstructured.Unstructured
		f    scaleFlags
	}{
		{"has a standby", withStandby, scaleFlags{memory: "32Gi", standbys: -1}},
		{"disk only", sizedCluster("my-db"), scaleFlags{disk: "20Gi", standbys: -1}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := fakeOpts(output.OutputTable, tc.db)
			var out, errOut bytes.Buffer
			if err := runClusterScale(scaleCmd(&out, &errOut, "y\n"), opts, "my-db", tc.f); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(errOut.String(), "unavailable") {
				t.Errorf("unexpected outage warning:\n%s", errOut.String())
			}
		})
	}
}

func TestClusterScaleRefusesUnreadableDiskSize(t *testing.T) {
	db := sizedCluster("my-db")
	_ = unstructured.SetNestedSlice(db.Object, []any{map[string]any{"name": "DataDisk"}},
		"spec", "primarySpec", "resources", "disks")
	opts := fakeOpts(output.OutputTable, db)
	opts.Yes = true
	var out, errOut bytes.Buffer
	err := runClusterScale(scaleCmd(&out, &errOut, ""), opts, "my-db", scaleFlags{disk: "1Gi", standbys: -1})
	if err == nil || !strings.Contains(err.Error(), "could not be read") {
		t.Fatalf("want refusal, got %v", err)
	}
}

func TestPlanScalePatchHasOnlyChangedFieldsAndResourceVersion(t *testing.T) {
	db := sizedCluster("my-db")
	db.SetResourceVersion("42")
	_, patch, err := planScale(db, scaleFlags{cpu: "4", standbys: -1})
	if err != nil {
		t.Fatal(err)
	}
	if rv, _, _ := unstructured.NestedString(patch, "metadata", "resourceVersion"); rv != "42" {
		t.Errorf("patch resourceVersion = %q, want 42 so concurrent edits conflict", rv)
	}
	res, _, _ := unstructured.NestedMap(patch, "spec", "primarySpec", "resources")
	if len(res) != 1 || res["cpu"] != int64(4) {
		t.Errorf("patch resources = %v, want only cpu=4", res)
	}
	spec, _, _ := unstructured.NestedMap(patch, "spec")
	if _, ok := spec["databaseVersion"]; ok {
		t.Error("patch must not resend fields scale does not change")
	}
}

func TestPlanScaleStandbysSetsRequiredReadReplicaField(t *testing.T) {
	// No spec.availability: the CRD requires enableStandbyAsReadReplica
	// once that block exists, so the patch must include it.
	_, patch, err := planScale(sizedCluster("my-db"), scaleFlags{standbys: 1, standbysSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if v, found, _ := unstructured.NestedBool(patch, "spec", "availability", "enableStandbyAsReadReplica"); !found || v {
		t.Errorf("patch availability = %v, want enableStandbyAsReadReplica=false", patch["spec"])
	}

	// When the cluster already sets it, leave the user's choice alone.
	db := sizedCluster("my-db")
	_ = unstructured.SetNestedField(db.Object, true, "spec", "availability", "enableStandbyAsReadReplica")
	_, patch, err = planScale(db, scaleFlags{standbys: 2, standbysSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(patch, "spec", "availability", "enableStandbyAsReadReplica"); found {
		t.Error("patch must not overwrite an existing enableStandbyAsReadReplica")
	}
}

func TestClusterScaleJSONPrintsUpdatedCluster(t *testing.T) {
	opts := fakeOpts(output.OutputJSON, sizedCluster("my-db"))
	opts.Yes = true
	var out, errOut bytes.Buffer
	if err := runClusterScale(scaleCmd(&out, &errOut, ""), opts, "my-db", scaleFlags{cpu: "4", standbys: -1}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("scale -o json is not JSON: %v\n%s", err, out.String())
	}
	if cpu, _, _ := unstructured.NestedFieldNoCopy(got, "spec", "primarySpec", "resources", "cpu"); cpu != float64(4) {
		t.Errorf("printed cpu = %v, want 4", cpu)
	}
}

func TestClusterScaleConflictIsReported(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{k8s.DBClusterGVR: "DBClusterList"}, sizedCluster("my-db"))
	dyn.PrependReactor("patch", "dbclusters", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewConflict(k8s.DBClusterGVR.GroupResource(), "my-db", errors.New("modified"))
	})
	opts := &GlobalOptions{Output: output.OutputTable, Yes: true,
		NewClients: func() (*k8s.Clients, error) {
			return &k8s.Clients{Dynamic: dyn, Namespace: "default", Context: "test-ctx"}, nil
		}}
	var out, errOut bytes.Buffer
	err := runClusterScale(scaleCmd(&out, &errOut, ""), opts, "my-db", scaleFlags{cpu: "4", standbys: -1})
	if err == nil || !strings.Contains(err.Error(), "changed while we were updating") {
		t.Fatalf("want conflict error, got %v", err)
	}
}

// readyStatus marks db Ready at observedGeneration og, with allocated
// resources matching sizedCluster's spec unless overridden.
func readyStatus(db *unstructured.Unstructured, og int64, allocated map[string]any) {
	_ = unstructured.SetNestedField(db.Object, clusterReadyPhase, "status", "phase")
	_ = unstructured.SetNestedField(db.Object, og, "status", "observedGeneration")
	if allocated != nil {
		_ = unstructured.SetNestedField(db.Object, allocated, "status", "primary", "allocatedResources")
	}
}

func TestScaleDone(t *testing.T) {
	_, patch, err := planScale(sizedCluster("my-db"), scaleFlags{cpu: "4", disk: "50Gi", standbys: 1, standbysSet: true})
	if err != nil {
		t.Fatal(err)
	}
	applied := map[string]any{"cpu": int64(4), "memory": "16Gi",
		"disks": []any{map[string]any{"name": "DataDisk", "size": "50Gi"}}}
	stale := map[string]any{"cpu": int64(2), "memory": "16Gi",
		"disks": []any{map[string]any{"name": "DataDisk", "size": "10Gi"}}}

	tests := []struct {
		name    string
		mutate  func(db *unstructured.Unstructured)
		done    bool
		failure bool
	}{
		{"all applied", func(db *unstructured.Unstructured) { readyStatus(db, 2, applied) }, true, false},
		{"old generation", func(db *unstructured.Unstructured) { readyStatus(db, 1, applied) }, false, false},
		{"not ready", func(db *unstructured.Unstructured) {
			readyStatus(db, 2, applied)
			_ = unstructured.SetNestedField(db.Object, "DBClusterReconciling", "status", "phase")
		}, false, false},
		{"resources not yet allocated", func(db *unstructured.Unstructured) { readyStatus(db, 2, stale) }, false, false},
		{"operator omits allocatedResources", func(db *unstructured.Unstructured) { readyStatus(db, 2, nil) }, true, false},
		{"HA not ready", func(db *unstructured.Unstructured) {
			readyStatus(db, 2, applied)
			_ = unstructured.SetNestedSlice(db.Object, []any{map[string]any{"type": "HAReady", "status": "False"}}, "status", "conditions")
		}, false, false},
		{"failed phase", func(db *unstructured.Unstructured) {
			readyStatus(db, 2, applied)
			_ = unstructured.SetNestedField(db.Object, "DBClusterReconcileFailed", "status", "phase")
		}, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := sizedCluster("my-db") // dbCluster sets HAReady=True
			tc.mutate(db)
			done, err := scaleDone(2, patch)(db)
			var failed *wait.FailedError
			if done != tc.done || errors.As(err, &failed) != tc.failure {
				t.Fatalf("done=%v err=%v, want done=%v failure=%v", done, err, tc.done, tc.failure)
			}
		})
	}
}

// waitOpts is fakeOpts with the dynamic client exposed so a test can play
// the operator.
func waitOpts(db *unstructured.Unstructured) (*GlobalOptions, *dynamicfake.FakeDynamicClient) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{k8s.DBClusterGVR: "DBClusterList"}, db)
	return &GlobalOptions{Output: output.OutputTable, Yes: true, Timeout: 5 * time.Second,
		NewClients: func() (*k8s.Clients, error) {
			return &k8s.Clients{Dynamic: dyn, Namespace: "default", Context: "test-ctx"}, nil
		}}, dyn
}

func TestClusterScaleWaitsForOperator(t *testing.T) {
	db := sizedCluster("my-db")
	db.SetGeneration(2) // the fake does not bump generation on patch
	readyStatus(db, 1, nil)
	opts, dyn := waitOpts(db)

	// Play the operator: once the patch lands, report generation 2 Ready.
	go func() {
		for {
			cur, err := dyn.Resource(k8s.DBClusterGVR).Namespace("default").Get(t.Context(), "my-db", metav1.GetOptions{})
			if err != nil {
				return
			}
			if cpu, _, _ := unstructured.NestedInt64(cur.Object, "spec", "primarySpec", "resources", "cpu"); cpu == 4 {
				_ = unstructured.SetNestedField(cur.Object, "DBClusterReconciling", "status", "phase")
				_, _ = dyn.Resource(k8s.DBClusterGVR).Namespace("default").Update(t.Context(), cur, metav1.UpdateOptions{})
				readyStatus(cur, 2, map[string]any{"cpu": int64(4)})
				_, _ = dyn.Resource(k8s.DBClusterGVR).Namespace("default").Update(t.Context(), cur, metav1.UpdateOptions{})
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	var out, errOut bytes.Buffer
	if err := runClusterScale(scaleCmd(&out, &errOut, ""), opts, "my-db", scaleFlags{cpu: "4", standbys: -1, wait: true}); err != nil {
		t.Fatalf("%v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), `Scaled cluster "my-db"`) {
		t.Errorf("want completion message, got %q", out.String())
	}
	// Which intermediate phases show depends on timing; the final one always does.
	if !strings.Contains(errOut.String(), "Waiting for cluster") || !strings.Contains(errOut.String(), "my-db: DBClusterReady") {
		t.Errorf("want wait progress on stderr, got:\n%s", errOut.String())
	}
}

func TestClusterScaleWaitTimesOut(t *testing.T) {
	db := sizedCluster("my-db")
	db.SetGeneration(2)
	readyStatus(db, 1, nil) // the operator never catches up
	opts, _ := waitOpts(db)
	opts.Timeout = 300 * time.Millisecond

	var out, errOut bytes.Buffer
	err := runClusterScale(scaleCmd(&out, &errOut, ""), opts, "my-db", scaleFlags{cpu: "4", standbys: -1, wait: true})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "the change is applied") {
		t.Fatalf("want a timeout that says the change is applied, got %v", err)
	}
}

func TestClusterScaleNoWaitFlag(t *testing.T) {
	opts := fakeOpts(output.OutputTable, sizedCluster("my-db"))
	opts.Yes = true
	cmd := newClusterScaleCommand(opts)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"my-db", "--cpu", "4", "--no-wait"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut.String(), "Waiting") || !strings.Contains(out.String(), "Check progress") {
		t.Errorf("--no-wait should return at once; out=%q err=%q", out.String(), errOut.String())
	}
	cmd = newClusterScaleCommand(opts)
	cmd.SetArgs([]string{"my-db", "--cpu", "4", "--wait", "--no-wait"})
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	if err := cmd.ExecuteContext(t.Context()); err == nil {
		t.Error("--wait and --no-wait together should fail")
	}
}
