package cmd

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// clusterReadyPhase is status.phase on a DBCluster the operator considers up.
const clusterReadyPhase = "DBClusterReady"

func newStatusCommand(opts *GlobalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status CLUSTER_NAME",
		Short: "Show a cluster's health, including failure reasons kubectl doesn't show",
		Example: `  adbo status my-db
  adbo status my-db -o yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, opts, args[0])
		},
	}
}

func runStatus(cmd *cobra.Command, opts *GlobalOptions, name string) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	c, err := opts.Clients()
	if err != nil {
		return err
	}
	db, err := c.In(k8s.DBClusterGVR).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Deliberately not wrapping err: k8s.Format in main would otherwise
			// prepend its own generic "not found" to this message.
			return fmt.Errorf("cluster %q not found in namespace %q (context %q); use -n to look in another namespace",
				name, c.Namespace, c.Context)
		}
		return c.FormatErr(err)
	}

	// Scripts get the raw object.
	if opts.Output == output.OutputJSON || opts.Output == output.OutputYAML {
		return output.Print(out, opts.Output, output.Table{}, db.Object)
	}

	str := func(path ...string) string {
		v, _, _ := unstructured.NestedString(db.Object, path...)
		return v
	}
	field := func(path ...string) string {
		v, found, _ := unstructured.NestedFieldNoCopy(db.Object, path...)
		if !found || v == nil {
			return "?"
		}
		return fmt.Sprint(v)
	}
	orDash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}

	phase := str("status", "phase")

	version := str("status", "primary", "currentDatabaseVersion")
	if version == "" {
		version = str("spec", "databaseVersion")
	}

	state := "running"
	if stopped, _, _ := unstructured.NestedBool(db.Object, "spec", "primarySpec", "isStopped"); stopped {
		state = "stopped"
	}

	disk := "?"
	if disks, _, _ := unstructured.NestedSlice(db.Object, "spec", "primarySpec", "resources", "disks"); len(disks) > 0 {
		if d, ok := disks[0].(map[string]any); ok {
			disk = fmt.Sprint(d["size"])
		}
	}
	resources := fmt.Sprintf("%s CPU, %s memory, %s disk",
		field("spec", "primarySpec", "resources", "cpu"), field("spec", "primarySpec", "resources", "memory"), disk)

	ha := "off (no standbys)"
	if n, found, _ := unstructured.NestedInt64(db.Object, "spec", "availability", "numberOfStandbys"); found && n > 0 {
		ha = fmt.Sprintf("%d standby", n)
		if af, found, _ := unstructured.NestedBool(db.Object, "spec", "availability", "enableAutoFailover"); found && !af {
			ha += ", auto-failover off"
		}
	}

	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for _, row := range [][2]string{
		{"Cluster:", fmt.Sprintf("%s (namespace %s)", name, c.Namespace)},
		{"Phase:", orDash(phase)},
		{"State:", state},
		{"Version:", orDash(version)},
		{"Endpoint:", orDash(str("status", "primary", "endpoint"))},
		{"Resources:", resources},
		{"HA:", ha},
	} {
		fmt.Fprintf(w, "%s\t%s\n", row[0], row[1])
	}
	if err := w.Flush(); err != nil {
		return err
	}

	// criticalIncidents holds the real failure reason; `kubectl get dbcluster` doesn't show it.
	incidents, _, _ := unstructured.NestedSlice(db.Object, "status", "criticalIncidents")
	if len(incidents) == 0 {
		if phase != clusterReadyPhase {
			fmt.Fprintf(out, "\nNot ready yet, and the operator reports no incidents (likely still provisioning).\nWatch pods with: kubectl get pods -n %s -w\n", c.Namespace)
		}
		return nil
	}
	fmt.Fprintf(out, "\nCritical incidents (%d):\n", len(incidents))
	for _, it := range incidents {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		msg, _ := m["message"].(string)
		if code, ok := m["code"].(string); ok && code != "" {
			msg = "[" + code + "] " + msg
		}
		fmt.Fprintf(out, "  - %s\n", msg)
	}
	return nil
}
