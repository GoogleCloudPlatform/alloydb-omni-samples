package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"
)

// Omni backup phases (status.phase). A Backup CR is created immediately;
// the operator later sets one of these.
const (
	backupPhaseSucceeded = "Succeeded"
	backupPhaseFailed    = "Failed"
	backupPlanPhaseReady = "Ready"
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
		backupName string
		paused     bool
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
			if backupName != "" {
				if errs := validation.NameIsDNSSubdomain(backupName, false); len(errs) > 0 {
					return fmt.Errorf("--name %q is not a valid DNS subdomain: %s", backupName, errs[0])
				}
			}
			cluster, _, err := prepareCluster(opts, args)
			if err != nil {
				return err
			}
			return runBackupTrigger(cmd, opts, triggerOpts{
				cluster:    cluster,
				backupType: backupType,
				planName:   planName,
				name:       backupName,
				paused:     paused,
				wait:       waitFlags.Enabled(),
			})
		},
	}
	waitFlags = opts.AddWaitFlags(cmd)
	cmd.Flags().StringVar(&backupType, "type", "full", "backup type: full, diff, or incr")
	cmd.Flags().StringVar(&planName, "plan", "", "BackupPlan name (required when the cluster has more than one)")
	cmd.Flags().StringVar(&backupName, "name", "", "Backup name (default: generate a unique name)")
	cmd.Flags().BoolVar(&paused, "paused", false, "create the default BackupPlan paused, so scheduled backups stay off")
	return cmd
}

type triggerOpts struct {
	cluster    string
	backupType string
	planName   string
	name       string
	paused     bool
	wait       bool
}

func runBackupTrigger(cmd *cobra.Command, opts *GlobalOptions, in triggerOpts) error {
	switch in.backupType {
	case "full", "diff", "incr": // CRD enum: backup.spec.physicalBackupSpec.backupType
	default:
		return fmt.Errorf("--type must be full, diff, or incr")
	}
	if in.name != "" {
		if errs := validation.NameIsDNSSubdomain(in.name, false); len(errs) > 0 {
			return fmt.Errorf("--name %q is not a valid DNS subdomain: %s", in.name, errs[0])
		}
	}

	ctx := cmd.Context()
	c, err := opts.Clients()
	if err != nil {
		return err
	}

	// FormatErr distinguishes a missing DBCluster from a missing CRD
	// ("the server could not find the requested resource") and omits an
	// empty context. A local IsNotFound check hides both.
	cluster, err := c.In(k8s.DBClusterGVR).Get(ctx, in.cluster, metav1.GetOptions{})
	if err != nil {
		return c.FormatErr(err)
	}
	if err := clusterReadyForBackup(cluster, in.cluster); err != nil {
		return err
	}

	plan, err := resolveBackupPlan(ctx, c, in.cluster, in.planName, in.paused, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	if err := waitForBackupPlan(ctx, cmd, opts, c, plan); err != nil {
		return err
	}

	created, err := c.In(k8s.BackupGVR).Create(ctx, newManualBackup(c.Namespace, in.cluster, plan, in.backupType, in.name), metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating backup: %w", c.FormatErr(err))
	}
	// Progress stays on stderr so -o json|yaml on stdout is a single Backup object.
	fmt.Fprintf(cmd.ErrOrStderr(), "backup %s created in namespace %q\n", created.GetName(), c.Namespace)
	if !in.wait {
		return printBackup(ctx, cmd.OutOrStdout(), opts.Output, c, created)
	}
	return waitForBackup(ctx, cmd, opts, c, in.cluster, created.GetName())
}

// clusterReadyForBackup fails before a BackupPlan or Backup is created.
// A stopped or deleting cluster accepts the Backup CR then deletes it,
// which wait reports as "was deleted while waiting".
func clusterReadyForBackup(cluster *unstructured.Unstructured, name string) error {
	if cluster.GetDeletionTimestamp() != nil {
		return fmt.Errorf("cluster %q is being deleted", name)
	}
	if deleted, _, _ := unstructured.NestedBool(cluster.Object, "spec", "isDeleted"); deleted {
		return fmt.Errorf("cluster %q is marked deleted (spec.isDeleted)", name)
	}
	if stopped, _, _ := unstructured.NestedBool(cluster.Object, "spec", "primarySpec", "isStopped"); stopped {
		return fmt.Errorf("cluster %q is stopped; start it before taking a backup", name)
	}
	if phase := wait.Phase(cluster); phase == "DBClusterStopped" {
		return fmt.Errorf("cluster %q is %s; start it before taking a backup", name, phase)
	}
	return nil
}

func newManualBackup(namespace, cluster, plan, backupType, name string) *unstructured.Unstructured {
	meta := map[string]any{"namespace": namespace}
	if name != "" {
		meta["name"] = name
	} else {
		// generateName lets the API append a suffix so two triggers do not collide.
		meta["generateName"] = cluster + "-backup-"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": k8s.APIVersion,
		"kind":       "Backup",
		"metadata":   meta,
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

func waitForBackupPlan(ctx context.Context, cmd *cobra.Command, opts *GlobalOptions, c *k8s.Clients, name string) error {
	waitCtx, cancel := opts.WithTimeout(ctx)
	defer cancel()
	_, err := wait.ForPhaseWatch(waitCtx, c, wait.Options{
		GVR:     k8s.BackupPlanGVR,
		Name:    name,
		Success: []string{backupPlanPhaseReady},
		Failure: []string{backupPhaseFailed},
		OnChange: func(phase string) {
			fmt.Fprintf(cmd.ErrOrStderr(), "  plan %s: %s\n", name, phase)
		},
	})
	if err != nil {
		var failed *wait.FailedError
		switch {
		case errors.As(err, &failed):
			return fmt.Errorf("backup plan %s failed: %s", failed.Name, failed.Reason)
		case errors.Is(err, context.DeadlineExceeded):
			return fmt.Errorf("backup plan %s did not become Ready in time: %w", name, err)
		default:
			return fmt.Errorf("waiting for backup plan %s: %w", name, c.FormatErr(err))
		}
	}
	return nil
}

func waitForBackup(ctx context.Context, cmd *cobra.Command, opts *GlobalOptions, c *k8s.Clients, clusterName, name string) error {
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
		// The wait context is already expired here. Use a fresh one for events.
		OnTimeout: func(*unstructured.Unstructured) string {
			evCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return recentEvents(evCtx, c, c.Namespace, name, clusterName)
		},
	})
	if err != nil {
		var failed *wait.FailedError
		switch {
		case errors.As(err, &failed):
			return backupFailedError(ctx, c, failed)
		case errors.Is(err, context.DeadlineExceeded):
			return fmt.Errorf("backup %s did not finish in time: %w", name, err)
		default:
			return fmt.Errorf("waiting for backup %s: %w", name, c.FormatErr(err))
		}
	}
	return printBackup(ctx, cmd.OutOrStdout(), opts.Output, c, done)
}

func backupFailedError(ctx context.Context, c *k8s.Clients, failed *wait.FailedError) error {
	id := ""
	if obj, err := c.In(k8s.BackupGVR).Get(ctx, failed.Name, metav1.GetOptions{}); err == nil {
		id = backupID(obj)
	}
	if id != "" {
		return fmt.Errorf("backup %s (%s) failed: %s", failed.Name, id, failed.Reason)
	}
	return fmt.Errorf("backup %s failed: %s", failed.Name, failed.Reason)
}

// recentEvents returns a few Event messages for the named objects.
// A missing typed client or API error yields an empty string so the timeout
// still reports the phase.
func recentEvents(ctx context.Context, c *k8s.Clients, namespace string, names ...string) string {
	if c == nil || c.Typed == nil || namespace == "" {
		return ""
	}
	want := map[string]struct{}{}
	for _, name := range names {
		if name != "" {
			want[name] = struct{}{}
		}
	}
	if len(want) == 0 {
		return ""
	}
	list, err := c.Typed.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return ""
	}
	var lines []string
	// The list is oldest first. Walk backwards so a timeout shows the newest events.
	for i := len(list.Items) - 1; i >= 0; i-- {
		ev := &list.Items[i]
		if _, ok := want[ev.InvolvedObject.Name]; !ok {
			continue
		}
		msg := strings.TrimSpace(ev.Reason + " " + ev.Message)
		if msg == "" {
			continue
		}
		lines = append(lines, msg)
		if len(lines) == 3 {
			break
		}
	}
	return strings.Join(lines, "; ")
}

