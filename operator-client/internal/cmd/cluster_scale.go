package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

// dataDisk is the DBCluster disk that holds the database files.
const dataDisk = "DataDisk"

var (
	resourcesPath = []string{"spec", "primarySpec", "resources"}
	standbysPath  = []string{"spec", "availability", "numberOfStandbys"}
)

// scaleFlags are the requested changes; empty string / -1 means "leave as is".
type scaleFlags struct {
	cpu, memory, disk string
	standbys          int
	standbysSet       bool // --standbys was passed (0 is a valid value)
}

func newClusterScaleCommand(opts *GlobalOptions) *cobra.Command {
	f := scaleFlags{standbys: -1}

	cmd := &cobra.Command{
		Use:   "scale [CLUSTER_NAME]",
		Short: "Change a cluster's CPU, memory, disk size or number of standbys",
		Long: "Change the compute and storage of a database cluster.\n" +
			"Only the flags you pass are changed. Changing CPU or memory restarts the\n" +
			"database, so adbo shows the change and asks before applying it (skip with -y).\n" +
			"Disks can grow but not shrink.",
		Example: "  adbo cluster scale my-db --cpu 4 --memory 32Gi\n" +
			"  adbo cluster scale my-db --disk 100Gi\n" +
			"  adbo cluster scale --standbys 1 -y",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.standbysSet = cmd.Flags().Changed("standbys")
			name, err := opts.ClusterName(args)
			if err != nil {
				return err
			}
			return runClusterScale(cmd, opts, name, f)
		},
	}

	cmd.Flags().StringVar(&f.cpu, "cpu", "", "CPU cores for each instance, e.g. 2 or 500m")
	cmd.Flags().StringVar(&f.memory, "memory", "", "memory for each instance, e.g. 16Gi")
	cmd.Flags().StringVar(&f.disk, "disk", "", "data disk size, e.g. 50Gi (grow only)")
	cmd.Flags().IntVar(&f.standbys, "standbys", -1, "number of standby instances (0 disables HA)")
	return cmd
}

// change is one row of the "before -> after" summary shown before patching.
type change struct{ field, from, to string }

func runClusterScale(cmd *cobra.Command, opts *GlobalOptions, name string, f scaleFlags) error {
	if f.cpu == "" && f.memory == "" && f.disk == "" && !f.standbysSet {
		return fmt.Errorf("nothing to change: pass at least one of --cpu, --memory, --disk, --standbys")
	}
	c, err := opts.Clients()
	if err != nil {
		return err
	}
	db, err := c.In(k8s.DBClusterGVR).Get(cmd.Context(), name, metav1.GetOptions{})
	if err != nil {
		return c.FormatErr(err)
	}

	changes, patch, err := planScale(db, f)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(changes) == 0 {
		fmt.Fprintf(out, "Cluster %q already has these settings; nothing to do.\n", name)
		return nil
	}

	printChanges(cmd.ErrOrStderr(), name, changes)
	prompt := "Apply these changes?"
	if restartsDatabase(changes) {
		prompt = "Changing CPU or memory restarts the database. Apply?"
	}
	ok, err := opts.Confirm(cmd, prompt)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(out, "Cancelled; nothing changed.")
		return nil
	}

	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	if _, err := c.In(k8s.DBClusterGVR).Patch(cmd.Context(), name, types.MergePatchType, body, metav1.PatchOptions{}); err != nil {
		return c.FormatErr(err)
	}
	fmt.Fprintf(out, "Scaling cluster %q%s. Check progress with `adbo cluster list`.\n",
		name, k8s.Location(c.Namespace, c.Context))
	return nil
}

