package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// haReadyConditionType is the condition the operator sets once the standbys
// have caught up and a failover would not lose data.
const haReadyConditionType = "HAReady"

// haReadyPollInterval is how often the HA wait re-reads the cluster.
// A variable so tests do not sleep.
var haReadyPollInterval = 5 * time.Second

func newClusterRestartCommand(opts *GlobalOptions) *cobra.Command {
	var forceConflicts, noWait bool

	cmd := &cobra.Command{
		Use:   "restart [CLUSTER_NAME]",
		Short: "Stop a database cluster and start it again",
		Long: "Stop a cluster and start it back up. The database is down from the moment\n" +
			"the stop is accepted until the cluster is ready again, so adbo asks first\n" +
			"(skip with -y). Data and the admin password are kept.\n\n" +
			"A restart is always synchronous: the start cannot be requested until the\n" +
			"operator reports the cluster stopped. If the command is interrupted in\n" +
			"between, the cluster is left stopped and `adbo cluster start` finishes it.",
		Example: "  adbo cluster restart my-db\n" +
			"  adbo cluster restart my-db -y\n" +
			"  adbo cluster restart my-db --force-conflicts",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if noWait {
				return errors.New("restart cannot run with --no-wait: it has to wait for the stop " +
					"before it can ask for the start.\nRun `adbo cluster stop CLUSTER_NAME --no-wait` " +
					"if returning early is what you want")
			}
			name, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			return runClusterRestart(cmd, opts, name, forceConflicts)
		},
	}

	cmd.Flags().BoolVar(&forceConflicts, "force-conflicts", false,
		"restart even if the cluster was edited since adbo read it, instead of failing on the conflict")
	// --no-wait is registered only to be refused. Every other long-running
	// verb takes it, so it will be typed here; catching it turns cobra's bare
	// "unknown flag" into an explanation. Hidden, since it never succeeds.
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "not supported by restart")
	_ = cmd.Flags().MarkHidden("no-wait")
	return cmd
}

func runClusterRestart(cmd *cobra.Command, opts *GlobalOptions, name string, forceConflicts bool) error {
	c, err := opts.Clients()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()

	// One budget for the whole restart. waitForClusterPhase derives its own
	// context from this one and the earlier deadline wins, so a --timeout of
	// 10m means ten minutes for the restart rather than ten for each half.
	ctx, cancel := opts.WithTimeout(cmd.Context())
	defer cancel()

	db, err := c.In(k8s.DBClusterGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("cluster %q not found in namespace %q (context %q); use -n to look in another namespace",
				name, c.Namespace, c.Context)
		}
		return c.FormatErr(err)
	}
	stopped, _, _ := unstructured.NestedBool(db.Object, isStoppedPath...)
	standbys, _, _ := unstructured.NestedInt64(db.Object, "spec", "availability", "numberOfStandbys")

	if stopped {
		// The downtime has already happened, so there is nothing to confirm
		// and nothing to take down: finish the half the cluster is missing.
		fmt.Fprintf(out, "Cluster %q is already stopped; starting it back up.\n", name)
	} else {
		ok, err := opts.Confirm(cmd, restartPrompt(name, c.Namespace, standbys))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(out, "Cancelled; the cluster is untouched.")
			return nil
		}
		if err := patchIsStopped(ctx, c, db, true, forceConflicts); err != nil {
			return fmt.Errorf("requesting stop of %q: %w", name, err)
		}
		fmt.Fprintf(out, "Stopping cluster %q in namespace %q.\n", name, c.Namespace)
		// Printed before the wait, not after it fails, so the way out is in
		// the user's scrollback even if the terminal goes away.
		fmt.Fprintf(out, "If this command does not finish, bring the cluster back up with: adbo cluster start %s\n", name)
	}

	// Timed from here, not from the prompt: the number should be the downtime,
	// not how long the user took to answer.
	began := time.Now()
	if err := waitForClusterPhase(ctx, cmd, opts, c, name, clusterStoppedPhase); err != nil {
		return stillStopped(err, name)
	}

	// Re-read: the stop moved resourceVersion, and the start patch needs the
	// current one to keep the same protection against a concurrent edit.
	db, err = c.In(k8s.DBClusterGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return stillStopped(c.FormatErr(err), name)
	}
	if err := patchIsStopped(ctx, c, db, false, forceConflicts); err != nil {
		return stillStopped(fmt.Errorf("requesting start of %q: %w", name, err), name)
	}
	fmt.Fprintf(out, "Starting cluster %q.\n", name)

	// From here the operator is bringing the cluster back on its own, so a
	// failed wait is a reporting problem rather than a stranded cluster.
	if err := waitForClusterPhase(ctx, cmd, opts, c, name, clusterReadyPhase); err != nil {
		return err
	}

	// Ready is not yet highly available: the standbys replay what they missed
	// only after the primary is serving again.
	if standbys > 0 {
		if err := waitForHAReady(ctx, cmd, c, name); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Restarted cluster %q in %s.\n", name, time.Since(began).Round(time.Second))
	return nil
}

