package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/wait"
)

// dataDisk is the DBCluster disk that holds the database files.
const dataDisk = "DataDisk"

// clusterReadyPhase is DBCluster status.phase once the operator has
// finished reconciling.
const clusterReadyPhase = "DBClusterReady"

var (
	resourcesPath   = []string{"spec", "primarySpec", "resources"}
	standbysPath    = []string{"spec", "availability", "numberOfStandbys"}
	readReplicaPath = []string{"spec", "availability", "enableStandbyAsReadReplica"}
)

// maxStandbys is the DBCluster CRD's maximum for numberOfStandbys.
const maxStandbys = 5

// scaleMinMemory catches unit mistakes (e.g. --memory 16 meaning 16 bytes); it
// is not a sizing guide. Matches `cluster edit`.
var scaleMinMemory = resource.MustParse("1Gi")

// errDeclined is returned when the user answers no (or stdin is closed
// without -y), so scripts do not mistake an aborted scale for success.
var errDeclined = errors.New("cancelled; nothing changed")

// scaleFlags are the requested changes; empty string / -1 means "leave as is".
type scaleFlags struct {
	cpu, memory, disk string
	standbys          int
	standbysSet       bool // --standbys was passed (0 is a valid value)
	wait              bool // block until the operator has applied the change
}

