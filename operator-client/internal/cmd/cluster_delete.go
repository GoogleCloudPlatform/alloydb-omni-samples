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
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// deleteGonePollInterval is how often `cluster delete --wait` re-reads the
// DBCluster while waiting for it to disappear. A variable so tests do not sleep.
var deleteGonePollInterval = 3 * time.Second

func newClusterDeleteCommand(opts *GlobalOptions) *cobra.Command {
	var keepSecret, dryRun bool
	var w *WaitFlags

	cmd := &cobra.Command{
		Use:   "delete [CLUSTER_NAME]",
		Short: "Delete a database cluster and its admin password secret",
		Long: "Delete a DBCluster and, unless --keep-secret is passed, the db-pw-<name>\n" +
			"Secret that held its admin password. This destroys the database and its\n" +
			"data; adbo asks before deleting anything (skip with -y).\n" +
			"Deletion is asynchronous: the operator runs finalizers to remove the\n" +
			"instances and their disks. delete waits for that to finish, which is\n" +
			"what you want before recreating a cluster with the same name; pass\n" +
			"--no-wait to return as soon as the API server accepts the delete.",
		Example: "  adbo cluster delete my-db\n" +
			"  adbo cluster delete my-db --keep-secret\n" +
			"  adbo cluster delete my-db --no-wait -y",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			return runClusterDelete(cmd, opts, name, keepSecret, w.Enabled(), dryRun)
		},
	}

	cmd.Flags().BoolVar(&keepSecret, "keep-secret", false,
		"leave the db-pw-<name> admin secret in place")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false,
		"ask the API server to validate the delete without performing it")
	// Shared --wait / --no-wait so every state-changing verb behaves the same.
	w = opts.AddWaitFlags(cmd)
	return cmd
}

func runClusterDelete(cmd *cobra.Command, opts *GlobalOptions, name string, keepSecret, wait, dryRun bool) error {
	c, err := opts.Clients()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	// -o json/yaml promises stdout to a parser, so the commentary goes to
	// stderr and the document is the only thing on stdout.
	out := opts.Prose(cmd)

	// Read first so a typo is a clear error instead of a confirmation prompt
	// for something that does not exist.
	db, err := c.In(k8s.DBClusterGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		// Only claim the cluster is missing when the 404 was about the object.
		// A cluster with no Omni CRDs answers the same way, and FormatErr says
		// so; intercepting that here would send the user hunting for a typo.
		if k8s.IsObjectNotFound(err) {
			return fmt.Errorf("cluster %q not found%s; use -n to look in another namespace", name, c.Where())
		}
		return c.FormatErr(err)
	}

	secretName := k8s.AdminSecretName(name)
	if dryRun {
		return dryRunDelete(ctx, cmd, c, name, secretName, keepSecret)
	}

	prompt := fmt.Sprintf("Delete cluster %q in namespace %q and its database? This cannot be undone.", name, c.Namespace)
	if !keepSecret {
		prompt = fmt.Sprintf("Delete cluster %q in namespace %q, its database, and the secret %q? This cannot be undone.",
			name, c.Namespace, secretName)
	}
	ok, err := opts.Confirm(cmd, prompt)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%w; nothing was deleted", errDeclined)
	}

	// Omni's validating webhook rejects a DELETE while spec.isDeleted is
	// false ("Cannot delete a DBCluster if spec.isDeleted is false"): the
	// operator wants an explicit request first so it can release network
	// endpoints and other resources. Patch, then delete.
	if requested, _, _ := unstructured.NestedBool(db.Object, "spec", "isDeleted"); !requested {
		patch := []byte(`{"spec":{"isDeleted":true}}`)
		if _, err := c.In(k8s.DBClusterGVR).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil {
			return fmt.Errorf("requesting deletion of %q: %w", name, c.FormatErr(err))
		}
	}

	if err := c.In(k8s.DBClusterGVR).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return c.FormatErr(err)
	}
	fmt.Fprintf(out, "Deleting cluster %q in namespace %q.\n", name, c.Namespace)

	summary := deletedCluster{Name: name, Namespace: c.Namespace, Secret: secretName, Waited: wait}
	if keepSecret {
		summary.SecretAction = "kept"
		fmt.Fprintf(out, "Kept the admin secret %q.\n", secretName)
	} else {
		// A missing secret is not a failure: the cluster may have been created
		// by hand, or a previous delete may have removed it already.
		switch err := c.Typed.CoreV1().Secrets(c.Namespace).Delete(ctx, secretName, metav1.DeleteOptions{}); {
		case err == nil:
			summary.SecretAction = "deleted"
			fmt.Fprintf(out, "Deleted the admin secret %q.\n", secretName)
		case apierrors.IsNotFound(err):
			summary.SecretAction = "absent"
			fmt.Fprintf(out, "No admin secret %q to delete.\n", secretName)
		default:
			return c.FormatErr(err)
		}
	}

	gone, err := waitForClusterGoneIf(cmd, opts, c, name, wait)
	if err != nil {
		return err
	}
	summary.Gone = gone
	if !opts.Structured() {
		return nil
	}
	return output.Print(cmd.OutOrStdout(), opts.Output, output.Table{}, summary)
}

