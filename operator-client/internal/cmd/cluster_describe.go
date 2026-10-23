package cmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/duration"
	"sigs.k8s.io/yaml"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

const noValue = "<none>"

// Spec and status fields describe shows in their own rows. Everything else
// is printed under "Other Spec" / "Other Status" so nothing is hidden.
var (
	describedSpec = [][]string{
		{"databaseVersion"},
		{"mode"},
		{"availability", "numberOfStandbys"},
		{"availability", "enableAutoFailover"},
		{"availability", "enableAutoHeal"},
		{"availability", "enableStandbyAsReadReplica"},
		{"primarySpec", "resources", "cpu"},
		{"primarySpec", "resources", "memory"},
		{"primarySpec", "resources", "disks"},
		{"primarySpec", "isStopped"},
		{"primarySpec", "adminUser", "passwordRef", "name"},
		{"primarySpec", "parameters"},
	}
	describedStatus = [][]string{
		{"phase"},
		{"observedGeneration"},
		{"conditions"},
		{"criticalIncidents"},
		{"primary", "phase"},
		{"primary", "url"},
		{"primary", "endpoint"},
		{"primary", "currentDatabaseVersion"},
		{"primary", "allocatedResources", "cpu"},
		{"primary", "allocatedResources", "memory"},
		{"primary", "conditions"},
	}
)

func newClusterDescribeCommand(opts *GlobalOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "describe [CLUSTER_NAME]",
		Short: "Show a cluster's spec, status, conditions, incidents, and events",
		Long: "Show a cluster's full spec and status in one view, leading with the condition\n" +
			"messages and critical incidents the operator reports when a cluster is stuck.\n" +
			"Use -o yaml or -o json for the raw DBCluster object.",
		Example: "  adbo cluster describe my-db\n" +
			"  adbo cluster describe my-db -o yaml\n" +
			"  adbo cluster describe            # uses the cluster from `adbo cluster select`",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClusterDescribe(cmd, opts, args)
		},
	}
}

func runClusterDescribe(cmd *cobra.Command, opts *GlobalOptions, args []string) error {
	name, err := opts.ClusterName(args)
	if err != nil {
		return err
	}
	c, err := opts.Clients()
	if err != nil {
		return err
	}
	ctx := cmd.Context()

	obj, err := getDBCluster(ctx, c, name)
	if err != nil {
		return err
	}
	if opts.Output != output.OutputTable && opts.Output != "" {
		return output.Print(cmd.OutOrStdout(), opts.Output, output.Table{}, obj.Object)
	}

	events, err := clusterEvents(ctx, c, name)
	if err != nil {
		// Events help but aren't essential, and RBAC often hides them.
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: could not list events: %v\n", err)
	}
	return describeDBCluster(cmd.OutOrStdout(), obj, events, time.Now())
}

// clusterEvents returns the events recorded against the DBCluster, oldest first.
func clusterEvents(ctx context.Context, c *k8s.Clients, name string) ([]corev1.Event, error) {
	list, err := c.Typed.CoreV1().Events(c.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: fields.Set{
			"involvedObject.kind": "DBCluster",
			"involvedObject.name": name,
		}.String(),
	})
	if err != nil {
		return nil, err
	}
	events := list.Items
	sort.SliceStable(events, func(i, j int) bool {
		return eventTime(events[i]).Before(eventTime(events[j]))
	})
	return events, nil
}

func eventTime(e corev1.Event) time.Time {
	switch {
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	case !e.FirstTimestamp.IsZero():
		return e.FirstTimestamp.Time
	}
	return e.CreationTimestamp.Time
}

