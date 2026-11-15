package cmd

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/config"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

func newClusterSelectCommand(opts *GlobalOptions) *cobra.Command {
	var clearSel, verbose bool

	cmd := &cobra.Command{
		Use:   "select [CLUSTER_NAME]",
		Short: "Set the default cluster for later commands",
		Long: "Set the default cluster used when a command's CLUSTER_NAME is omitted.\n" +
			"The choice is saved per kube-context and namespace, so switching either\n" +
			"never points a command at a cluster you picked somewhere else.\n" +
			"With no CLUSTER_NAME, prints the current selection.\n" +
			"With -v, also prints the cluster's status and where the selection is saved.\n" +
			"With -o json|yaml, prints the selection as an object.",
		Example: "  adbo cluster select my-db\n  adbo cluster select\n  adbo cluster select -v\n  adbo cluster select -o json\n  adbo cluster select --clear",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch {
			case clearSel && len(args) > 0:
				return fmt.Errorf("--clear does not take a CLUSTER_NAME")
			case clearSel:
				return runClusterClear(cmd, opts, verbose)
			case len(args) == 0:
				return runClusterShow(cmd, opts, verbose)
			default:
				return runClusterSelect(cmd, opts, args[0], verbose)
			}
		},
	}

	cmd.Flags().BoolVar(&clearSel, "clear", false, "forget the selected cluster for this context and namespace")
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "also print the cluster's status and the config file path")
	return cmd
}

// selection is what `cluster select` prints for -o json|yaml.
type selection struct {
	Context    string            `json:"context"`
	Namespace  string            `json:"namespace"`
	Cluster    string            `json:"cluster"` // "" when nothing is selected
	ConfigPath string            `json:"configPath"`
	Status     map[string]string `json:"status,omitempty"` // absent when the cluster was not read or no longer exists
}

func runClusterSelect(cmd *cobra.Command, opts *GlobalOptions, name string, verbose bool) error {
	c, cfg, err := selectionState(opts)
	if err != nil {
		return err
	}
	// Refuse to save a typo: a bad default would make every later command fail.
	db, err := c.In(k8s.DBClusterGVR).Get(cmd.Context(), name, metav1.GetOptions{})
	if err != nil {
		return c.FormatErr(err)
	}
	if err := saveSelection(cfg, c, name); err != nil {
		return err
	}
	msg := fmt.Sprintf("Selected cluster %q%s\n", name, k8s.Location(c.Namespace, c.Context))
	return printSelection(cmd, opts, c, name, db, verbose, msg)
}

func runClusterShow(cmd *cobra.Command, opts *GlobalOptions, verbose bool) error {
	c, cfg, err := selectionState(opts)
	if err != nil {
		return err
	}
	where := k8s.Location(c.Namespace, c.Context)
	current := cfg.SelectedCluster(c.Context, c.Namespace)
	if current == "" {
		msg := fmt.Sprintf("No cluster selected%s. Run `adbo cluster select CLUSTER_NAME`.\n", where)
		return printSelection(cmd, opts, c, "", nil, false, msg)
	}
	// Machine output always carries status, so it needs the cluster too.
	needCluster := verbose || isStructured(opts)
	var db *unstructured.Unstructured
	if needCluster {
		db, err = c.In(k8s.DBClusterGVR).Get(cmd.Context(), current, metav1.GetOptions{})
		switch {
		case apierrors.IsNotFound(err):
			// Keep the selection: the user may be about to recreate it.
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: cluster %q no longer exists; select another or run `adbo cluster select --clear`\n", current)
			db = nil
		case err != nil:
			return c.FormatErr(err)
		}
	}
	return printSelection(cmd, opts, c, current, db, verbose, fmt.Sprintf("%s%s\n", current, where))
}

func runClusterClear(cmd *cobra.Command, opts *GlobalOptions, verbose bool) error {
	c, cfg, err := selectionState(opts)
	if err != nil {
		return err
	}
	if err := saveSelection(cfg, c, ""); err != nil {
		return err
	}
	msg := fmt.Sprintf("Cleared the selected cluster%s\n", k8s.Location(c.Namespace, c.Context))
	return printSelection(cmd, opts, c, "", nil, verbose, msg)
}

// selectionState loads what every select mode needs: where we are, and the saved selections.
func selectionState(opts *GlobalOptions) (*k8s.Clients, *config.Config, error) {
	c, err := opts.Clients()
	if err != nil {
		return nil, nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	return c, cfg, nil
}

// saveSelection records name for the client's context and namespace; "" clears it.
func saveSelection(cfg *config.Config, c *k8s.Clients, name string) error {
	cfg.SetSelectedCluster(c.Context, c.Namespace, name)
	return cfg.Save()
}

func isStructured(opts *GlobalOptions) bool {
	return opts.Output == output.OutputJSON || opts.Output == output.OutputYAML
}

// printSelection writes a selection object for -o json|yaml; otherwise msg,
// plus status details and the config path with -v. db is nil when the
// cluster was not read or no longer exists.
func printSelection(cmd *cobra.Command, opts *GlobalOptions, c *k8s.Clients, name string, db *unstructured.Unstructured, verbose bool, msg string) error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if isStructured(opts) {
		sel := selection{Context: c.Context, Namespace: c.Namespace, Cluster: name, ConfigPath: path}
		if db != nil {
			sel.Status = map[string]string{}
			for i, v := range clusterStatus(db) {
				sel.Status[strings.ToLower(clusterStatusColumns[i])] = v
			}
		}
		return output.Print(out, opts.Output, output.Table{}, sel)
	}
	fmt.Fprint(out, msg)
	if !verbose {
		return nil
	}
	return printSelectionDetails(out, db, path)
}

// printSelectionDetails prints db's status (the same fields as `adbo cluster list`)
// and the config file path. db is nil when there is no cluster to describe.
func printSelectionDetails(out io.Writer, db *unstructured.Unstructured, path string) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if db != nil {
		for i, v := range clusterStatus(db) {
			fmt.Fprintf(w, "  %s:\t%s\n", clusterStatusColumns[i], v)
		}
	}
	fmt.Fprintf(w, "  Saved in:\t%s\n", path)
	return w.Flush()
}
