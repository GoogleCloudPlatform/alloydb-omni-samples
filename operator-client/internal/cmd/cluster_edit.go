package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/yaml"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// editFile opens path in the user's editor and waits for it to close.
// Tests replace it.
var editFile = runEditor

const editHeader = `# Edit the DBCluster below and save to apply it. Lines starting with '#'
# are ignored, and an empty file cancels the edit. status and managedFields
# are left out because they can't be edited.
#
`

func newClusterEditCommand(opts *GlobalOptions) *cobra.Command {
	var cpu, memory string

	cmd := &cobra.Command{
		Use:   "edit [CLUSTER_NAME]",
		Short: "Change a cluster's CPU or memory, or edit its DBCluster in $EDITOR",
		Long: "With --cpu or --memory, update the database container's resources.\n" +
			"Without flags, open the DBCluster in $KUBE_EDITOR or $EDITOR (default vi)\n" +
			"and apply what you save. If the change is rejected, the editor reopens\n" +
			"with the error at the top.",
		Example: "  adbo cluster edit my-db --cpu 4 --memory 16Gi\n" +
			"  adbo cluster edit my-db --memory 32Gi -y\n" +
			"  adbo cluster edit my-db            # opens the DBCluster in $EDITOR",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("cpu") || cmd.Flags().Changed("memory") {
				return runClusterEditResources(cmd, opts, args, cpu, memory)
			}
			return runClusterEditInEditor(cmd, opts, args)
		},
	}

	cmd.Flags().StringVar(&cpu, "cpu", "", "CPUs for the database container, e.g. 2 or 500m")
	cmd.Flags().StringVar(&memory, "memory", "", "memory for the database container, e.g. 16Gi")
	return cmd
}

// resourceChange is one --cpu or --memory value to apply.
type resourceChange struct {
	key, flag, value, example string
}

func runClusterEditResources(cmd *cobra.Command, opts *GlobalOptions, args []string, cpu, memory string) error {
	var wanted []resourceChange
	if cmd.Flags().Changed("cpu") {
		wanted = append(wanted, resourceChange{"cpu", "--cpu", cpu, "2 or 500m"})
	}
	if cmd.Flags().Changed("memory") {
		wanted = append(wanted, resourceChange{"memory", "--memory", memory, "16Gi or 4096Mi"})
	}
	// Validate before touching the cluster.
	for _, w := range wanted {
		if err := validateResource(w); err != nil {
			return err
		}
	}

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

	patch := map[string]any{}
	var changes []string
	for _, w := range wanted {
		current := nestedString(obj.Object, "spec", "primarySpec", "resources", w.key)
		if sameQuantity(current, w.value) {
			continue
		}
		patch[w.key] = quantityValue(w.value)
		changes = append(changes, fmt.Sprintf("%s %s -> %s", w.key, orDefault(current, noValue), w.value))
	}
	errOut := cmd.ErrOrStderr()
	if len(changes) == 0 {
		fmt.Fprintf(errOut, "No changes: dbcluster %q already has those resources.\n", name)
		return nil
	}

	fmt.Fprintf(errOut, "Changing dbcluster %q: %s\n", name, strings.Join(changes, ", "))
	fmt.Fprintln(errOut, "The database restarts to apply new CPU or memory.")
	if standbys := nestedString(obj.Object, "spec", "availability", "numberOfStandbys"); standbys == "" || standbys == "0" {
		fmt.Fprintln(errOut, "This cluster has no standbys, so it is unavailable until the restart finishes.")
	}
	ok, err := opts.Confirm(cmd, "Apply these changes?")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("aborted, no changes made (pass -y to skip the prompt)")
	}
	if ctx.Err() != nil {
		// Ctrl+C doesn't stop the prompt, so a "y" can arrive after it.
		return errors.New("interrupted, no changes made")
	}

	body, err := json.Marshal(map[string]any{
		"spec": map[string]any{"primarySpec": map[string]any{"resources": patch}},
	})
	if err != nil {
		return err
	}
	updated, err := c.In(k8s.DBClusterGVR).Patch(ctx, name, types.MergePatchType, body, metav1.PatchOptions{})
	if err != nil {
		return c.FormatErr(fmt.Errorf("updating dbcluster %q: %w", name, err))
	}
	return printEdited(cmd, opts, updated, fmt.Sprintf("dbcluster %q updated", name))
}

