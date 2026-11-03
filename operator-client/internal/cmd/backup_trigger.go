package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"
)

// Omni backup phases (status.phase). A Backup CR is created immediately;
// the operator later sets one of these.
const (
	backupPhaseSucceeded = "Succeeded"
	backupPhaseFailed    = "Failed"
)

// newBackupTriggerCommand is `adbo backup trigger [CLUSTER_NAME]`.
//
// Omni will not take a Backup unless a BackupPlan already exists. This
// command finds or creates a plan, creates a manual Backup CR, then
// waits for Succeeded/Failed unless --no-wait is set.
func newBackupTriggerCommand(opts *GlobalOptions) *cobra.Command {
	var (
		backupType string
		planName   string
		waitFlags  *WaitFlags
	)
	cmd := &cobra.Command{
		Use:   "trigger [CLUSTER_NAME]",
		Short: "Take an on-demand backup",
		Example: "  adbo backup trigger my-db\n" +
			"  adbo backup trigger my-db --no-wait\n" +
			"  adbo backup trigger my-db --type incr",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cluster, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			return runBackupTrigger(cmd, opts, triggerOpts{
				cluster:    cluster,
				backupType: backupType,
				planName:   planName,
				wait:       waitFlags.Enabled(),
			})
		},
	}
	waitFlags = opts.AddWaitFlags(cmd)
	cmd.Flags().StringVar(&backupType, "type", "full", "backup type: full, diff, or incr")
	cmd.Flags().StringVar(&planName, "plan", "", "BackupPlan name (default: existing plan for the cluster, or create one)")
	return cmd
}

type triggerOpts struct {
	cluster    string
	backupType string
	planName   string
	wait       bool
}

func runBackupTrigger(cmd *cobra.Command, opts *GlobalOptions, in triggerOpts) error {
	switch in.backupType {
	case "full", "diff", "incr": // CRD enum: backup.spec.physicalBackupSpec.backupType
	default:
		return fmt.Errorf("--type must be full, diff, or incr")
	}

	ctx := cmd.Context()
	c, err := opts.Clients()
	if err != nil {
		return err
	}

	cluster, err := c.In(k8s.DBClusterGVR).Get(ctx, in.cluster, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("cluster %q not found in namespace %q (try -n or --context %q)",
			in.cluster, c.Namespace, c.Context)
	}
	if err != nil {
		return c.FormatErr(err)
	}
	if err := clusterReadyForBackup(cluster, in.cluster); err != nil {
		return err
	}

	plan, err := resolveBackupPlan(ctx, c, in.cluster, in.planName, cmd.ErrOrStderr())
	if err != nil {
		return err
	}

	created, err := c.In(k8s.BackupGVR).Create(ctx, newManualBackup(c.Namespace, in.cluster, plan, in.backupType), metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating backup: %w", c.FormatErr(err))
	}
	fmt.Fprintf(cmd.OutOrStdout(), "backup %s created in namespace %q\n", created.GetName(), c.Namespace)
	if !in.wait {
		return nil
	}
	return waitForBackup(ctx, cmd, opts, c, created.GetName())
}

// clusterReadyForBackup fails before Create: a stopped cluster accepts the
// Backup CR then deletes it, which wait reports as "was deleted while waiting".
func clusterReadyForBackup(cluster *unstructured.Unstructured, name string) error {
	if stopped, _, _ := unstructured.NestedBool(cluster.Object, "spec", "primarySpec", "isStopped"); stopped {
		return fmt.Errorf("cluster %q is stopped; start it before taking a backup", name)
	}
	if phase := wait.Phase(cluster); phase == "DBClusterStopped" {
		return fmt.Errorf("cluster %q is %s; start it before taking a backup", name, phase)
	}
	return nil
}

func newManualBackup(namespace, cluster, plan, backupType string) *unstructured.Unstructured {
	// generateName lets the API append a suffix so two triggers do not collide.
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "Backup",
		"metadata": map[string]any{
			"generateName": cluster + "-backup-",
			"namespace":    namespace,
		},
		"spec": map[string]any{
			"dbclusterRef":  cluster,
			"backupPlanRef": plan,
			"manual":        true,
			"physicalBackupSpec": map[string]any{
				"backupType": backupType,
			},
		},
	}}
}

