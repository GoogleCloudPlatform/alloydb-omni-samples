package cmd

import (
	"context"
	"errors"
	"fmt"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"
)

type restoreFlags struct {
	backup      string
	wait        bool
	to          string
	pointInTime string
}

func newRestoreCommand(opts *GlobalOptions) *cobra.Command {
	var f restoreFlags
	var waitFlags *WaitFlags

	cmd := &cobra.Command{
		Use:   "restore [CLUSTER_NAME] (--backup BACKUP | --point-in-time TIME --to NEW_CLUSTER)",
		Short: "Restore a cluster from a backup, in place, or into a new cluster",
		Long: "Replace all data in a cluster with the data from a backup.\n" +
			"Anything written after the backup was taken is lost, and the database\n" +
			"is unavailable while the restore runs, so adbo asks first (skip with -y).\n" +
			"By default adbo waits until the restore finishes; --no-wait returns at once.\n" +
			"With --to, adbo creates a new cluster instead and leaves the original untouched.",
		Example: "  adbo restore my-db --backup my-db-backup-x7k2p\n" +
			"  adbo restore --backup my-db-backup-x7k2p -y --no-wait\n" +
			"  adbo restore my-db --backup my-db-backup-x7k2p --to my-db-copy\n" +
			"  adbo restore my-db --point-in-time 2026-11-16T10:00:00-05:00 --to my-db-copy",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.wait = waitFlags.Enabled()
			name, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			if f.pointInTime != "" && f.to == "" {
				return errors.New("--point-in-time needs --to: a point-in-time restore always creates a new cluster")
			}
			explicit := len(args) > 0
			if f.to == "" && opts.Yes && !explicit {
				return fmt.Errorf("restore with -y needs an explicit CLUSTER_NAME; "+"the selected cluster %q is only used with an interactive confirmation", name)
			}
			if f.to == "" && !opts.Yes && !isInteractive(cmd) {
				return errors.New("restore overwrites data and needs a confirmation, but stdin is not a terminal; pass -y with an explicit CLUSTER_NAME")
			}

			return runRestore(cmd, opts, name, f)
		},
	}
	cmd.Flags().StringVar(&f.to, "to", "", "restore into a new cluster with this name instead of overwriting")
	cmd.Flags().StringVar(&f.backup, "backup", "", "name or ID of the Backup to restore from")
	cmd.Flags().StringVar(&f.pointInTime, "point-in-time", "", "restore to this time (RFC 3339, e.g. 2026-11-16T10:00:00-05:00); requires --to")
	cmd.MarkFlagsMutuallyExclusive("backup", "point-in-time")
	cmd.MarkFlagsOneRequired("backup", "point-in-time")
	waitFlags = opts.AddWaitFlags(cmd)
	return cmd
}

func newRestore(namespace, cluster, backup, to, pointInTime string) *unstructured.Unstructured {
	spec := map[string]any{
		"sourceDBCluster": cluster,
	}
	if to == "" {
		spec["backup"] = backup
	} else {
		spec["pointInTime"] = pointInTime
		spec["clonedDBClusterConfig"] = map[string]any{
			"dbclusterName": to,
		}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "Restore",
		"metadata": map[string]any{
			"generateName": cluster + "-restore-",
			"namespace":    namespace,
		},
		"spec": spec,
	}}
}