// planScale validates f against db's current spec and returns what would
// change plus a merge patch containing only those fields. The patch carries
// db's resourceVersion, so if the cluster was edited while the user was at
// the prompt the API server rejects it with a Conflict instead of letting
// the patch overwrite that edit.
func planScale(db *unstructured.Unstructured, f scaleFlags) ([]change, map[string]any, error) {
	var changes []change
	patch := map[string]any{"metadata": map[string]any{"resourceVersion": db.GetResourceVersion()}}

	for _, r := range []struct{ flag, field, value string }{
		{"--cpu", "cpu", f.cpu},
		{"--memory", "memory", f.memory},
	} {
		if r.value == "" {
			continue
		}
		q, err := resource.ParseQuantity(r.value)
		if err != nil || q.Sign() <= 0 {
			return nil, nil, fmt.Errorf("%s %q is not a valid positive size", r.flag, r.value)
		}
		path := append(append([]string{}, resourcesPath...), r.field)
		cur := quantityField(db, path...)
		if cur != "" && sameQuantity(cur, q) {
			continue
		}
		// The CRD accepts cpu as an int or a string; keep whole cores as ints
		// to match what users usually write in YAML.
		var v any = q.String()
		if n, err := strconv.ParseInt(q.String(), 10, 64); err == nil && r.field == "cpu" {
			v = n
		}
		if err := unstructured.SetNestedField(patch, v, path...); err != nil {
			return nil, nil, err
		}
		changes = append(changes, change{r.field, orDash(cur), q.String()})
	}

	if f.disk != "" {
		c, err := planDisk(db, patch, f.disk)
		if err != nil {
			return nil, nil, err
		}
		if c != nil {
			changes = append(changes, *c)
		}
	}

	if f.standbysSet {
		if f.standbys < 0 {
			return nil, nil, fmt.Errorf("--standbys must be 0 or more")
		}
		// An unset count means no standbys, so `--standbys 0` there is a no-op.
		cur := int64(0)
		if v, found, _ := unstructured.NestedFieldNoCopy(db.Object, standbysPath...); found {
			n, err := strconv.ParseInt(fmt.Sprint(v), 10, 64)
			if err != nil {
				return nil, nil, fmt.Errorf("current numberOfStandbys %v is not a number; refusing to change it", v)
			}
			cur = n
		}
		if cur != int64(f.standbys) {
			if err := unstructured.SetNestedField(patch, int64(f.standbys), standbysPath...); err != nil {
				return nil, nil, err
			}
			changes = append(changes, change{"standbys", strconv.FormatInt(cur, 10), strconv.Itoa(f.standbys)})
		}
	}
	return changes, patch, nil
}

// planDisk grows the DataDisk in patch. A merge patch replaces lists
// wholesale, so the patch holds a copy of the full disks list with only
// DataDisk's size changed; other disks (e.g. LogDisk) are sent unchanged.
func planDisk(db *unstructured.Unstructured, patch map[string]any, size string) (*change, error) {
	want, err := resource.ParseQuantity(size)
	if err != nil || want.Sign() <= 0 {
		return nil, fmt.Errorf("--disk %q is not a valid positive size", size)
	}
	disksPath := append(append([]string{}, resourcesPath...), "disks")
	disks, _, _ := unstructured.NestedSlice(db.Object, disksPath...) // deep copy
	for i, d := range disks {
		m, ok := d.(map[string]any)
		if !ok || m["name"] != dataDisk {
			continue
		}
		cur, _ := m["size"].(string)
		have, err := resource.ParseQuantity(cur)
		if err != nil {
			// Without a current size we cannot enforce grow-only, so do not guess.
			return nil, fmt.Errorf("current %s size %q could not be read; refusing to change it", dataDisk, cur)
		}
		switch want.Cmp(have) {
		case 0:
			return nil, nil
		case -1:
			return nil, fmt.Errorf("--disk %s is smaller than the current %s: disks can grow but not shrink", want.String(), cur)
		}
		m["size"] = want.String()
		disks[i] = m
		if err := unstructured.SetNestedSlice(patch, disks, disksPath...); err != nil {
			return nil, err
		}
		return &change{"disk", cur, want.String()}, nil
	}
	return nil, fmt.Errorf("cluster has no %s in spec.primarySpec.resources.disks", dataDisk)
}

// restartsDatabase reports whether any change is to CPU or memory, which
// restarts the database. Disk and standby changes do not.
func restartsDatabase(changes []change) bool {
	for _, c := range changes {
		if c.field == "cpu" || c.field == "memory" {
			return true
		}
	}
	return false
}

// quantityField reads a cpu/memory value that may be stored as a number or
// a string, returning "" when unset.
func quantityField(obj *unstructured.Unstructured, path ...string) string {
	v, found, _ := unstructured.NestedFieldNoCopy(obj.Object, path...)
	if !found || v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

func sameQuantity(cur string, want resource.Quantity) bool {
	have, err := resource.ParseQuantity(cur)
	return err == nil && have.Cmp(want) == 0
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func printChanges(w io.Writer, name string, changes []change) {
	fmt.Fprintf(w, "Cluster %q:\n", name)
	for _, c := range changes {
		fmt.Fprintf(w, "  %-9s %s -> %s\n", c.field, c.from, c.to)
	}
}
