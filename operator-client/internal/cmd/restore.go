package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"
)

type restoreFlags struct {
	backup string
	wait   bool
}

func newRestoreCommand(opts *GlobalOptions) *cobra.Command {
	var f restoreFlags
	var waitFlags *WaitFlags

	cmd := &cobra.Command{
		Use:   "restore [CLUSTER_NAME] --backup BACKUP_NAME",
		Short: "Restore a cluster in place from a backup",
		Long: "Replace all data in a cluster with the data from a backup.\n" +
			"Anything written after the backup was taken is lost, and the database\n" +
			"is unavailable while the restore runs, so adbo asks first (skip with -y).\n" +
			"By default adbo waits until the restore finishes; --no-wait returns at once.",
		Example: "  adbo restore my-db --backup my-db-backup-x7k2p\n" +
			"  adbo restore --backup my-db-backup-x7k2p -y --no-wait",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.wait = waitFlags.Enabled()
			if f.backup == "" {
				return errors.New("--backup is required; see `adbo backup list` for names")
			}
			name, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			return runRestore(cmd, opts, name, f)
		},
	}
	cmd.Flags().StringVar(&f.backup, "backup", "", "name of the Backup to restore from")
	waitFlags = opts.AddWaitFlags(cmd)
	return cmd
}

func newRestore(namespace, cluster, backup string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "Restore",
		"metadata": map[string]any{
			"generateName": cluster + "-restore-",
			"namespace":    namespace,
		},
		"spec": map[string]any{
			"sourceDBCluster": cluster,
			"backup":          backup,
		},
	}}
}

func runRestore(cmd *cobra.Command, opts *GlobalOptions, cluster string, f restoreFlags) error {
	c, err := opts.Clients()
	if err != nil {
		return err
	}

	ctx := cmd.Context()

	if _, err := c.In(k8s.DBClusterGVR).Get(ctx, cluster, metav1.GetOptions{}); err != nil {
		return c.FormatErr(err)
	}

	backup, err := c.In(k8s.BackupGVR).Get(ctx, f.backup, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("backup %q not found%s; run `adbo backup list %s` to see backups", f.backup, k8s.Location(c.Namespace, c.Context), cluster)
	}
	if err != nil {
		return c.FormatErr(err)
	}

	if owner := k8s.StringField(backup, "spec", "dbclusterRef"); owner != cluster {
		return fmt.Errorf("backup %q belongs to cluster %q, not %q", f.backup, owner, cluster)
	}

	if phase := k8s.StringField(backup, "status", "phase"); phase != "Succeeded" {
		return fmt.Errorf("backup %q is %s, not Succeeded; only finished backups can be restored", f.backup, phase)
	}
	taken := k8s.StringField(backup, "status", "completeTime")
	prompt := fmt.Sprintf("This will overwrite ALL data in cluster %q%s with backup %q (taken %s).\n"+"Anything written after that time will be lost, and the database will be\n"+"unavailable during the restore. Continue?", cluster, k8s.Location(c.Namespace, c.Context), f.backup, taken)
	ok, err := opts.Confirm(cmd, prompt)
	if err != nil {
		return err
	}
	if !ok {
		return errDeclined
	}

	created, err := c.In(k8s.RestoreGVR).Create(ctx, newRestore(c.Namespace, cluster, f.backup), metav1.CreateOptions{})
	if err != nil {
		return c.FormatErr(err)
	}
	result := created
	if f.wait {
		result, err = waitForRestore(cmd, opts, c, created.GetName())
		if err != nil {
			return err
		}
	}

	out := cmd.OutOrStdout()
	if isStructured(opts) {
		return output.Print(out, opts.Output, output.Table{}, result.Object)
	}

	if f.wait {
		fmt.Fprintf(out, "Restored cluster %q from backup %q.\n", cluster, f.backup)
		return nil
	}
	fmt.Fprintf(out, "Started restore %q of cluster %q. Check progress with `kubectl get restore %s -n %s`.\n",
		created.GetName(), cluster, created.GetName(), c.Namespace)
	return nil
}

func restoreDone(r *unstructured.Unstructured) (bool, error) {
	phase := wait.Phase(r)
	switch {
	case strings.HasSuffix(phase, "Failed"):
		return false, &wait.FailedError{Name: r.GetName(), Phase: phase, Reason: wait.Reason(r)}
	case k8s.StringField(r, "status", "completeTime") != "-":
		return true, nil
	case strings.HasSuffix(phase, "Succeeded"),
		strings.HasSuffix(phase, "Complete"),
		strings.HasSuffix(phase, "Completed"):
		return true, nil
	}
	return false, nil

}

func waitForRestore(cmd *cobra.Command, opts *GlobalOptions, c *k8s.Clients, name string) (*unstructured.Unstructured, error) {
	errOut := cmd.ErrOrStderr()
	fmt.Fprintf(errOut, "Waiting for restore %q to finish...\n", name)

	ctx, cancel := opts.WithTimeout(cmd.Context())
	defer cancel()

	prevPhase := ""
	last, err := wait.ForCondition(ctx, c, k8s.RestoreGVR, name, func(r *unstructured.Unstructured) (bool, error) {
		if p := wait.Phase(r); p != "" && p != prevPhase {
			fmt.Fprintf(errOut, " %s: %s\n", name, p)
			prevPhase = p
		}
		return restoreDone(r)
	})

	var failed *wait.FailedError
	switch {
	case err == nil:
		return last, nil
	case errors.As(err, &failed):
		return nil, fmt.Errorf("restore %s failed (phase %s): %s", failed.Name, failed.Phase, failed.Reason)
	case errors.Is(err, context.DeadlineExceeded):
		return nil, fmt.Errorf("restore %q did not finish within %s (last phase %q); "+"it is still running, check `kubectl get restore %s -n %s`: %w", name, opts.Timeout, wait.Phase(last), name, c.Namespace, err)
	default:
		return nil, c.FormatErr(err)
	}

}