func runRestore(cmd *cobra.Command, opts *GlobalOptions, cluster string, f restoreFlags) error {
	c, err := opts.Clients()
	if err != nil {
		return err
	}

	ctx := cmd.Context()

	src, err := c.In(k8s.DBClusterGVR).Get(ctx, cluster, metav1.GetOptions{})
	if err != nil {
		return c.FormatErr(err)
	}

	var backupName, taken string
	if f.pointInTime != "" {
		taken, err = normalizePointInTime(f.pointInTime)
		if err != nil {
			return err
		}
	} else {
		backup, err := c.In(k8s.BackupGVR).Get(ctx, f.backup, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			backup, err = findBackupByID(ctx, c, f.backup)
			if err != nil {
				return c.FormatErr(err)
			}
			if backup == nil {
				return fmt.Errorf("backup %q not found%s; run `adbo backup list %s` to see backups", f.backup, k8s.Location(c.Namespace, c.Context), cluster)
			}
		}
		if err != nil {
			return c.FormatErr(err)
		}
		backupName = backup.GetName()
		if owner := k8s.StringField(backup, "spec", "dbclusterRef"); owner != cluster {
			return fmt.Errorf("backup %q belongs to cluster %q, not %q", backupName, owner, cluster)
		}

		if phase := k8s.StringField(backup, "status", "phase"); phase != "Succeeded" {
			return fmt.Errorf("backup %q is %s, not Succeeded; only finished backups can be restored", backupName, phase)
		}
		taken = k8s.StringField(backup, "status", "completeTime")
		if taken == "-" {
			if f.to != "" {
				return fmt.Errorf("backup %q has no completeTime, so adbo cannot tell what time to clone from", backupName)
			}
			taken = "at an unknown time"
		}
	}
	if f.to != "" {
		_, err := c.In(k8s.DBClusterGVR).Get(ctx, f.to, metav1.GetOptions{})
		if err == nil {
			return fmt.Errorf("cluster %q already exists; pick a new name for --to", f.to)
		}
		if !apierrors.IsNotFound(err) {
			return c.FormatErr(err)
		}
	}
	if f.to == "" {
		prompt := fmt.Sprintf("This will overwrite ALL data in cluster %q%s with backup %q (taken %s).\n"+"Anything written after that time will be lost, and the database will be\n"+"unavailable during the restore. Continue?", cluster, k8s.Location(c.Namespace, c.Context), backupName, taken)
		ok, err := opts.Confirm(cmd, prompt)
		if err != nil {
			return err
		}

		if !ok {
			return errDeclined
		}
	}
	// createdSecret is true only when this run made db-pw-<to>, so a failed
	// Restore never deletes a Secret that was already there.
	createdSecret := false
	if pwRef := k8s.StringField(src, "spec", "primarySpec", "adminUser", "passwordRef", "name"); f.to != "" && pwRef != "-" {
		createdSecret, err = copyPasswordSecret(ctx, c, pwRef, cluster, f.to)
		if err != nil {
			return c.FormatErr(err)
		}
	}
	created, err := c.In(k8s.RestoreGVR).Create(ctx, newRestore(c.Namespace, cluster, backupName, f.to, taken), metav1.CreateOptions{})
	if err != nil {
		if createdSecret {
			_ = c.Typed.CoreV1().Secrets(c.Namespace).Delete(ctx, k8s.AdminSecretName(f.to), metav1.DeleteOptions{})
		}
		return c.FormatErr(err)
	}

	result := created
	target := cluster
	if f.to != "" {
		target = f.to
	}
	if f.wait {
		result, err = waitForRestore(cmd, opts, c, created.GetName(), target)
		if err != nil {
			return err
		}
	}

	out := cmd.OutOrStdout()
	if isStructured(opts) {
		return output.Print(out, opts.Output, output.Table{}, result.Object)
	}

	if f.wait {
		if f.to != "" {
			if f.pointInTime != "" {
				fmt.Fprintf(out, "Created cluster %q from %q as of %s.\n", f.to, cluster, taken)
			} else {
				fmt.Fprintf(out, "Created cluster %q from backup %q of %q.\n", f.to, backupName, cluster)
			}
		} else {
			fmt.Fprintf(out, "Restored cluster %q from backup %q.\n", cluster, backupName)
		}
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

func waitForRestore(cmd *cobra.Command, opts *GlobalOptions, c *k8s.Clients, name, cluster string) (*unstructured.Unstructured, error) {
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
		if _, err := waitForClusterReady(ctx, cmd, opts, c, cluster); err != nil {
			return nil, err
		}
		return last, nil
	case errors.As(err, &failed):
		return nil, fmt.Errorf("restore %s failed (phase %s): %s", failed.Name, failed.Phase, failed.Reason)
	case errors.Is(err, context.DeadlineExceeded):
		return nil, fmt.Errorf("restore %q did not finish within %s (last phase %q); "+"it is still running, check `kubectl get restore %s -n %s`: %w", name, opts.Timeout, wait.Phase(last), name, c.Namespace, err)
	default:
		return nil, c.FormatErr(err)
	}

}

func waitForClusterReady(ctx context.Context, cmd *cobra.Command, opts *GlobalOptions, c *k8s.Clients, cluster string) (*unstructured.Unstructured, error) {
	errOut := cmd.ErrOrStderr()
	fmt.Fprintf(errOut, "Waiting for cluster %q to be ready...\n", cluster)

	last, err := wait.ForCondition(ctx, c, k8s.DBClusterGVR, cluster, func(db *unstructured.Unstructured) (bool, error) {
		if p := wait.Phase(db); strings.HasSuffix(p, "Failed") {
			return false, &wait.FailedError{Name: db.GetName(), Phase: p, Reason: wait.Reason(db)}
		}
		return wait.Phase(db) == scaleReadyPhase, nil
	})
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("cluster %q was not ready within %s (last phase %q): %w", cluster, opts.Timeout, wait.Phase(last), err)
	}
	var failed *wait.FailedError
	if errors.As(err, &failed) {
		return nil, fmt.Errorf("cluster %s failed after the restore (phase %s): %s", failed.Name, failed.Phase, failed.Reason)
	}
	if err != nil {
		return nil, c.FormatErr(err)
	}
	return last, nil

}