// deletedCluster is what delete did, for -o json/yaml. Gone is true only when
// --wait actually saw the object disappear.
type deletedCluster struct {
	Name         string `json:"name"`
	Namespace    string `json:"namespace"`
	Secret       string `json:"secret"`
	SecretAction string `json:"secretAction"` // deleted, kept or absent
	Waited       bool   `json:"waited"`
	Gone         bool   `json:"gone"`
}

// waitForClusterGoneIf blocks until the DBCluster is really gone, when --wait
// was passed. "Gone" is a NotFound Get rather than a status.phase: the object
// lingers with a deletionTimestamp while the operator's finalizers remove the
// instances and their disks.
//
// internal/wait is deliberately not used here: its package doc says a Deleted
// event is treated as an error and that waiting for an object to disappear
// needs a different helper.
func waitForClusterGoneIf(cmd *cobra.Command, opts *GlobalOptions, c *k8s.Clients, name string, wait bool) (bool, error) {
	out := opts.Prose(cmd)
	if !wait {
		fmt.Fprintf(out, "Not waiting for the operator to finish; check with: adbo status %s\n", name)
		return false, nil
	}

	ctx, cancel := opts.WithTimeout(cmd.Context())
	defer cancel()
	for {
		_, err := c.In(k8s.DBClusterGVR).Get(ctx, name, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			fmt.Fprintf(out, "Cluster %q is gone.\n", name)
			return true, nil
		case ctx.Err() != nil:
			// --timeout is the only deadline in play, so anything else is a
			// Ctrl+C: reporting a timeout there would be the wrong story.
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return false, ctx.Err()
			}
			return false, fmt.Errorf("timed out after %s waiting for cluster %q to finish deleting; the operator may still be running finalizers (kubectl get dbcluster %s -n %s)",
				opts.Timeout, name, name, c.Namespace)
		case err != nil:
			return false, c.FormatErr(err)
		}

		select {
		case <-ctx.Done():
			// Round the loop once more so the switch above says why we stopped.
		case <-time.After(deleteGonePollInterval):
		}
	}
}

// dryRunDelete validates what it can without changing anything, and without
// prompting, since nothing is at stake.
//
// It cannot validate the DELETE itself. Omni's webhook refuses a DELETE while
// spec.isDeleted is false, and the patch that would set it is not persisted on
// a dry run -- so sending the DELETE would only produce a denial for the wrong
// reason. The patch is validated instead, and the gap is reported.
func dryRunDelete(ctx context.Context, cmd *cobra.Command, c *k8s.Clients, name, secretName string, keepSecret bool) error {
	out := cmd.OutOrStdout()

	patch := []byte(`{"spec":{"isDeleted":true}}`)
	if _, err := c.In(k8s.DBClusterGVR).Patch(ctx, name, types.MergePatchType, patch,
		metav1.PatchOptions{DryRun: []string{metav1.DryRunAll}}); err != nil {
		return fmt.Errorf("requesting deletion of %q: %w", name, c.FormatErr(err))
	}

	fmt.Fprintf(out, "Dry run: cluster %q exists and the spec.isDeleted patch is valid; nothing was changed.\n", name)
	fmt.Fprint(out, "The delete itself cannot be validated yet: the operator's webhook only accepts it once\n"+
		"that patch has actually been applied.\n")
	if keepSecret {
		fmt.Fprintf(out, "The admin secret %q would be kept.\n", secretName)
	} else {
		fmt.Fprintf(out, "The admin secret %q would also be deleted.\n", secretName)
	}
	return nil
}
