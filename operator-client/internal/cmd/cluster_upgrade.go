package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"

	"context"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"strconv"
	"strings"
)

type version [3]int

var (
	dbVersionPath     = []string{"spec", "databaseVersion"}
	agentsVersionPath = []string{"spec", "controlPlaneAgentsVersion"}
	dbRunningPath     = []string{"status", "primary", "currentDatabaseVersion"}
	agentsRunningPath = []string{"status", "primary", "currentControlPlaneAgentsVersion"}
)

type versionFlags struct {
	version, agentsVersion string
	wait                   bool
}

func planVersionChange(db *unstructured.Unstructured, f versionFlags, dir int) ([]change, map[string]any, error) {
	fields := []struct {
		flag, field, value string
		path, running      []string
	}{
		{"--version", "version", f.version, dbVersionPath, dbRunningPath},
		{"--agents-version", "agents", f.agentsVersion, agentsVersionPath, agentsRunningPath},
	}
	// A change is in progress when spec asks for a version the cluster is not
	// running yet. A spec that omits the field (samples/v1_dbcluster.yaml,
	// `cluster create`) leaves the choice to the operator, so it is not one.
	for _, t := range fields {
		spec, running := k8s.StringField(db, t.path...), k8s.StringField(db, t.running...)
		if spec != "-" && running != "-" && spec != running {
			return nil, nil, fmt.Errorf("cluster %q is already changing its %s (running %s, spec %s); wait for it to finish", db.GetName(), t.flag[2:], running, spec)
		}
	}
	var changes []change
	patch := map[string]any{"metadata": map[string]any{"resourceVersion": db.GetResourceVersion()}}
	for _, t := range fields {
		if t.value == "" {
			continue
		}
		target, err := parseVersion(t.flag, t.value)
		if err != nil {
			return nil, nil, err
		}
		curStr := k8s.StringField(db, t.path...)
		if curStr == "-" {
			curStr = k8s.StringField(db, t.running...) // spec omits it; use what is running
		}
		cur, err := parseVersion("current "+t.field, curStr)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot read the cluster's current %s (%q); refusing to change it", t.field, curStr)
		}
		moved, err := checkVersionMove(t.flag, cur, target, dir)
		if err != nil {
			return nil, nil, err
		}
		if !moved {
			continue
		}
		if err := unstructured.SetNestedField(patch, target.String(), t.path...); err != nil {
			return nil, nil, err
		}
		changes = append(changes, change{t.field, cur.String(), target.String()})
	}
	return changes, patch, nil
}
func parseVersion(flag, s string) (version, error) {
	var v version
	parts := strings.Split(strings.TrimSpace(s), ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("%s %q must look like x.y.z, e.g. 17.5.0", flag, s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, fmt.Errorf("%s %q must look like x.y.z, e.g. 17.5.0", flag, s)
		}
		v[i] = n
	}
	return v, nil
}

func (v version) String() string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}

func (v version) compare(o version) int {
	for i := 0; i < 3; i++ {
		if v[i] < o[i] {
			return -1
		}
		if v[i] > o[i] {
			return 1
		}
	}
	return 0
}

func checkVersionMove(flag string, cur, target version, dir int) (bool, error) {
	if target[0] != cur[0] {
		if dir > 0 && flag == "--version" {
			return false, fmt.Errorf("%s %s is a major version change from %s; adbo only upgrades within the same major version. "+"For a major upgrade, follow samples/MVU.sh", flag, target, cur)
		}
		return false, fmt.Errorf("%s %s is a major version change from %s; adbo only changes versions within the same major version", flag, target, cur)
	}
	switch target.compare(cur) {
	case 0:
		return false, nil
	case dir:
		return true, nil
	}
	if dir > 0 {
		return false, fmt.Errorf("%s %s is older than the current %s; use `adbo cluster downgrade`", flag, target, cur)
	}
	return false, fmt.Errorf("%s %s is newer than the current %s; use `adbo cluster upgrade`", flag, target, cur)

}

