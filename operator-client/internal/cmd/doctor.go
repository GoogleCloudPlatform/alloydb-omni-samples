package cmd

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/cluster"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/doctor"
	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// ErrReported is a failure the command already printed.
// main exits 1 without writing a second Error line.
var ErrReported = errors.New("")

func newDoctorCommand(opts *GlobalOptions) *cobra.Command {
	var (
		allNamespaces     bool
		operatorNamespace string
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the AlloyDB Omni installation, prerequisites, and versions",
		Long: `Check whether AlloyDB Omni is installed and whether this cluster can run it.

doctor validates Kubernetes reachability, RBAC, cert-manager (including webhook
image pulls), the Omni operator and its admission webhooks, and node size.
Version checks compare declared DBCluster specs with observed status. They do
not contact the network to look up the latest upstream release.

A failing check exits 1. Warnings, including a cluster that has not scheduled
any nodes yet, do not.`,
		Example:       "  adbo doctor\n  adbo doctor -A\n  adbo doctor --operator-namespace alloydb-omni-system",
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(cmd, opts, allNamespaces, operatorNamespace)
		},
	}
	cmd.Flags().BoolVarP(&allNamespaces, "all-namespaces", "A", false, "compare DBCluster versions in every namespace")
	cmd.Flags().StringVar(&operatorNamespace, "operator-namespace", cluster.OperatorNamespace, "namespace of the AlloyDB Omni operator")
	return cmd
}

func runDoctor(cmd *cobra.Command, opts *GlobalOptions, allNamespaces bool, operatorNamespace string) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	c, err := opts.Clients()
	var rep doctor.Report
	if err != nil {
		rep = doctor.Evaluate(doctor.Snapshot{APIError: err.Error()})
	} else {
		if !allNamespaces {
			if err := followSelectedNamespace(c); err != nil {
				return err
			}
		}
		rep, err = doctor.Run(ctx, c, allNamespaces, operatorNamespace)
		if err != nil {
			return err
		}
	}
	if err := printDoctor(cmd, opts.Output, rep); err != nil {
		return err
	}
	if rep.Failed() {
		return ErrReported
	}
	return nil
}

func printDoctor(cmd *cobra.Command, format string, rep doctor.Report) error {
	out := cmd.OutOrStdout()
	if format == output.OutputJSON || format == output.OutputYAML {
		return output.Print(out, format, output.Table{}, rep)
	}
	checks := make([]output.Check, len(rep.Results))
	for i, result := range rep.Results {
		checks[i] = output.Check{Name: result.Name, Status: result.Status, Detail: result.Detail}
	}
	if err := output.PrintChecks(out, checks); err != nil {
		return err
	}
	fmt.Fprintf(out, "\ninstallation: %s\n", rep.Installation)
	if rep.Next != "" {
		fmt.Fprintf(out, "next: %s\n", rep.Next)
	}
	if rep.Note != "" {
		fmt.Fprintf(out, "note: %s\n", rep.Note)
	}
	return nil
}
