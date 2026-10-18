// Package cmd is the cobra command tree for adbo.
//
// Pattern for a new command:
//
//	newXxxCommand(opts) → cobra.Command + local flags
//	  RunE → runXxx(cmd, opts, ...)
//	    opts.ClusterName(args)
//	    opts.Clients()
//	    c.In(k8s.SomeGVR).Get/List/Create/Patch
//	    output.Print(...)
package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// NewRootCommand builds the adbo command tree and registers global flags.
// Add new groups with root.AddCommand(newXxxCommand(opts)).
func NewRootCommand() *cobra.Command {
	// Default cobra only runs the nearest PersistentPreRunE. Traverse so
	// root still validates -o / --timeout when a child adds its own hook.
	cobra.EnableTraverseRunHooks = true

	opts := &GlobalOptions{
		KubeFlags: genericclioptions.NewConfigFlags(true),
	}
	// ConfigFlags.AddFlags skips nil fields. Drop kubectl's --cluster and
	// --request-timeout so we can use --cluster for a DBCluster later and
	// keep a single --timeout for adbo operations.
	opts.KubeFlags.ClusterName = nil
	opts.KubeFlags.Timeout = nil

	var cancel context.CancelFunc
	root := &cobra.Command{
		Use:          "adbo",
		Short:        "Manage AlloyDB Omni clusters on Kubernetes",
		SilenceUsage: true,
		// Runnable so --help prints flags even before subcommands land.
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			switch opts.Output {
			case output.OutputTable, output.OutputJSON, output.OutputYAML:
			default:
				return fmt.Errorf("invalid --output %q: use %s, %s, or %s",
					opts.Output, output.OutputTable, output.OutputJSON, output.OutputYAML)
			}
			if opts.Timeout <= 0 {
				return fmt.Errorf("--timeout must be greater than 0")
			}
			var ctx context.Context
			ctx, cancel = context.WithTimeout(cmd.Context(), opts.Timeout)
			cmd.SetContext(ctx)
			return nil
		},
		PersistentPostRun: func(cmd *cobra.Command, args []string) {
			if cancel != nil {
				cancel()
			}
		},
	}

	opts.KubeFlags.AddFlags(root.PersistentFlags())
	root.PersistentFlags().DurationVar(&opts.Timeout, "timeout", 30*time.Minute, "how long to wait for operations (e.g. 10m, 1h)")
	root.PersistentFlags().StringVarP(&opts.Output, "output", "o", output.OutputTable, "output format: table, json or yaml")
	root.PersistentFlags().BoolVarP(&opts.Yes, "yes", "y", false, "skip confirmation prompts")

	root.AddCommand(
		// newBackupCommand(opts),
	)

	return root
}