func newClusterScaleCommand(opts *GlobalOptions) *cobra.Command {
	f := scaleFlags{standbys: -1}
	var waitFlags *WaitFlags

	cmd := &cobra.Command{
		Use:   "scale [CLUSTER_NAME]",
		Short: "Change a cluster's CPU, memory, disk size or number of standbys",
		Long: "Change the compute and storage of a database cluster.\n" +
			"Only the flags you pass are changed. Changing CPU or memory restarts the\n" +
			"database, so adbo shows the change and asks before applying it (skip with -y).\n" +
			"Disks can grow but not shrink.\n" +
			"By default adbo waits until the operator has applied the change and the\n" +
			"cluster is ready again (bounded by --timeout); --no-wait returns at once.",
		Example: "  adbo cluster scale my-db --cpu 4 --memory 32Gi\n" +
			"  adbo cluster scale my-db --disk 100Gi\n" +
			"  adbo cluster scale --standbys 1 -y\n" +
			"  adbo cluster scale my-db --cpu 8 --no-wait",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f.standbysSet = cmd.Flags().Changed("standbys")
			f.wait = waitFlags.Enabled()
			// Check flags first: "nothing to change" is more useful than
			// "no cluster specified" when both are missing.
			if f.empty() {
				return errNothingToChange
			}
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
	cmd.Flags().IntVar(&f.standbys, "standbys", -1, "number of standby instances, 0-5 (0 disables HA)")
	waitFlags = opts.AddWaitFlags(cmd)
	return cmd
}

func (f scaleFlags) empty() bool {
	return f.cpu == "" && f.memory == "" && f.disk == "" && !f.standbysSet
}

var errNothingToChange = errors.New("nothing to change: pass at least one of --cpu, --memory, --disk, --standbys")

// change is one row of the "before -> after" summary shown before patching.
type change struct{ field, from, to string }

func runClusterScale(cmd *cobra.Command, opts *GlobalOptions, name string, f scaleFlags) error {
	if f.empty() {
		return errNothingToChange
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
		if isStructured(opts) {
			return output.Print(out, opts.Output, output.Table{}, db.Object)
		}
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
		return errDeclined
	}

	body, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	updated, err := c.In(k8s.DBClusterGVR).Patch(cmd.Context(), name, types.MergePatchType, body, metav1.PatchOptions{})
	if err != nil {
		return c.FormatErr(err)
	}
	if f.wait {
		if updated, err = waitForScale(cmd, opts, c, updated, patch); err != nil {
			return err
		}
	}
	if isStructured(opts) {
		return output.Print(out, opts.Output, output.Table{}, updated.Object)
	}
	if f.wait {
		fmt.Fprintf(out, "Scaled cluster %q%s.\n", name, k8s.Location(c.Namespace, c.Context))
		return nil
	}
	fmt.Fprintf(out, "Scaling cluster %q%s. Check progress with `adbo cluster list`.\n",
		name, k8s.Location(c.Namespace, c.Context))
	return nil
}

// waitForScale blocks until the operator has applied patch to the cluster
// (returned by the Patch call), printing phase changes to stderr. The
// deadline is --timeout.
func waitForScale(cmd *cobra.Command, opts *GlobalOptions, c *k8s.Clients, patched *unstructured.Unstructured, patch map[string]any) (*unstructured.Unstructured, error) {
	name := patched.GetName()
	errOut := cmd.ErrOrStderr()
	limit := ""
	if opts.Timeout > 0 {
		limit = fmt.Sprintf(" (up to %s)", opts.Timeout)
	}
	fmt.Fprintf(errOut, "Waiting for cluster %q to finish scaling%s...\n", name, limit)

	ctx, cancel := opts.WithTimeout(cmd.Context())
	defer cancel()
	prevPhase := ""
	done := scaleDone(patched.GetGeneration(), patch)
	last, err := wait.ForCondition(ctx, c, k8s.DBClusterGVR, name, func(db *unstructured.Unstructured) (bool, error) {
		if p := wait.Phase(db); p != "" && p != prevPhase {
			fmt.Fprintf(errOut, "  %s: %s\n", name, p)
			prevPhase = p
		}
		return done(db)
	})
	switch {
	case err == nil:
		return last, nil
	case errors.Is(err, context.DeadlineExceeded):
		// The patch is already applied; only our wait gave up.
		return nil, fmt.Errorf("cluster %q did not finish scaling within %s (last phase %q); "+
			"the change is applied and the operator is still working on it, check `adbo cluster list`: %w",
			name, opts.Timeout, wait.Phase(last), err)
	default:
		return nil, c.FormatErr(err)
	}
}

// scaleDone reports when the operator has finished applying patch, a
// patch from planScale whose result had metadata.generation generation:
//   - it has observed that generation (when it reports observedGeneration);
//   - the cluster is Ready again;
//   - the primary's allocated cpu, memory and DataDisk match the patch
//     (each checked only when the operator reports it);
//   - when standbys were raised above 0, HA is ready.
//
// A phase ending in "Failed" stops the wait with a *wait.FailedError.
func scaleDone(generation int64, patch map[string]any) wait.Condition {
	return func(db *unstructured.Unstructured) (bool, error) {
		phase := wait.Phase(db)
		if strings.HasSuffix(phase, "Failed") {
			return false, &wait.FailedError{Name: db.GetName(), Phase: phase, Reason: wait.Reason(db)}
		}
		if og, found, _ := unstructured.NestedInt64(db.Object, "status", "observedGeneration"); found && og < generation {
			return false, nil
		}
		if phase != clusterReadyPhase {
			return false, nil
		}
		if !allocatedMatches(db, patch) {
			return false, nil
		}
		if n, found, _ := unstructured.NestedInt64(patch, standbysPath...); found && n > 0 &&
			k8s.ConditionStatus(db, "HAReady") != "True" {
			return false, nil
		}
		return true, nil
	}
}

// allocatedMatches reports whether status.primary.allocatedResources agrees
// with the cpu, memory and DataDisk size in patch. A value the operator
// does not report is treated as matching, so an older operator that omits
// allocatedResources does not hang the wait.
func allocatedMatches(db *unstructured.Unstructured, patch map[string]any) bool {
	allocated := []string{"status", "primary", "allocatedResources"}
	for _, field := range []string{"cpu", "memory"} {
		want, found, _ := unstructured.NestedFieldNoCopy(patch, append(append([]string{}, resourcesPath...), field)...)
		if !found {
			continue
		}
		have := quantityField(db, append(append([]string{}, allocated...), field)...)
		if have == "" {
			continue
		}
		q, err := resource.ParseQuantity(fmt.Sprint(want))
		if err != nil || !quantityEqual(have, q) {
			return false
		}
	}
	wantDisk := diskSize(patch, resourcesPath)
	haveDisk := diskSize(db.Object, allocated)
	if wantDisk != "" && haveDisk != "" {
		q, err := resource.ParseQuantity(wantDisk)
		if err != nil || !quantityEqual(haveDisk, q) {
			return false
		}
	}
	return true
}

// diskSize returns the DataDisk size in the disks list under base, or "".
func diskSize(obj map[string]any, base []string) string {
	disks, _, _ := unstructured.NestedSlice(obj, append(append([]string{}, base...), "disks")...)
	for _, d := range disks {
		if m, ok := d.(map[string]any); ok && m["name"] == dataDisk {
			size, _ := m["size"].(string)
			return size
		}
	}
	return ""
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
		q, err := parseScaleQuantity(r.flag, r.value)
		if err != nil {
			return nil, nil, err
		}
		path := append(append([]string{}, resourcesPath...), r.field)
		cur := quantityField(db, path...)
		if cur != "" && quantityEqual(cur, q) {
			continue
		}
		// The CRD accepts cpu as an int or a string; keep whole cores as ints
		// to match what users usually write in YAML.
		var v any = q.String()
		if r.field == "cpu" {
			if n, err := strconv.ParseInt(q.String(), 10, 64); err == nil {
				v = n
			}
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
		if f.standbys < 0 || f.standbys > maxStandbys {
			return nil, nil, fmt.Errorf("--standbys must be between 0 and %d", maxStandbys)
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
			// The CRD requires enableStandbyAsReadReplica whenever
			// spec.availability is present, so a cluster with no
			// availability block would reject the patch without it.
			// false matches the operator's behaviour when it is unset.
			if _, found, _ := unstructured.NestedFieldNoCopy(db.Object, readReplicaPath...); !found {
				if err := unstructured.SetNestedField(patch, false, readReplicaPath...); err != nil {
					return nil, nil, err
				}
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
	want, err := parseScaleQuantity("--disk", size)
	if err != nil {
		return nil, err
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

// parseScaleQuantity parses a --cpu, --memory or --disk value and rejects
// ones the API server would accept but are almost certainly mistakes:
// unitless memory/disk (bytes) and byte units on CPU.
func parseScaleQuantity(flag, value string) (resource.Quantity, error) {
	q, err := resource.ParseQuantity(value)
	if err != nil || q.Sign() <= 0 {
		return resource.Quantity{}, fmt.Errorf("%s %q is not a valid positive size", flag, value)
	}
	unit := strings.TrimLeft(value, "0123456789.")
	switch flag {
	case "--cpu":
		if unit != "" && unit != "m" {
			return resource.Quantity{}, fmt.Errorf("--cpu %q: use a number of cores such as 2 or 0.5, or millicores such as 500m", value)
		}
	case "--memory", "--disk":
		if unit == "" {
			return resource.Quantity{}, fmt.Errorf("%s %q is %s bytes; add a unit, e.g. %sGi", flag, value, value, value)
		}
		if flag == "--memory" && q.Cmp(scaleMinMemory) < 0 {
			return resource.Quantity{}, fmt.Errorf("--memory %q: the database needs at least %s", value, scaleMinMemory.String())
		}
	}
	return q, nil
}

// quantityEqual reports whether cur (as stored on the cluster) equals want.
// Not named sameQuantity: `cluster edit` defines that in this package.
func quantityEqual(cur string, want resource.Quantity) bool {
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