func findBackupByID(ctx context.Context, c *k8s.Clients, id string) (*unstructured.Unstructured, error) {
	list, err := c.In(k8s.BackupGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		b := &list.Items[i]
		if k8s.StringField(b, "status", "physicalBackupStatus", "backupID") == id {
			return b, nil
		}
	}
	return nil, nil
}

// copyPasswordSecret copies the source cluster's admin password, from the
// Secret its passwordRef names, into db-pw-<to> under key <to>, the name and
// key a new cluster reads. It reports whether it created the Secret; an
// existing one (left by an earlier attempt) is updated instead.
func copyPasswordSecret(ctx context.Context, c *k8s.Clients, srcSecret, from, to string) (bool, error) {
	secrets := c.Typed.CoreV1().Secrets(c.Namespace)
	src, err := secrets.Get(ctx, srcSecret, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	password, ok := src.Data[from]
	if !ok && len(src.Data) == 1 {
		// A custom Secret may use its own key; with only one there is no doubt.
		for _, v := range src.Data {
			password, ok = v, true
		}
	}
	if !ok {
		return false, fmt.Errorf("secret %s has no key %q; cannot tell which value is the password", srcSecret, from)
	}
	data := map[string][]byte{to: password}
	_, err = secrets.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: k8s.AdminSecretName(to), Namespace: c.Namespace},
		Data:       data,
	}, metav1.CreateOptions{})
	if !apierrors.IsAlreadyExists(err) {
		return err == nil, err
	}
	existing, err := secrets.Get(ctx, k8s.AdminSecretName(to), metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	existing.Data = data
	_, err = secrets.Update(ctx, existing, metav1.UpdateOptions{})
	return false, err
}

func normalizePointInTime(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	s = strings.Replace(s, " ", "T", 1)
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "", fmt.Errorf("--point-in-time %q is not a valid time; use RFC 3339 with a timezone, e.g. 2026-11-16T10:00:00-05:00 or 2026-11-16T15:00:00Z", s)
	}
	return t.UTC().Format(time.RFC3339), nil
}
