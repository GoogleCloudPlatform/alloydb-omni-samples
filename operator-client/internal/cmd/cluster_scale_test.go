package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
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
	if err := runClusterScale(scaleCmd(&out, &errOut, "n\n"), opts, "my-db", scaleFlags{cpu: "8", standbys: -1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Cancelled") {
		t.Errorf("want cancel message, got %q", out.String())
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
		{"negative standbys", scaleFlags{standbys: -2, standbysSet: true}, "0 or more"},
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
