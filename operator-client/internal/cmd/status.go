package cmd

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

func newStatusCommand(opts *GlobalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "status [CLUSTER_NAME]",
		Short: "Show a cluster's health, including failure reasons kubectl doesn't show",
		Long: "Show one cluster's health, including the criticalIncidents the\n" +
			"operator records but `kubectl get dbcluster` does not display.\n" +
			"With no CLUSTER_NAME, reports on the cluster chosen by\n" +
			"`adbo cluster select`.",
		Example: `  adbo status my-db
  adbo status                 # the selected cluster
  adbo status my-db -o yaml`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			return runStatus(cmd, opts, name)
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
		// Only a 404 for the object itself means a missing cluster. On a
		// cluster with no Omni CRDs every Get answers 404, and FormatErr says
		// that; claiming a bad name here would hide it.
		//
		// Deliberately not wrapping err: k8s.Format in main would otherwise
		// prepend its own generic "not found" to this message.
		if k8s.IsObjectNotFound(err) {
			return fmt.Errorf("cluster %q not found%s; use -n to look in another namespace", name, c.Where())
		}
		return c.FormatErr(err)
	}

	// Scripts get the raw object.
	if opts.Output == output.OutputJSON || opts.Output == output.OutputYAML {
		return output.Print(out, opts.Output, output.Table{}, db.Object)
	}

	st := readStatusView(db)

	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for _, row := range [][2]string{
		{"Cluster:", fmt.Sprintf("%s (namespace %s)", name, c.Namespace)},
		{"Phase:", st.phase},
		{"State:", st.state},
		{"Version:", st.version},
		{"Endpoint:", st.endpoint},
		{"Resources:", st.resources},
		{"HA:", st.ha},
	} {
		fmt.Fprintf(w, "%s\t%s\n", row[0], row[1])
	}
	if err := w.Flush(); err != nil {
		return err
	}

	// criticalIncidents holds the real failure reason, and `kubectl get
	// dbcluster` does not show it. That is the point of this command.
	incidents := criticalIncidentMessages(db)
	if len(incidents) > 0 {
		fmt.Fprintf(out, "\nCritical incidents (%d):\n", len(incidents))
		for _, msg := range incidents {
			fmt.Fprintf(out, "  - %s\n", msg)
		}
		return nil
	}

	switch {
	case st.stopped:
		// Stopped clusters are not ready on purpose, so the provisioning hint
		// would be actively misleading here.
		fmt.Fprintf(out, "\nThis cluster is stopped, not broken. Start it with: adbo cluster start %s\n", name)
	case st.phase != clusterReadyPhase:
		fmt.Fprintf(out, "\nNot ready yet, and the operator reports no incidents (likely still provisioning).\n"+
			"Watch pods with: kubectl get pods -n %s -w\n", c.Namespace)
	}
	return nil
}

// statusView is the display-ready view of a DBCluster: every field is
// already formatted, with "-" standing in for anything the operator has not
// filled in yet.
type statusView struct {
	phase     string
	state     string
	version   string
	endpoint  string
	resources string
	ha        string
	stopped   bool
}

func readStatusView(db *unstructured.Unstructured) statusView {
	str := func(path ...string) string {
		v, _, _ := unstructured.NestedString(db.Object, path...)
		return v
	}
	// Quantities come back as either numbers or strings depending on how the
	// cluster was written, so format whatever is there.
	quantity := func(path ...string) string {
		v, found, _ := unstructured.NestedFieldNoCopy(db.Object, path...)
		if !found || v == nil {
			return "?"
		}
		return fmt.Sprint(v)
	}
	orDash := func(v string) string {
		if v == "" {
			return "-"
		}
		return v
	}

	st := statusView{
		phase:    orDash(str("status", "phase")),
		endpoint: orDash(primaryEndpoint(db)),
		state:    "running",
		ha:       "off (no standbys)",
	}

	// What is running beats what was asked for; they differ during an upgrade.
	version := str("status", "primary", "currentDatabaseVersion")
	if version == "" {
		version = str("spec", "databaseVersion")
	}
	st.version = orDash(version)

	if stopped, _, _ := unstructured.NestedBool(db.Object, "spec", "primarySpec", "isStopped"); stopped {
		st.stopped, st.state = true, "stopped"
	}

	disk := dataDiskSize(db)
	st.resources = fmt.Sprintf("%s CPU, %s memory, %s disk",
		quantity("spec", "primarySpec", "resources", "cpu"),
		quantity("spec", "primarySpec", "resources", "memory"), disk)

	if n, found, _ := unstructured.NestedInt64(db.Object, "spec", "availability", "numberOfStandbys"); found && n > 0 {
		noun := "standbys"
		if n == 1 {
			noun = "standby"
		}
		st.ha = fmt.Sprintf("%d %s", n, noun)
		if af, found, _ := unstructured.NestedBool(db.Object, "spec", "availability", "enableAutoFailover"); found && !af {
			st.ha += ", auto-failover off"
		}
		// Configured is not the same as working: a restarted cluster is
		// DBClusterReady while its standbys are still catching up, and until
		// HAReady goes True a failover could lose data. Say so where the user
		// is already looking for it.
		if status, reason := haReadyCondition(db); status != "True" {
			st.ha += ", " + haNotReady(status, reason)
		}
	}
	return st
}

// haReadyConditionType is the condition the operator sets once the standbys
// have caught up and a failover would not lose data. It is reported there
// rather than in status.phase, which stays DBClusterReady throughout.
const haReadyConditionType = "HAReady"

// haReadyCondition reads the HAReady condition's status and reason, both empty
// when the operator has not posted the condition at all -- which is the normal
// state of a cluster with no standbys.
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

// haNotReady describes an HAReady condition that is not True.
func haNotReady(status, reason string) string {
	switch {
	case reason != "":
		return fmt.Sprintf("HA not ready (%s)", reason)
	case status == "":
		return "HA readiness not reported yet"
	default:
		return "HA not ready"
	}
}

// primaryEndpoint prefers status.primary.endpoints, the list that replaced the
// single status.primary.endpoint the CRD now marks deprecated. The old field
// is still read, for operators that have not caught up.
func primaryEndpoint(db *unstructured.Unstructured) string {
	endpoints, _, _ := unstructured.NestedSlice(db.Object, "status", "primary", "endpoints")
	for _, e := range endpoints {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		if v, _ := m["value"].(string); v != "" {
			return v
		}
	}
	if url, _, _ := unstructured.NestedString(db.Object, "status", "primary", "url"); url != "" {
		return url
	}
	v, _, _ := unstructured.NestedString(db.Object, "status", "primary", "endpoint")
	return v
}

// dataDiskSize is the size of the disk holding the database files. The disks
// are a list and DataDisk is not promised to be first, so match it by name
// rather than reporting whichever disk the operator happened to write first.
func dataDiskSize(db *unstructured.Unstructured) string {
	disks, _, _ := unstructured.NestedSlice(db.Object, "spec", "primarySpec", "resources", "disks")
	for _, d := range disks {
		m, ok := d.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := m["name"].(string); name != defaultDataDiskName {
			continue
		}
		// Same guard as quantity: an absent size must not print as "<nil>".
		if m["size"] != nil {
			return fmt.Sprint(m["size"])
		}
	}
	return "?"
}