func runVersionChange(cmd *cobra.Command, opts *GlobalOptions, name string, f versionFlags, dir int) error {
	c, err := opts.Clients()
	if err != nil {
		return err
	}
	db, err := c.In(k8s.DBClusterGVR).Get(cmd.Context(), name, metav1.GetOptions{})
	if err != nil {
		return c.FormatErr(err)
	}
	changes, patch, err := planVersionChange(db, f, dir)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(changes) == 0 {
		if isStructured(opts) {
			return output.Print(out, opts.Output, output.Table{}, db.Object)
		}
		fmt.Fprintf(out, "Cluster %q is already on these versions; nothing to do.\n", name)
		return nil
	}
	if dir > 0 && hasReadyStandby(db) && db.GetAnnotations()[ldtmAnnotation] != "false" {
		if err := unstructured.SetNestedField(patch, "true", "metadata", "annotations", ldtmAnnotation); err != nil {
			return err
		}
	}
	printChanges(cmd.ErrOrStderr(), name, changes)
	if dir < 0 {
		prompt := "Downgrading restarts the database"
		if hasReadyStandby(db) {
			prompt += "; standbys are downgraded too and HA is reduced until they catch up."
		} else {
			prompt += ", and with no standbys the cluster is unavailable until it finishes."
		}
		ok, err := opts.Confirm(cmd, prompt+" Continue?")
		if err != nil {
			return err
		}
		if !ok {
			return errDeclined
		}
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	updated, err := c.In(k8s.DBClusterGVR).Patch(cmd.Context(), name, types.MergePatchType, body, metav1.PatchOptions{})

	if err != nil {
		return c.FormatErr(err)
	}
	if f.wait {
		if updated, err = waitForVersion(cmd, opts, c, name, updated.GetGeneration(), patch); err != nil {
			return err
		}
	}
	if isStructured(opts) {
		return output.Print(out, opts.Output, output.Table{}, updated.Object)
	}
	if f.wait {
		verb := "Upgraded"
		if dir < 0 {
			verb = "Downgraded"
		}
		fmt.Fprintf(out, "%s cluster %q%s.\n", verb, name, k8s.Location(c.Namespace, c.Context))
		return nil
	}
	verb := "Upgrading"
	if dir < 0 {
		verb = "Downgrading"
	}
	fmt.Fprintf(out, "%s cluster %q%s. Check progress with `adbo cluster list`.\n", verb, name, k8s.Location(c.Namespace, c.Context))
	return nil
}

func newClusterUpgradeCommand(opts *GlobalOptions) *cobra.Command {
	var f versionFlags
	var waitFlags *WaitFlags
	cmd := &cobra.Command{
		Use:   "upgrade [CLUSTER_NAME] --version X.Y.Z [--agents-version A.B.C]",
		Short: "Upgrade a cluster to a newer minor or patch version",
		Long: "Upgrade the database version, the control plane agents version, or both.\n" +
			"Only upgrades within the same major version (e.g. 17.5.0 -> 17.6.0) are supported;\n" +
			"for a major upgrade, follow samples/MVU.sh. When the cluster has a ready standby,\n" +
			"adbo asks the operator for a low-downtime upgrade.",
		Example: "  adbo cluster upgrade my-db --version 17.6.0\n" +
			"  adbo cluster upgrade my-db --version 17.6.0 --agents-version 1.7.0",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.wait = waitFlags.Enabled()
			name, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			return runVersionChange(cmd, opts, name, f, +1)
		},
	}
	cmd.Flags().StringVar(&f.version, "version", "", "target database version, e.g. 17.6.0")
	cmd.Flags().StringVar(&f.agentsVersion, "agents-version", "", "target control plane agents version, e.g. 1.7.0")
	cmd.MarkFlagsOneRequired("version", "agents-version")
	waitFlags = opts.AddWaitFlags(cmd)
	return cmd

}

