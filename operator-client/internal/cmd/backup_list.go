package cmd

import (
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"
)

// newBackupListCommand is `adbo backup list [CLUSTER_NAME]`.
func newBackupListCommand(opts *GlobalOptions) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:     "list [CLUSTER_NAME]",
		Short:   "List backups of a cluster",
		Example: "  adbo backup list my-db\n  adbo backup list my-db -o json",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cluster, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			return runBackupList(cmd, opts, cluster, limit)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "show at most this many backups")
	return cmd
}

func runBackupList(cmd *cobra.Command, opts *GlobalOptions, cluster string, limit int) error {
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

	mine := backupsForCluster(list.Items, cluster, limit)
	table := output.Table{Headers: []string{"NAME", "PHASE", "AGE"}}
	for i := range mine {
		b := &mine[i] // index, not range copy: Unstructured methods need a pointer
		table.Rows = append(table.Rows, []string{
			b.GetName(),
			wait.Phase(b),
			duration.HumanDuration(time.Since(b.GetCreationTimestamp().Time)),
		})
	}
	return output.Print(cmd.OutOrStdout(), opts.Output, table, mine)
}

func backupsForCluster(items []unstructured.Unstructured, cluster string, limit int) []unstructured.Unstructured {
	var mine []unstructured.Unstructured
	for i := range items {
		if dbclusterRef(&items[i]) == cluster {
			mine = append(mine, items[i])
		}
	}
	sort.Slice(mine, func(i, j int) bool {
		return mine[i].GetCreationTimestamp().After(mine[j].GetCreationTimestamp().Time)
	})
	if len(mine) > limit {
		mine = mine[:limit]
	}
	return mine
}
