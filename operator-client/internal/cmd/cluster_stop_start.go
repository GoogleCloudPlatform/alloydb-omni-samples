package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// clusterStoppedPhase is status.phase once the operator has shut the database
// down. Stopping keeps the disks; only the compute goes away.
const clusterStoppedPhase = "DBClusterStopped"

// isStoppedPath is the switch both verbs flip.
var isStoppedPath = []string{"spec", "primarySpec", "isStopped"}

// powerOp is what the user asked for. Named rather than three bare bools, so
// the call sites say which verb they are.
type powerOp struct {
	stop   bool // false means start
	wait   bool
	dryRun bool
}

func newClusterStopCommand(opts *GlobalOptions) *cobra.Command {
	var dryRun bool
	var w *WaitFlags

	cmd := &cobra.Command{
		Use:   "stop [CLUSTER_NAME]",
		Short: "Stop a database cluster, keeping its data",
		Long: "Shut the database down by setting spec.primarySpec.isStopped.\n" +
			"Disks and the admin secret are kept, so `adbo cluster start` brings the\n" +
			"same data back. Connections are dropped, so adbo asks first (skip with -y).",
		Example: "  adbo cluster stop my-db\n" +
			"  adbo cluster stop my-db -y --no-wait",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			return runClusterPower(cmd, opts, name, powerOp{stop: true, wait: w.Enabled(), dryRun: dryRun})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"ask the API server to validate the change without applying it")
	w = opts.AddWaitFlags(cmd)
	return cmd
}

func newClusterStartCommand(opts *GlobalOptions) *cobra.Command {
	var dryRun bool
	var w *WaitFlags

	cmd := &cobra.Command{
		Use:   "start [CLUSTER_NAME]",
		Short: "Start a stopped database cluster",
		Long: "Bring a stopped database back up by clearing spec.primarySpec.isStopped.\n" +
			"The cluster keeps the data and the admin password it had when it stopped.",
		Example: "  adbo cluster start my-db\n" +
			"  adbo cluster start my-db --no-wait",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			return runClusterPower(cmd, opts, name, powerOp{wait: w.Enabled(), dryRun: dryRun})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"ask the API server to validate the change without applying it")
	w = opts.AddWaitFlags(cmd)
	return cmd
}

// runClusterPower is both verbs: stop sets isStopped, start clears it.
func runClusterPower(cmd *cobra.Command, opts *GlobalOptions, name string, op powerOp) error {
	c, err := opts.Clients()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	verb, wantPhase := "start", clusterReadyPhase
	if op.stop {
		verb, wantPhase = "stop", clusterStoppedPhase
	}

	// Read first so a typo is a clear error rather than a prompt, and so an
	// already-stopped cluster is a no-op instead of a pointless write.
	db, err := c.In(k8s.DBClusterGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return c.FormatErr(err)
	}
	stopped, _, _ := unstructured.NestedBool(db.Object, isStoppedPath...)
	if stopped == op.stop {
		state := "running"
		if stopped {
			state = "stopped"
		}
		fmt.Fprintf(out, "Cluster %q is already %s; nothing to do.\n", name, state)
		return nil
	}

	// Stopping drops every connection, so confirm it. Starting is additive.
	if op.stop {
		ok, err := opts.Confirm(cmd, fmt.Sprintf(
			"Stop cluster %q in namespace %q? Open connections will be dropped; data is kept.", name, c.Namespace))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(out, "Cancelled; the cluster is still running.")
			return nil
		}
	}

	body, err := isStoppedPatch(db, op.stop)
	if err != nil {
		return err
	}
	patchOpts := metav1.PatchOptions{}
	if op.dryRun {
		patchOpts.DryRun = []string{metav1.DryRunAll}
	}
	if _, err := c.In(k8s.DBClusterGVR).Patch(ctx, name, types.MergePatchType, body, patchOpts); err != nil {
		return fmt.Errorf("requesting %s of %q: %w", verb, name, c.FormatErr(err))
	}

	if op.dryRun {
		fmt.Fprintf(out, "Dry run: %s of cluster %q was validated and not applied.\n", verb, name)
		return nil
	}
	fmt.Fprintf(out, "Requested %s of cluster %q in namespace %q.\n", verb, name, c.Namespace)
	if !op.wait {
		fmt.Fprintf(out, "Not waiting. Check progress with: adbo status %s\n", name)
		return nil
	}
	return waitForClusterPhase(ctx, cmd, opts, c, name, wantPhase)
}

// isStoppedPatch sets spec.primarySpec.isStopped and carries the object's
// resourceVersion, so a cluster edited while the user was at the prompt fails
// with a Conflict rather than having that edit silently overwritten.
func isStoppedPatch(db *unstructured.Unstructured, stop bool) ([]byte, error) {
	patch := map[string]any{
		"metadata": map[string]any{"resourceVersion": db.GetResourceVersion()},
	}
	if err := unstructured.SetNestedField(patch, stop, isStoppedPath...); err != nil {
		return nil, err
	}
	return json.Marshal(patch)
}