// printBackup emits the Backup. Table output matches `backup list`;
// json and yaml emit the object itself so -o is not ignored.
func printBackup(ctx context.Context, w io.Writer, format string, c *k8s.Clients, obj *unstructured.Unstructured) error {
	table := output.Table{
		Headers: backupTableHeaders,
		Rows:    [][]string{backupTableRow(obj, backupStorage(ctx, c, obj))},
	}
	return output.Print(w, format, table, obj)
}

// resolveBackupPlan picks the BackupPlan for this cluster:
//  1. --plan if the user passed one
//  2. the one existing plan whose spec.dbclusterRef matches
//  3. create <cluster>-backupplan (paused only when --paused is set)
//
// More than one matching plan is an error so the first list item is never chosen.
func resolveBackupPlan(ctx context.Context, c *k8s.Clients, cluster, planName string, paused bool, errOut io.Writer) (string, error) {
	if planName != "" {
		planObj, err := c.In(k8s.BackupPlanGVR).Get(ctx, planName, metav1.GetOptions{})
		if err != nil {
			return "", c.FormatErr(err)
		}
		if ref := dbclusterRef(planObj); ref != cluster {
			return "", fmt.Errorf("backup plan %q is for cluster %q, not %q", planObj.GetName(), ref, cluster)
		}
		return planObj.GetName(), nil
	}

	list, err := c.In(k8s.BackupPlanGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("listing backup plans: %w", c.FormatErr(err))
	}
	var matches []string
	for i := range list.Items {
		if dbclusterRef(&list.Items[i]) == cluster {
			matches = append(matches, list.Items[i].GetName())
		}
	}
	switch len(matches) {
	case 0:
		// fall through to create
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("cluster %q has more than one backup plan (%s); pass --plan", cluster, strings.Join(matches, ", "))
	}

	plan := defaultBackupPlan(c.Namespace, cluster, paused)
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
	if paused {
		fmt.Fprintf(errOut, "created paused backup plan %s for %s (Sunday full, daily incremental, retain 14 days)\n", created.GetName(), cluster)
	} else {
		fmt.Fprintf(errOut, "created backup plan %s for %s (Sunday full, daily incremental, retain 14 days)\n", created.GetName(), cluster)
	}
	return created.GetName(), nil
}

func defaultBackupPlan(namespace, cluster string, paused bool) *unstructured.Unstructured {
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
			"paused":           paused,
			"backupSchedules": map[string]any{
				"full":        "0 0 * * 0",
				"incremental": "0 21 * * *",
			},
		},
	}}
}