func waitForBackup(ctx context.Context, cmd *cobra.Command, opts *GlobalOptions, c *k8s.Clients, name string) error {
	waitCtx, cancel := opts.WithTimeout(ctx)
	defer cancel()
	done, err := wait.ForPhaseWatch(waitCtx, c, wait.Options{
		GVR:     k8s.BackupGVR,
		Name:    name,
		Success: []string{backupPhaseSucceeded},
		Failure: []string{backupPhaseFailed},
		Timeout: 0, // deadline is already on waitCtx
		OnChange: func(phase string) {
			fmt.Fprintf(cmd.ErrOrStderr(), "  %s: %s\n", name, phase)
		},
	})
	if err != nil {
		var failed *wait.FailedError
		switch {
		case errors.As(err, &failed):
			return fmt.Errorf("backup %s failed: %s", failed.Name, failed.Reason)
		case errors.Is(err, context.DeadlineExceeded):
			return fmt.Errorf("backup %s did not finish in time: %w", name, err)
		default:
			return fmt.Errorf("waiting for backup %s: %w", name, c.FormatErr(err))
		}
	}
	fmt.Fprintf(cmd.OutOrStdout(), "backup %s %s\n", done.GetName(), wait.Phase(done))
	return nil
}

// resolveBackupPlan picks the BackupPlan for this cluster:
//  1. --plan if the user passed one
//  2. an existing plan whose spec.dbclusterRef matches
//  3. create <cluster>-backupplan (paused, so we do not start cron backups)
func resolveBackupPlan(ctx context.Context, c *k8s.Clients, cluster, planName string, errOut io.Writer) (string, error) {
	if planName != "" {
		if _, err := c.In(k8s.BackupPlanGVR).Get(ctx, planName, metav1.GetOptions{}); apierrors.IsNotFound(err) {
			return "", fmt.Errorf("backup plan %q not found in namespace %q", planName, c.Namespace)
		} else if err != nil {
			return "", c.FormatErr(err)
		}
		return planName, nil
	}

	list, err := c.In(k8s.BackupPlanGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("listing backup plans: %w", c.FormatErr(err))
	}
	for i := range list.Items {
		if dbclusterRef(&list.Items[i]) == cluster {
			return list.Items[i].GetName(), nil
		}
	}

	plan := defaultBackupPlan(c.Namespace, cluster)
	created, err := c.In(k8s.BackupPlanGVR).Create(ctx, plan, metav1.CreateOptions{})
	// Two triggers at once can both miss the List and hit Create.
	if apierrors.IsAlreadyExists(err) {
		existing, getErr := c.In(k8s.BackupPlanGVR).Get(ctx, plan.GetName(), metav1.GetOptions{})
		if getErr != nil {
			return "", c.FormatErr(getErr)
		}
		ref := dbclusterRef(existing)
		if ref != cluster {
			return "", fmt.Errorf("backup plan %q already exists for cluster %q, not %q", existing.GetName(), ref, cluster)
		}
		return existing.GetName(), nil
	}
	if err != nil {
		return "", fmt.Errorf("creating default backup plan: %w", c.FormatErr(err))
	}
	fmt.Fprintf(errOut, "created paused backup plan %s for %s (no scheduled backups; unpause the plan to enable cron)\n",
		created.GetName(), cluster)
	return created.GetName(), nil
}

func defaultBackupPlan(namespace, cluster string) *unstructured.Unstructured {
	// paused: true — CRD says this only stops scheduling. Manual Backups
	// still work, so trigger does not silently start weekly/daily jobs.
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "BackupPlan",
		"metadata": map[string]any{
			"name":      cluster + "-backupplan",
			"namespace": namespace,
		},
		"spec": map[string]any{
			"dbclusterRef":     cluster,
			"backupRetainDays": int64(14),
			"paused":           true,
			"backupSchedules": map[string]any{
				"full":        "0 0 * * 0",
				"incremental": "0 21 * * *",
			},
		},
	}}
}
