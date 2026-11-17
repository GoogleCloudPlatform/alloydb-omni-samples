package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// versionedCluster is a ready DBCluster running database 16.8.0 and
// agents 1.5.0, with status matching spec (no change in progress).
func versionedCluster(name string) *unstructured.Unstructured {
	db := dbCluster("default", name, "DBClusterReady")
	_ = unstructured.SetNestedField(db.Object, "1.5.0", agentsVersionPath...)
	_ = unstructured.SetNestedField(db.Object, "16.8.0", "status", "primary", "currentDatabaseVersion")
	_ = unstructured.SetNestedField(db.Object, "1.5.0", "status", "primary", "currentControlPlaneAgentsVersion")
	return db
}

func TestParseVersion(t *testing.T) {
	v, err := parseVersion("--version", " 17.5.0 ")
	if err != nil || v != (version{17, 5, 0}) {
		t.Fatalf("parseVersion = %v, %v; want 17.5.0", v, err)
	}
	if v.String() != "17.5.0" {
		t.Errorf("String() = %q", v.String())
	}
	for _, bad := range []string{"", "17", "17.5", "17.5.0.1", "17.x.0", "17.-1.0", "v17.5.0"} {
		if _, err := parseVersion("--version", bad); err == nil || !strings.Contains(err.Error(), "x.y.z") {
			t.Errorf("parseVersion(%q) err = %v, want an x.y.z error", bad, err)
		}
	}
}

func TestVersionCompare(t *testing.T) {
	a := version{16, 8, 0}
	if a.compare(version{16, 8, 0}) != 0 || a.compare(version{16, 9, 0}) != -1 || a.compare(version{16, 7, 9}) != 1 {
		t.Error("compare is wrong")
	}
	// Numeric, not string, order: 16.10.0 is newer than 16.9.0.
	if (version{16, 10, 0}).compare(version{16, 9, 0}) != 1 {
		t.Error("16.10.0 should be newer than 16.9.0")
	}
}

func TestCheckVersionMove(t *testing.T) {
	cur := version{16, 8, 0}
	tests := []struct {
		name   string
		flag   string
		target version
		dir    int
		moved  bool
		errHas string
	}{
		{"upgrade newer", "--version", version{16, 9, 0}, 1, true, ""},
		{"downgrade older", "--version", version{16, 7, 0}, -1, true, ""},
		{"same version", "--version", cur, 1, false, ""},
		{"upgrade to older", "--version", version{16, 7, 0}, 1, false, "adbo cluster downgrade"},
		{"downgrade to newer", "--version", version{16, 9, 0}, -1, false, "adbo cluster upgrade"},
		{"major upgrade points to MVU", "--version", version{17, 0, 0}, 1, false, "MVU.sh"},
		{"major downgrade", "--version", version{15, 0, 0}, -1, false, "same major version"},
		{"agents major change", "--agents-version", version{17, 0, 0}, 1, false, "same major version"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			moved, err := checkVersionMove(tc.flag, cur, tc.target, tc.dir)
			if moved != tc.moved {
				t.Errorf("moved = %v, want %v", moved, tc.moved)
			}
			if tc.errHas == "" && err != nil {
				t.Errorf("unexpected error %v", err)
			}
			if tc.errHas != "" && (err == nil || !strings.Contains(err.Error(), tc.errHas)) {
				t.Errorf("err = %v, want it to mention %q", err, tc.errHas)
			}
		})
	}
	// MVU.sh is only for database major upgrades, not the agents.
	if _, err := checkVersionMove("--agents-version", version{1, 5, 0}, version{2, 0, 0}, 1); err == nil || strings.Contains(err.Error(), "MVU.sh") {
		t.Errorf("agents major upgrade should not mention MVU.sh, got %v", err)
	}
}