func describeDBCluster(out io.Writer, obj *unstructured.Unstructured, events []corev1.Event, now time.Time) error {
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	get := func(path ...string) string { return nestedString(obj.Object, path...) }
	spec := func(path ...string) string { return get(append([]string{"spec"}, path...)...) }
	status := func(path ...string) string { return get(append([]string{"status"}, path...)...) }

	created := obj.GetCreationTimestamp().Time
	describeField(w, "", "Name", obj.GetName())
	describeField(w, "", "Namespace", obj.GetNamespace())
	describeField(w, "", "Created", fmt.Sprintf("%s (%s ago)", created.UTC().Format(time.RFC3339), ageSince(created, now)))
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Spec:")
	describeField(w, "  ", "Database Version", spec("databaseVersion"))
	describeField(w, "  ", "CPU", spec("primarySpec", "resources", "cpu"))
	describeField(w, "  ", "Memory", spec("primarySpec", "resources", "memory"))
	describeField(w, "  ", "Disks", diskSummary(obj.Object, "spec", "primarySpec", "resources", "disks")...)
	describeField(w, "  ", "Standbys", orDefault(spec("availability", "numberOfStandbys"), "0"))
	// The CRD defaults these two to true; read replicas has no default.
	describeField(w, "  ", "Auto Failover", orDefault(spec("availability", "enableAutoFailover"), "true"))
	describeField(w, "  ", "Auto Heal", orDefault(spec("availability", "enableAutoHeal"), "true"))
	describeField(w, "  ", "Read Replicas", orDefault(spec("availability", "enableStandbyAsReadReplica"), "false"))
	describeField(w, "  ", "Stopped", orDefault(spec("primarySpec", "isStopped"), "false"))
	describeField(w, "  ", "Mode", spec("mode"))
	describeField(w, "  ", "Admin Secret", spec("primarySpec", "adminUser", "passwordRef", "name"))
	describeField(w, "  ", "Parameters", parameterList(obj.Object)...)
	fmt.Fprintln(w)
	yamlSection(w, "Other Spec", leftovers(obj.Object, "spec", describedSpec))

	generation := fmt.Sprint(obj.GetGeneration())
	if observed := status("observedGeneration"); observed != "" && observed != generation {
		generation += fmt.Sprintf(" (operator has seen %s; it hasn't processed the latest spec yet)", observed)
	}
	fmt.Fprintln(w, "Status:")
	describeField(w, "  ", "Phase", status("phase"))
	describeField(w, "  ", "Primary Phase", status("primary", "phase"))
	describeField(w, "  ", "Endpoint", orDefault(status("primary", "url"), status("primary", "endpoint")))
	describeField(w, "  ", "Running Version", status("primary", "currentDatabaseVersion"))
	describeField(w, "  ", "Allocated CPU", status("primary", "allocatedResources", "cpu"))
	describeField(w, "  ", "Allocated Memory", status("primary", "allocatedResources", "memory"))
	describeField(w, "  ", "Generation", generation)
	fmt.Fprintln(w)
	yamlSection(w, "Other Status", leftovers(obj.Object, "status", describedStatus))

	var conds [][]string
	conds = append(conds, conditionRows(obj.Object, now, "", "status", "conditions")...)
	conds = append(conds, conditionRows(obj.Object, now, "Primary/", "status", "primary", "conditions")...)
	describeSection(w, "Conditions", []string{"TYPE", "STATUS", "REASON", "AGE", "MESSAGE"}, conds)

	var incidents [][]string
	for _, inc := range nestedMaps(obj.Object, "status", "criticalIncidents") {
		incidents = append(incidents, []string{
			nestedString(inc, "code"),
			timestampAge(nestedString(inc, "createTime"), now),
			oneLine(nestedString(inc, "message")),
		})
		// The stack trace often holds the underlying error.
		for _, st := range nestedMaps(inc, "stackTrace") {
			incidents = append(incidents, []string{"", "", "  " + nestedString(st, "component") + ": " + oneLine(nestedString(st, "message"))})
		}
	}
	describeSection(w, "Critical Incidents", []string{"CODE", "AGE", "MESSAGE"}, incidents)

	var evRows [][]string
	for _, e := range events {
		evRows = append(evRows, []string{
			e.Type, e.Reason, ageSince(eventTime(e), now), orDefault(e.Source.Component, e.ReportingController), oneLine(e.Message),
		})
	}
	describeSection(w, "Events", []string{"TYPE", "REASON", "AGE", "FROM", "MESSAGE"}, evRows)

	return w.Flush()
}