// printEdited prints the updated DBCluster for -o json|yaml, or a short
// confirmation otherwise.
func printEdited(cmd *cobra.Command, opts *GlobalOptions, updated *unstructured.Unstructured, msg string) error {
	if opts.Output != output.OutputTable && opts.Output != "" {
		return output.Print(cmd.OutOrStdout(), opts.Output, output.Table{}, updated.Object)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s. Follow progress with `adbo cluster describe %s`.\n", msg, updated.GetName())
	return nil
}

// validateResource rejects values the API server would accept but that are
// almost certainly mistakes, like --memory 16 (bytes) or --cpu 4Gi.
func validateResource(w resourceChange) error {
	q, err := resource.ParseQuantity(w.value)
	if err != nil {
		return fmt.Errorf("invalid %s %q: use a Kubernetes quantity such as %s", w.flag, w.value, w.example)
	}
	if q.Sign() <= 0 {
		return fmt.Errorf("invalid %s %q: must be greater than 0", w.flag, w.value)
	}
	unit := strings.TrimLeft(w.value, "0123456789.")
	switch w.key {
	case "cpu":
		if unit != "" && unit != "m" {
			return fmt.Errorf("invalid --cpu %q: use a number of CPUs such as 2 or 0.5, or millicores such as 500m", w.value)
		}
	case "memory":
		if unit == "" {
			return fmt.Errorf("invalid --memory %q: that's %s bytes; add a unit, e.g. %sGi", w.value, w.value, w.value)
		}
		if q.Cmp(minMemory) < 0 {
			return fmt.Errorf("invalid --memory %q: the database needs at least %s", w.value, minMemory.String())
		}
	}
	return nil
}

// minMemory is a floor for catching unit mistakes, not a sizing guide.
var minMemory = resource.MustParse("1Gi")

func sameQuantity(current, wanted string) bool {
	a, errA := resource.ParseQuantity(current)
	b, errB := resource.ParseQuantity(wanted)
	return errA == nil && errB == nil && a.Cmp(b) == 0
}

// quantityValue keeps whole numbers as integers (`cpu: 4`, like the samples)
// and everything else as the string the user typed.
func quantityValue(s string) any {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return s
}

func runClusterEditInEditor(cmd *cobra.Command, opts *GlobalOptions, args []string) error {
	name, err := opts.ClusterName(args)
	if err != nil {
		return err
	}
	c, err := opts.Clients()
	if err != nil {
		return err
	}
	ctx := cmd.Context()
	live, err := getDBCluster(ctx, c, name)
	if err != nil {
		return err
	}

	editable := live.DeepCopy()
	unstructured.RemoveNestedField(editable.Object, "status")
	editable.SetManagedFields(nil)
	body, err := yaml.Marshal(editable.Object)
	if err != nil {
		return err
	}
	// Parse what we wrote so "unchanged" compares like with like.
	original, err := parseEdited(body)
	if err != nil {
		return err
	}

	f, err := os.CreateTemp("", "adbo-edit-"+name+"-*.yaml")
	if err != nil {
		return err
	}
	path := f.Name()
	f.Close()
	keep := false
	defer func() {
		if !keep {
			os.Remove(path)
		}
	}()

	errOut := cmd.ErrOrStderr()
	content := editHeader + string(body)
	retrying := false
	for {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return err
		}
		if err := editFile(path); err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		edited := string(raw)

		if onlyComments(edited) {
			fmt.Fprintln(errOut, "Edit cancelled: the file was empty, no changes made.")
			return nil
		}
		if retrying && edited == content {
			keep = true
			return fmt.Errorf("edit aborted, the error was not fixed. Your changes are saved in %s", path)
		}

		updated, problem := parseEdited(raw)
		if problem == nil {
			if equality.Semantic.DeepEqual(updated.Object, original.Object) {
				fmt.Fprintln(errOut, "Edit cancelled, no changes made.")
				return nil
			}
			problem = sameIdentity(updated, live)
		}
		if problem == nil && ctx.Err() != nil {
			// Ctrl+C in the editor cancels ctx; otherwise every save would
			// fail with "context canceled" and reopen the editor.
			keep = true
			return fmt.Errorf("interrupted, so nothing was applied; your changes are saved in %s", path)
		}
		if problem == nil {
			result, err := c.In(k8s.DBClusterGVR).Update(ctx, updated, metav1.UpdateOptions{})
			if err == nil {
				return printEdited(cmd, opts, result, fmt.Sprintf("dbcluster %q edited", name))
			}
			if apierrors.IsConflict(err) {
				keep = true
				return fmt.Errorf("dbcluster %q changed while you were editing; run `adbo cluster edit %s` again and re-apply your changes, which are saved in %s", name, name, path)
			}
			problem = err
		}

		retrying = true
		content = errorHeader(problem) + stripLeadingComments(edited)
	}
}

func parseEdited(raw []byte) (*unstructured.Unstructured, error) {
	j, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	u := &unstructured.Unstructured{}
	if err := u.UnmarshalJSON(j); err != nil {
		return nil, err
	}
	return u, nil
}

// sameIdentity rejects edits that would point the update at another object.
func sameIdentity(edited, live *unstructured.Unstructured) error {
	for _, f := range []struct{ field, got, want string }{
		{"apiVersion", edited.GetAPIVersion(), live.GetAPIVersion()},
		{"kind", edited.GetKind(), live.GetKind()},
		{"metadata.name", edited.GetName(), live.GetName()},
		{"metadata.namespace", edited.GetNamespace(), live.GetNamespace()},
	} {
		if f.got != f.want {
			return fmt.Errorf("%s can't be changed (was %q, now %q)", f.field, f.want, f.got)
		}
	}
	return nil
}

func errorHeader(problem error) string {
	var b strings.Builder
	b.WriteString(editHeader)
	b.WriteString("# The last save was not applied:\n")
	for _, line := range strings.Split(problem.Error(), "\n") {
		b.WriteString("#   " + line + "\n")
	}
	b.WriteString("#\n# Fix it and save, or quit without saving to abort.\n#\n")
	return b.String()
}

func onlyComments(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "#") {
			return false
		}
	}
	return true
}

// stripLeadingComments drops the header so retries don't stack headers.
func stripLeadingComments(s string) string {
	lines := strings.Split(s, "\n")
	i := 0
	for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
		i++
	}
	return strings.Join(lines[i:], "\n")
}

// runEditor runs $KUBE_EDITOR, then $EDITOR, then vi. It goes through the
// shell so values like EDITOR="code --wait" work.
func runEditor(path string) error {
	editor := os.Getenv("KUBE_EDITOR")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command("sh", "-c", editor+` "$1"`, "adbo-edit", path)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("running editor %q: %w", editor, err)
	}
	return nil
}