func newClusterDowngradeCommand(opts *GlobalOptions) *cobra.Command {
	var f versionFlags
	var waitFlags *WaitFlags
	cmd := &cobra.Command{
		Use:   "downgrade [CLUSTER_NAME] --version X.Y.Z [--agents-version A.B.C]",
		Short: "Downgrade a cluster to an older minor or patch version",
		Long: "Downgrade the database version, the control plane agents version, or both,\n" +
			"within the same major version. This restarts the database, so adbo asks first\n" +
			"(skip with -y, which also needs an explicit CLUSTER_NAME).",
		Example: "  adbo cluster downgrade my-db --version 17.4.0\n" +
			"  adbo cluster downgrade my-db --agents-version 1.5.0 -y",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.wait = waitFlags.Enabled()
			name, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			if opts.Yes && len(args) == 0 {
				return fmt.Errorf("downgrade with -y needs an explicit CLUSTER_NAME; "+"the selected cluster %q is only used with an interactive confirmation", name)
			}
			if !opts.Yes && !isInteractive(cmd) {
				return errors.New("downgrade needs a confirmation, but stdin is not a terminal; pass -y with an explicit CLUSTER_NAME")
			}
			return runVersionChange(cmd, opts,
				name, f, -1)
		},
	}
	cmd.Flags().StringVar(&f.version, "version", "", "target database version, e.g. 17.4.0")
	cmd.Flags().StringVar(&f.agentsVersion, "agents-version", "", "target control plane agents version, e.g. 1.5.0")
	cmd.MarkFlagsOneRequired("version", "agents-version")
	waitFlags = opts.AddWaitFlags(cmd)
	return cmd
}

// versionDone is done when the operator has handled this patch (generation),
// is no longer upgrading, runs the requested versions and is Ready.
//
// During a low-downtime upgrade the operator upgrades the standby, switches
// over, then upgrades the old primary with DBCUpgradeInProgress still True.
// Right after the switchover status.primary already reports the new version
// and the phase is Ready, so that condition is what says it is really done.
func versionDone(generation int64, patch map[string]any) wait.Condition {
	return func(db *unstructured.Unstructured) (bool, error) {
		phase := wait.Phase(db)
		if strings.HasSuffix(phase, "Failed") {
			return false, &wait.FailedError{Name: db.GetName(), Phase: phase, Reason: wait.Reason(db)}
		}
		if conditionReason(db, "DBCUpgradeInProgress") == "UpgradeFailed" {
			return false, &wait.FailedError{Name: db.GetName(), Phase: "UpgradeFailed", Reason: wait.Reason(db)}
		}
		if og, found, _ := unstructured.NestedInt64(db.Object, "status", "observedGeneration"); found && og < generation {
			return false, nil
		}
		if k8s.ConditionStatus(db, "DBCUpgradeInProgress") == "True" {
			return false, nil
		}
		if want, found, _ := unstructured.NestedString(patch, dbVersionPath...); found && k8s.StringField(db, dbRunningPath...) != want {
			return false, nil
		}
		if want, found, _ := unstructured.NestedString(patch, agentsVersionPath...); found && k8s.StringField(db, agentsRunningPath...) != want {
			return false, nil
		}
		return phase == scaleReadyPhase, nil
	}
}

// conditionReason returns the reason on condition condType, or "".
func conditionReason(obj *unstructured.Unstructured, condType string) string {
	conds, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, c := range conds {
		if m, ok := c.(map[string]any); ok && m["type"] == condType {
			r, _ := m["reason"].(string)
			return r
		}
	}
	return ""
}

func waitForVersion(cmd *cobra.Command, opts *GlobalOptions, c *k8s.Clients, name string, generation int64, patch map[string]any) (*unstructured.Unstructured, error) {
	fmt.Fprintf(cmd.ErrOrStderr(), "Waiting for cluster %q to finish...\n", name)

	ctx, cancel := opts.WithTimeout(cmd.Context())
	defer cancel()

	last, err := wait.ForCondition(ctx, c, k8s.DBClusterGVR, name, versionDone(generation, patch))
	var failed *wait.FailedError
	if errors.As(err, &failed) {
		return nil, fmt.Errorf("cluster %s failed to change version (%s): %s", failed.Name, failed.Phase, failed.Reason)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("cluster %q did not finish within %s; the change is applied and "+"the operator is still working on it, check `adbo cluster list`: %w", name, opts.Timeout, err)
	}
	if err != nil {
		return nil, c.FormatErr(err)
	}
	return last, nil
}