// describeField writes "key: value", putting extra values on their own aligned lines.
func describeField(w io.Writer, indent, key string, values ...string) {
	if len(values) == 0 {
		values = []string{noValue}
	}
	for i, v := range values {
		if v == "" {
			v = noValue
		}
		k := key + ":"
		if i > 0 {
			k = ""
		}
		fmt.Fprintf(w, "%s%s\t%s\n", indent, k, v)
	}
}

// describeSection writes a titled table, or "<none>" when it has no rows.
func describeSection(w io.Writer, title string, headers []string, rows [][]string) {
	if len(rows) == 0 {
		fmt.Fprintf(w, "%s:\t%s\n\n", title, noValue)
		return
	}
	fmt.Fprintf(w, "%s:\n", title)
	fmt.Fprintf(w, "  %s\n", strings.Join(headers, "\t"))
	for _, r := range rows {
		fmt.Fprintf(w, "  %s\n", strings.Join(r, "\t"))
	}
	fmt.Fprintln(w)
}

// yamlSection prints fields as indented YAML, or nothing when there are none.
func yamlSection(w io.Writer, title string, fields map[string]any) {
	if len(fields) == 0 {
		return
	}
	b, err := yaml.Marshal(fields)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "%s:\n", title)
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		fmt.Fprintf(w, "  %s\n", line)
	}
	fmt.Fprintln(w)
}

// leftovers returns a copy of obj[root] without the shown paths, with any
// maps that became empty removed.
func leftovers(obj map[string]any, root string, shown [][]string) map[string]any {
	m, found, err := unstructured.NestedMap(obj, root)
	if !found || err != nil {
		return nil
	}
	for _, path := range shown {
		unstructured.RemoveNestedField(m, path...)
	}
	pruneEmptyMaps(m)
	return m
}

func pruneEmptyMaps(m map[string]any) {
	for k, v := range m {
		if child, ok := v.(map[string]any); ok {
			pruneEmptyMaps(child)
			if len(child) == 0 {
				delete(m, k)
			}
		}
	}
}

func conditionRows(obj map[string]any, now time.Time, prefix string, path ...string) [][]string {
	var rows [][]string
	for _, c := range nestedMaps(obj, path...) {
		rows = append(rows, []string{
			prefix + nestedString(c, "type"),
			nestedString(c, "status"),
			orDefault(nestedString(c, "reason"), noValue),
			timestampAge(nestedString(c, "lastTransitionTime"), now),
			orDefault(oneLine(nestedString(c, "message")), noValue),
		})
	}
	return rows
}

func diskSummary(obj map[string]any, path ...string) []string {
	var out []string
	for _, d := range nestedMaps(obj, path...) {
		s := nestedString(d, "name") + " " + nestedString(d, "size")
		if sc := nestedString(d, "storageClass"); sc != "" {
			s += " (" + sc + ")"
		}
		out = append(out, s)
	}
	return out
}

func parameterList(obj map[string]any) []string {
	params, _, _ := unstructured.NestedMap(obj, "spec", "primarySpec", "parameters")
	var out []string
	for k, v := range params {
		out = append(out, fmt.Sprintf("%s=%v", k, v))
	}
	sort.Strings(out)
	return out
}

// nestedString renders any scalar at path. cpu and memory are int-or-string
// in the CRD, so NestedString alone would miss `cpu: 2`.
func nestedString(obj map[string]any, path ...string) string {
	v, found, err := unstructured.NestedFieldNoCopy(obj, path...)
	if !found || err != nil || v == nil {
		return ""
	}
	switch v.(type) {
	case map[string]any, []any:
		return ""
	}
	return fmt.Sprint(v)
}

func nestedMaps(obj map[string]any, path ...string) []map[string]any {
	items, _, _ := unstructured.NestedSlice(obj, path...)
	var out []map[string]any
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func timestampAge(ts string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return "<unknown>"
	}
	return ageSince(t, now)
}

func ageSince(t, now time.Time) string {
	if t.IsZero() {
		return "<unknown>"
	}
	return duration.HumanDuration(now.Sub(t))
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
