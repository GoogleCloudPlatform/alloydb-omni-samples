package cmd

import (
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/config"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
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
			"With -v, also prints the cluster's status and where the selection is saved.",
		Example: "  adbo cluster select my-db\n  adbo cluster select\n  adbo cluster select -v\n  adbo cluster select --clear",
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
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Selected cluster %q%s\n", name, k8s.Location(c.Namespace, c.Context))
	if verbose {
		return printSelectionDetails(out, db)
	}
	return nil
}

func runClusterShow(cmd *cobra.Command, opts *GlobalOptions, verbose bool) error {
	c, cfg, err := selectionState(opts)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	where := k8s.Location(c.Namespace, c.Context)
	current := cfg.SelectedCluster(c.Context, c.Namespace)
	if current == "" {
		fmt.Fprintf(out, "No cluster selected%s. Run `adbo cluster select CLUSTER_NAME`.\n", where)
		return nil
	}
	fmt.Fprintf(out, "%s%s\n", current, where)
	if !verbose {
		return nil
	}
	db, err := c.In(k8s.DBClusterGVR).Get(cmd.Context(), current, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		// Keep the selection: the user may be about to recreate it.
		fmt.Fprintf(out, "  Warning: cluster %q no longer exists; select another or run `adbo cluster select --clear`\n", current)
		db = nil
	case err != nil:
		return c.FormatErr(err)
	}
	return printSelectionDetails(out, db)
}

func runClusterClear(cmd *cobra.Command, opts *GlobalOptions, verbose bool) error {
	c, cfg, err := selectionState(opts)
	if err != nil {
		return err
	}
	if err := saveSelection(cfg, c, ""); err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "Cleared the selected cluster%s\n", k8s.Location(c.Namespace, c.Context))
	if verbose {
		return printSelectionDetails(out, nil)
	}
	return nil
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

// printSelectionDetails prints db's status (the same fields as `adbo cluster list`)
// and the config file path. db is nil when the selected cluster no longer exists.
func printSelectionDetails(out io.Writer, db *unstructured.Unstructured) error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if db != nil {
		for i, v := range clusterStatus(db) {
			fmt.Fprintf(w, "  %s:\t%s\n", clusterStatusColumns[i], v)
		}
	}
	fmt.Fprintf(w, "  Saved in:\t%s\n", path)
	return w.Flush()
}