func TestPlanVersionChangePatchHasOnlyChangedFields(t *testing.T) {
	db := versionedCluster("my-db")
	db.SetResourceVersion("42")
	changes, patch, err := planVersionChange(db, versionFlags{version: "16.9.0", agentsVersion: "1.5.0"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0] != (change{"version", "16.8.0", "16.9.0"}) {
		t.Errorf("changes = %v, want only version 16.8.0 -> 16.9.0", changes)
	}
	if v, _, _ := unstructured.NestedString(patch, dbVersionPath...); v != "16.9.0" {
		t.Errorf("patch databaseVersion = %q", v)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(patch, agentsVersionPath...); found {
		t.Error("patch should not touch the agents version when it is unchanged")
	}
	if rv, _, _ := unstructured.NestedString(patch, "metadata", "resourceVersion"); rv != "42" {
		t.Errorf("patch resourceVersion = %q, want 42", rv)
	}
}

func TestPlanVersionChangeRejects(t *testing.T) {
	changingDB := versionedCluster("my-db")
	_ = unstructured.SetNestedField(changingDB.Object, "16.9.0", dbVersionPath...)
	changingAgents := versionedCluster("my-db")
	_ = unstructured.SetNestedField(changingAgents.Object, "1.6.0", agentsVersionPath...)
	unreadable := versionedCluster("my-db")
	_ = unstructured.SetNestedField(unreadable.Object, "latest", dbVersionPath...)
	_ = unstructured.SetNestedField(unreadable.Object, "latest", "status", "primary", "currentDatabaseVersion")

	tests := []struct {
		name string
		db   *unstructured.Unstructured
		f    versionFlags
		want string
	}{
		{"database change in progress", changingDB, versionFlags{version: "16.10.0"}, "already changing version"},
		{"agents change in progress", changingAgents, versionFlags{version: "16.9.0"}, "already changing agents version"},
		{"bad flag", versionedCluster("my-db"), versionFlags{version: "17"}, "x.y.z"},
		{"unreadable current version", unreadable, versionFlags{version: "16.9.0"}, "cannot read the cluster's current version"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := planVersionChange(tc.db, tc.f, 1)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPlanVersionChangeIgnoresMissingStatus(t *testing.T) {
	// A cluster the operator has not reported on yet must not look "already changing".
	db := dbCluster("default", "my-db", "")
	_ = unstructured.SetNestedField(db.Object, "1.5.0", agentsVersionPath...)
	if _, _, err := planVersionChange(db, versionFlags{version: "16.9.0"}, 1); err != nil {
		t.Fatal(err)
	}
}

func TestClusterUpgradeAppliesWithoutPrompt(t *testing.T) {
	opts := fakeOpts(output.OutputTable, versionedCluster("my-db"))
	var out, errOut bytes.Buffer
	// Empty stdin: upgrade must not ask.
	err := runVersionChange(scaleCmd(&out, &errOut, ""), opts, "my-db", versionFlags{version: "16.9.0", agentsVersion: "1.6.0"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(errOut.String(), "Continue?") {
		t.Errorf("upgrade should not prompt, stderr:\n%s", errOut.String())
	}
	for _, want := range []string{"version   16.8.0 -> 16.9.0", "agents    1.5.0 -> 1.6.0"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr missing %q:\n%s", want, errOut.String())
		}
	}
	if !strings.Contains(out.String(), `Upgrading cluster "my-db"`) {
		t.Errorf("out = %q", out.String())
	}
	db := getCluster(t, opts, "my-db")
	if v := k8s.StringField(db, dbVersionPath...); v != "16.9.0" {
		t.Errorf("databaseVersion = %s, want 16.9.0", v)
	}
	if v := k8s.StringField(db, agentsVersionPath...); v != "1.6.0" {
		t.Errorf("controlPlaneAgentsVersion = %s, want 1.6.0", v)
	}
}

func TestClusterUpgradeLowDowntimeOnlyWithReadyStandby(t *testing.T) {
	for _, tc := range []struct {
		name     string
		standbys int64
		want     string
	}{
		{"no standby", 0, ""},
		{"ready standby", 1, "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := versionedCluster("my-db")
			_ = unstructured.SetNestedField(db.Object, tc.standbys, standbysPath...)
			opts := fakeOpts(output.OutputTable, db)
			var out, errOut bytes.Buffer
			if err := runVersionChange(scaleCmd(&out, &errOut, ""), opts, "my-db", versionFlags{version: "16.9.0"}, 1); err != nil {
				t.Fatal(err)
			}
			if got := getCluster(t, opts, "my-db").GetAnnotations()[ldtmAnnotation]; got != tc.want {
				t.Errorf("annotation = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestClusterDowngradeNeverSendsLowDowntime(t *testing.T) {
	db := versionedCluster("my-db")
	_ = unstructured.SetNestedField(db.Object, int64(1), standbysPath...)
	opts := fakeOpts(output.OutputTable, db)
	var out, errOut bytes.Buffer
	if err := runVersionChange(scaleCmd(&out, &errOut, "y\n"), opts, "my-db", versionFlags{version: "16.7.0"}, -1); err != nil {
		t.Fatal(err)
	}
	if got := getCluster(t, opts, "my-db").GetAnnotations()[ldtmAnnotation]; got != "" {
		t.Errorf("downgrade set %s = %q, want none", ldtmAnnotation, got)
	}
	if !strings.Contains(errOut.String(), "HA is reduced") {
		t.Errorf("with a standby the prompt should mention HA, got:\n%s", errOut.String())
	}
}

func TestClusterDowngradePrompts(t *testing.T) {
	opts := fakeOpts(output.OutputTable, versionedCluster("my-db"))
	var out, errOut bytes.Buffer
	if err := runVersionChange(scaleCmd(&out, &errOut, "y\n"), opts, "my-db", versionFlags{version: "16.7.0"}, -1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "unavailable until it finishes") || !strings.Contains(errOut.String(), "Continue?") {
		t.Errorf("prompt should warn about downtime, got:\n%s", errOut.String())
	}
	if !strings.Contains(out.String(), `Downgrading cluster "my-db"`) {
		t.Errorf("out = %q", out.String())
	}
	if v := k8s.StringField(getCluster(t, opts, "my-db"), dbVersionPath...); v != "16.7.0" {
		t.Errorf("databaseVersion = %s, want 16.7.0", v)
	}
}

func TestClusterDowngradeDeclinedChangesNothing(t *testing.T) {
	opts := fakeOpts(output.OutputTable, versionedCluster("my-db"))
	var out, errOut bytes.Buffer
	err := runVersionChange(scaleCmd(&out, &errOut, "n\n"), opts, "my-db", versionFlags{version: "16.7.0"}, -1)
	if !errors.Is(err, errDeclined) {
		t.Fatalf("want errDeclined, got %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("nothing should go to stdout when declined, got %q", out.String())
	}
	if v := k8s.StringField(getCluster(t, opts, "my-db"), dbVersionPath...); v != "16.8.0" {
		t.Errorf("databaseVersion changed to %s after declining", v)
	}
}

func TestClusterVersionNothingToDo(t *testing.T) {
	opts := fakeOpts(output.OutputTable, versionedCluster("my-db"))
	var out, errOut bytes.Buffer
	if err := runVersionChange(scaleCmd(&out, &errOut, ""), opts, "my-db", versionFlags{version: "16.8.0"}, 1); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to do") {
		t.Errorf("out = %q", out.String())
	}

	// -o json still prints the cluster so scripts always get JSON.
	opts = fakeOpts(output.OutputJSON, versionedCluster("my-db"))
	out.Reset()
	if err := runVersionChange(scaleCmd(&out, &errOut, ""), opts, "my-db", versionFlags{version: "16.8.0"}, 1); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
}

func TestClusterUpgradeJSONPrintsUpdatedCluster(t *testing.T) {
	opts := fakeOpts(output.OutputJSON, versionedCluster("my-db"))
	var out, errOut bytes.Buffer
	if err := runVersionChange(scaleCmd(&out, &errOut, ""), opts, "my-db", versionFlags{version: "16.9.0"}, 1); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if v, _, _ := unstructured.NestedString(got, dbVersionPath...); v != "16.9.0" {
		t.Errorf("printed databaseVersion = %q, want 16.9.0", v)
	}
}

func TestClusterVersionUnknownCluster(t *testing.T) {
	opts := fakeOpts(output.OutputTable)
	var out, errOut bytes.Buffer
	if err := runVersionChange(scaleCmd(&out, &errOut, ""), opts, "nope", versionFlags{version: "16.9.0"}, 1); err == nil {
		t.Fatal("want an error for a missing cluster")
	}
}

func TestVersionDone(t *testing.T) {
	_, patch, err := planVersionChange(versionedCluster("my-db"), versionFlags{version: "16.9.0", agentsVersion: "1.6.0"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name          string
		db, ag, phase string
		done          bool
	}{
		{"both applied and ready", "16.9.0", "1.6.0", "DBClusterReady", true},
		{"database not yet", "16.8.0", "1.6.0", "DBClusterReady", false},
		{"agents not yet", "16.9.0", "1.5.0", "DBClusterReady", false},
		{"applied but reconciling", "16.9.0", "1.6.0", "DBClusterReconciling", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := versionedCluster("my-db")
			_ = unstructured.SetNestedField(db.Object, tc.db, "status", "primary", "currentDatabaseVersion")
			_ = unstructured.SetNestedField(db.Object, tc.ag, "status", "primary", "currentControlPlaneAgentsVersion")
			_ = unstructured.SetNestedField(db.Object, tc.phase, "status", "phase")
			done, err := versionDone(patch)(db)
			if err != nil || done != tc.done {
				t.Fatalf("done=%v err=%v, want done=%v", done, err, tc.done)
			}
		})
	}
}

func TestClusterUpgradeWaitsForOperator(t *testing.T) {
	opts, dyn := waitOpts(versionedCluster("my-db"))

	// Play the operator: once the patch lands, report the new version running.
	go func() {
		for {
			cur, err := dyn.Resource(k8s.DBClusterGVR).Namespace("default").Get(t.Context(), "my-db", metav1.GetOptions{})
			if err != nil {
				return
			}
			if k8s.StringField(cur, dbVersionPath...) == "16.9.0" {
				_ = unstructured.SetNestedField(cur.Object, "16.9.0", "status", "primary", "currentDatabaseVersion")
				_, _ = dyn.Resource(k8s.DBClusterGVR).Namespace("default").Update(t.Context(), cur, metav1.UpdateOptions{})
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()

	var out, errOut bytes.Buffer
	if err := runVersionChange(scaleCmd(&out, &errOut, ""), opts, "my-db", versionFlags{version: "16.9.0", wait: true}, 1); err != nil {
		t.Fatalf("%v\nstderr:\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), `Upgraded cluster "my-db"`) {
		t.Errorf("want completion message, got %q", out.String())
	}
	if !strings.Contains(errOut.String(), "Waiting for cluster") {
		t.Errorf("want wait message on stderr, got:\n%s", errOut.String())
	}
}

func TestClusterUpgradeWaitTimesOut(t *testing.T) {
	opts, _ := waitOpts(versionedCluster("my-db")) // the operator never catches up
	opts.Timeout = 300 * time.Millisecond
	var out, errOut bytes.Buffer
	err := runVersionChange(scaleCmd(&out, &errOut, ""), opts, "my-db", versionFlags{version: "16.9.0", wait: true}, 1)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "the change is applied") {
		t.Fatalf("want a timeout that says the change is applied, got %v", err)
	}
}

func TestVersionCommandsNeedAFlag(t *testing.T) {
	for _, newCmd := range []func(*GlobalOptions) *cobra.Command{newClusterUpgradeCommand, newClusterDowngradeCommand} {
		opts := fakeOpts(output.OutputTable, versionedCluster("my-db"))
		cmd := newCmd(opts)
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs([]string{"my-db"})
		if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "version") {
			t.Errorf("%s with no flags: err = %v, want a missing-flag error", cmd.Name(), err)
		}
	}
}

func TestClusterDowngradeYesNeedsExplicitName(t *testing.T) {
	opts := fakeOpts(output.OutputTable, versionedCluster("my-db"))
	opts.Yes = true // -y is a root flag
	opts.SelectedCluster = "my-db"
	cmd := newClusterDowngradeCommand(opts)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--version", "16.7.0"})
	if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "explicit CLUSTER_NAME") {
		t.Fatalf("err = %v, want an explicit CLUSTER_NAME error", err)
	}
	if v := k8s.StringField(getCluster(t, opts, "my-db"), dbVersionPath...); v != "16.8.0" {
		t.Errorf("databaseVersion changed to %s", v)
	}

	// With the name given, -y goes through without a prompt.
	cmd = newClusterDowngradeCommand(opts)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"my-db", "--version", "16.7.0", "--no-wait"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if v := k8s.StringField(getCluster(t, opts, "my-db"), dbVersionPath...); v != "16.7.0" {
		t.Errorf("databaseVersion = %s, want 16.7.0", v)
	}
}

func TestClusterUpgradeUsesSelectedCluster(t *testing.T) {
	opts := fakeOpts(output.OutputTable, versionedCluster("my-db"))
	opts.SelectedCluster = "my-db"
	cmd := newClusterUpgradeCommand(opts)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--version", "16.9.0", "--no-wait"})
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if v := k8s.StringField(getCluster(t, opts, "my-db"), dbVersionPath...); v != "16.9.0" {
		t.Errorf("databaseVersion = %s, want 16.9.0", v)
	}
}

func TestClusterDowngradeNoTerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	opts := fakeOpts(output.OutputTable, versionedCluster("my-db"))
	cmd := newClusterDowngradeCommand(opts)
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(r)
	cmd.SetArgs([]string{"my-db", "--version", "16.7.0"})
	if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "not a terminal") {
		t.Fatalf("err = %v, want a not-a-terminal error", err)
	}
}
