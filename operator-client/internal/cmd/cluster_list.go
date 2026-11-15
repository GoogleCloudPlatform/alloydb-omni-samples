package cmd

import (
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

func newClusterListCommand(opts *GlobalOptions) *cobra.Command {
	var allNamespaces bool

	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List database clusters",
		Example: "  adbo cluster list\n  adbo cluster list -A\n  adbo cluster list -n prod -o json",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClusterList(cmd, opts, allNamespaces)
		},
	}

	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "list clusters across all namespaces")
	return cmd
}

func runClusterList(cmd *cobra.Command, opts *GlobalOptions, allNamespaces bool) error {
	c, err := opts.Clients()
	if err != nil {
		return err
	}

	ns := c.Namespace
	if allNamespaces {
		ns = metav1.NamespaceAll
	}
	list, err := c.InNamespace(k8s.DBClusterGVR, ns).List(cmd.Context(), metav1.ListOptions{})
	if err != nil {
		// Report where we looked: with -A that is every namespace, not c.Namespace.
		return k8s.Format(err, ns, c.Context)
	}

	items := list.Items
	sort.Slice(items, func(i, j int) bool {
		if items[i].GetNamespace() != items[j].GetNamespace() {
			return items[i].GetNamespace() < items[j].GetNamespace()
		}
		return items[i].GetName() < items[j].GetName()
	})

	var headers []string
	if allNamespaces {
		headers = append(headers, "NAMESPACE")
	}
	headers = append(headers, "NAME")
	for _, col := range clusterStatusColumns {
		headers = append(headers, strings.ToUpper(col))
	}
	headers = append(headers, "AGE")
	table := output.Table{Headers: headers}
	for i := range items {
		db := &items[i]
		var row []string
		if allNamespaces {
			row = append(row, db.GetNamespace())
		}
		row = append(row, db.GetName())
		row = append(row, clusterStatus(db)...)
		row = append(row, duration.HumanDuration(time.Since(db.GetCreationTimestamp().Time)))
		table.Rows = append(table.Rows, row)
	}
	return output.Print(cmd.OutOrStdout(), opts.Output, table, items)
}

// clusterStatusColumns label the values from clusterStatus. They mirror the
// operator's `kubectl get dbclusters` printer columns and are shared by
// `cluster list` (as table headers) and `cluster select -v`.
var clusterStatusColumns = []string{"Phase", "Primary", "Endpoint", "Version", "HA"}

func clusterStatus(db *unstructured.Unstructured) []string {
	return []string{
		k8s.StringField(db, "status", "phase"),
		k8s.StringField(db, "status", "primary", "phase"),
		// Match `cluster describe`: prefer url (<ip>:<port>) over the
		// deprecated endpoint, which older operators still set.
		k8s.FirstStringField(db,
			[]string{"status", "primary", "url"},
			[]string{"status", "primary", "endpoint"}),
		// Show the running version so a stuck or in-progress upgrade is
		// visible; fall back to the desired version before provisioning.
		k8s.FirstStringField(db,
			[]string{"status", "primary", "currentDatabaseVersion"},
			[]string{"spec", "databaseVersion"}),
		k8s.ConditionStatus(db, "HAReady"),
	}
}
