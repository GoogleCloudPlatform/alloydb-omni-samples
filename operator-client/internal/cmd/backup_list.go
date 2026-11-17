package cmd

import (
	"context"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"
)

// backupTableHeaders are the columns for `backup list` and `backup trigger`.
var backupTableHeaders = []string{"NAME", "ID", "TYPE", "PHASE", "STARTED", "COMPLETED", "STORAGE"}

// newBackupListCommand is `adbo backup list [CLUSTER_NAME]`.
func newBackupListCommand(opts *GlobalOptions) *cobra.Command {
	var (
		limit    int
		planName string
	)
	cmd := &cobra.Command{
		Use:     "list [CLUSTER_NAME]",
		Short:   "List backups of a cluster",
		Example: "  adbo backup list my-db\n  adbo backup list my-db --plan my-db-backupplan\n  adbo backup list my-db -o json",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cluster, _, err := prepareCluster(opts, args)
			if err != nil {
				return err
			}
			return runBackupList(cmd, opts, cluster, planName, limit)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "show at most this many backups")
	cmd.Flags().StringVar(&planName, "plan", "", "only backups that reference this BackupPlan")
	return cmd
}

func runBackupList(cmd *cobra.Command, opts *GlobalOptions, cluster, planName string, limit int) error {
	if limit <= 0 {
		return fmt.Errorf("--limit must be greater than 0")
	}

	ctx := cmd.Context()
	c, err := opts.Clients()
	if err != nil {
		return err
	}

	// Backup has no server-side filter on dbclusterRef, so list the
	// namespace and keep matching spec.dbclusterRef client-side.
	list, err := c.In(k8s.BackupGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return c.FormatErr(err)
	}

	mine := backupsForCluster(list.Items, cluster, planName, limit)
	if opts.Output == output.OutputJSON || opts.Output == output.OutputYAML {
		return output.Print(cmd.OutOrStdout(), opts.Output, output.Table{}, mine)
	}

	plans, err := backupPlansByName(ctx, c)
	if err != nil {
		return err
	}
	table := output.Table{Headers: backupTableHeaders}
	for i := range mine {
		b := &mine[i] // index, not range copy: Unstructured methods need a pointer
		table.Rows = append(table.Rows, backupTableRow(b, storageTarget(plans[backupPlanRef(b)])))
	}
	if err := output.Print(cmd.OutOrStdout(), opts.Output, table, nil); err != nil {
		return err
	}
	for _, line := range planSummaries(mine, plans) {
		fmt.Fprintln(cmd.OutOrStdout(), line)
	}
	return nil
}

func backupTableRow(b *unstructured.Unstructured, storage string) []string {
	return []string{
		b.GetName(),
		backupID(b),
		physicalBackupType(b),
		wait.Phase(b),
		statusTime(b, "createTime"),
		statusTime(b, "completeTime"),
		storage,
	}
}

func backupsForCluster(items []unstructured.Unstructured, cluster, planName string, limit int) []unstructured.Unstructured {
	var mine []unstructured.Unstructured
	for i := range items {
		if dbclusterRef(&items[i]) != cluster {
			continue
		}
		if planName != "" && backupPlanRef(&items[i]) != planName {
			continue
		}
		mine = append(mine, items[i])
	}
	sort.Slice(mine, func(i, j int) bool {
		return mine[i].GetCreationTimestamp().After(mine[j].GetCreationTimestamp().Time)
	})
	if len(mine) > limit {
		mine = mine[:limit]
	}
	return mine
}

func backupPlansByName(ctx context.Context, c *k8s.Clients) (map[string]*unstructured.Unstructured, error) {
	list, err := c.In(k8s.BackupPlanGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing backup plans: %w", c.FormatErr(err))
	}
	out := make(map[string]*unstructured.Unstructured, len(list.Items))
	for i := range list.Items {
		out[list.Items[i].GetName()] = &list.Items[i]
	}
	return out, nil
}

// storageTarget is spec.backupLocation.type. The CRD enum is GCS or S3.
// A plan with no location stores the backup on local disk.
func storageTarget(plan *unstructured.Unstructured) string {
	if plan == nil {
		return "Local"
	}
	t, _, _ := unstructured.NestedString(plan.Object, "spec", "backupLocation", "type")
	if t == "" {
		return "Local"
	}
	return t
}

func backupStorage(ctx context.Context, c *k8s.Clients, obj *unstructured.Unstructured) string {
	ref := backupPlanRef(obj)
	if ref == "" || c == nil {
		return "Local"
	}
	plan, err := c.In(k8s.BackupPlanGVR).Get(ctx, ref, metav1.GetOptions{})
	if err != nil {
		return "Local"
	}
	return storageTarget(plan)
}

// planSummaries is one line per BackupPlan referenced by the listed backups.
// JSON and YAML output skip this; they print the Backup objects only.
func planSummaries(backups []unstructured.Unstructured, plans map[string]*unstructured.Unstructured) []string {
	var lines []string
	seen := map[string]bool{}
	for i := range backups {
		name := backupPlanRef(&backups[i])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		plan := plans[name]
		if plan == nil {
			continue
		}
		lines = append(lines, planSummary(plan))
	}
	return lines
}

func planSummary(plan *unstructured.Unstructured) string {
	full, _, _ := unstructured.NestedString(plan.Object, "spec", "backupSchedules", "full")
	incr, _, _ := unstructured.NestedString(plan.Object, "spec", "backupSchedules", "incremental")
	begin, _, _ := unstructured.NestedString(plan.Object, "status", "recoveryWindow", "begin")
	end, _, _ := unstructured.NestedString(plan.Object, "status", "recoveryWindow", "end")
	return fmt.Sprintf("plan %s: full %s, incremental %s, retain %d days, recovery %s..%s",
		plan.GetName(), dash(full), dash(incr), nestedInt64(plan, "spec", "backupRetainDays"), dash(begin), dash(end))
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func nestedInt64(obj *unstructured.Unstructured, path ...string) int64 {
	v, found, _ := unstructured.NestedFieldNoCopy(obj.Object, path...)
	if !found || v == nil {
		return 0
	}
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}
