package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	var interactive bool
	cmd := &cobra.Command{
		Use:   "edit [CLUSTER_NAME]",
		Short: "Edit a cluster's DBCluster in $EDITOR or a terminal UI",
		Long: "Open the DBCluster in $KUBE_EDITOR or $EDITOR (default vi) and apply\n" +
			"what you save. If the change is rejected, the editor reopens with the\n" +
			"error at the top.\n\n" +
			"With -i, edit the spec field by field in a terminal UI instead. Each\n" +
			"change is checked with the API server (a dry run) as you make it, so\n" +
			"you can iterate until it's accepted, then apply it.\n\n" +
			"To change CPU, memory, disk or standbys, `adbo cluster scale` is simpler.",
		Example: "  adbo cluster edit my-db\n" +
			"  adbo cluster edit my-db -i\n" +
			"  KUBE_EDITOR=\"code --wait\" adbo cluster edit my-db",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if interactive {
				return runClusterEditInteractive(cmd, opts, args)
			}
			return runClusterEditInEditor(cmd, opts, args)
		},
	}
	cmd.Flags().BoolVarP(&interactive, "interactive", "i", false,
		"edit field by field in a terminal UI that checks each change with the API server")
	// Resizing lives in `cluster scale`; say so instead of a bare "unknown flag".
	cmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		if strings.Contains(err.Error(), "--cpu") || strings.Contains(err.Error(), "--memory") {
			return fmt.Errorf("%w; to change CPU or memory, use `adbo cluster scale`", err)
		}
		return err
	})
	return cmd
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

	// Object names can be 253 characters; filenames are capped at 255 bytes.
	short := name
	if len(short) > 64 {
		short = short[:64]
	}
	f, err := os.CreateTemp("", "adbo-edit-"+short+"-*.yaml")
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
			problem = c.FormatErr(err)
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

// runEditor runs the editor on path and waits for it to close.
func runEditor(path string) error {
	c, editor := editorCommand(path)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return fmt.Errorf("running editor %q: %w", editor, err)
	}
	return nil
}

// editorCommand runs $KUBE_EDITOR, then $EDITOR, then vi. It goes through
// the shell so values like EDITOR="code --wait" work.
func editorCommand(path string) (*exec.Cmd, string) {
	editor := os.Getenv("KUBE_EDITOR")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	return exec.Command("sh", "-c", editor+` "$1"`, "adbo-edit", path), editor
}