// restartPrompt is the downtime warning. HA clusters also hear about
// switchover, which moves the primary onto a standby instead of taking the
// whole cluster down; a cluster with no standby has nowhere to move it, so
// mentioning it there would be noise.
func restartPrompt(name, namespace string, standbys int64) string {
	prompt := fmt.Sprintf("Restart cluster %q in namespace %q? The whole cluster is down until it is ready again; data is kept.",
		name, namespace)
	if standbys > 0 {
		noun := "standbys"
		if standbys == 1 {
			noun = "standby"
		}
		prompt += fmt.Sprintf("\nThis cluster has %d %s: `adbo switchover %s` moves the primary onto one of them with far less downtime.",
			standbys, noun, name)
	}
	return prompt
}

// patchIsStopped flips spec.primarySpec.isStopped on the cluster the caller
// just read. forceConflicts drops the resourceVersion precondition, so the
// patch lands even on a cluster that has been edited since that read.
func patchIsStopped(ctx context.Context, c *k8s.Clients, db *unstructured.Unstructured, stop, forceConflicts bool) error {
	resourceVersion := db.GetResourceVersion()
	if forceConflicts {
		resourceVersion = ""
	}
	body, err := isStoppedPatch(resourceVersion, stop)
	if err != nil {
		return err
	}
	if _, err := c.In(k8s.DBClusterGVR).Patch(ctx, db.GetName(), types.MergePatchType, body, metav1.PatchOptions{}); err != nil {
		if apierrors.IsConflict(err) {
			// FormatErr already says the object changed and to retry; what it
			// cannot know is that restart can be told to go ahead regardless.
			return fmt.Errorf("%w\nPass --force-conflicts to restart the cluster anyway", c.FormatErr(err))
		}
		return c.FormatErr(err)
	}
	return nil
}

// stillStopped marks a failure that happened while the cluster was down and
// nothing had asked for it to come back up. Nothing else is going to start
// it, so the message has to say how.
func stillStopped(err error, name string) error {
	return fmt.Errorf("%w\nThe cluster is still stopped; start it with: adbo cluster start %s", err, name)
}

// waitForHAReady blocks until the cluster's HAReady condition is True.
//
// A restarted cluster reaches DBClusterReady as soon as its primary serves
// again, while the standbys are still replaying what they missed; until they
// finish, a failover would lose data. The operator reports that in the
// HAReady condition, which does not move status.phase, so this polls rather
// than watching the phase like the waits above.
func waitForHAReady(ctx context.Context, cmd *cobra.Command, c *k8s.Clients, name string) error {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cluster %q is serving; waiting for its standbys to catch up.\n", name)

	var lastReason string
	for {
		db, err := c.In(k8s.DBClusterGVR).Get(ctx, name, metav1.GetOptions{})
		switch {
		case err == nil:
			status, reason := haReadyCondition(db)
			if status == "True" {
				fmt.Fprintf(out, "Cluster %q is HA-ready again.\n", name)
				return nil
			}
			if reason != "" && reason != lastReason {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s: HA not ready yet (%s)\n", name, reason)
				lastReason = reason
			}
		case ctx.Err() == nil:
			// A read that failed on its own, rather than because the wait is
			// over; the select below reports the end of the wait.
			return c.FormatErr(err)
		}

		select {
		case <-ctx.Done():
			// --timeout is the only deadline on this context, so anything
			// else is a Ctrl+C and must not be reported as a timeout.
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return ctx.Err()
			}
			msg := fmt.Sprintf("cluster %q is serving again, but its standbys had not caught up before --timeout ran out", name)
			if lastReason != "" {
				msg += fmt.Sprintf(" (the operator last reported %q)", lastReason)
			}
			return fmt.Errorf("%s\nCheck on them with: adbo status %s", msg, name)
		case <-time.After(haReadyPollInterval):
		}
	}
}

// haReadyCondition reads the HAReady condition's status and reason, both
// empty when the operator has not posted the condition at all.
func haReadyCondition(db *unstructured.Unstructured) (status, reason string) {
	conds, _, _ := unstructured.NestedSlice(db.Object, "status", "conditions")
	for _, it := range conds {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := m["type"].(string); t != haReadyConditionType {
			continue
		}
		status, _ = m["status"].(string)
		reason, _ = m["reason"].(string)
		return status, reason
	}
	return "", ""
}
