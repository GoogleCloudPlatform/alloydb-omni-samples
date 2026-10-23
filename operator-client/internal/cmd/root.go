// Package cmd is the cobra command tree for adbo.
//
// Pattern for a new command:
//
//	newXxxCommand(opts) → cobra.Command + local flags
//	  RunE → runXxx(cmd, opts, ...)
//	    opts.ClusterName(args)
//	    opts.Clients()
//	    c.In(k8s.SomeGVR).Get/List/Create/Patch
//	    ctx, cancel := opts.WithTimeout(cmd.Context()); defer cancel()  // waiters only
//	    return c.FormatErr(err)  // translate StatusError
//	    output.Print(...)
package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/output"
)

// NewRootCommand builds the adbo command tree and registers global flags.
// Groups live in cluster.go / backup.go. Do not add verbs here.
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

	root := &cobra.Command{
		Use:           "adbo",
		Short:         "Manage AlloyDB Omni clusters on Kubernetes",
		SilenceUsage:  true,
		SilenceErrors: true, // main prints via k8s.Format so messages stay consistent
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
			return nil
		},
	}

	opts.KubeFlags.AddFlags(root.PersistentFlags())
	root.PersistentFlags().DurationVar(&opts.Timeout, "timeout", 30*time.Minute, "max wait for async operations that use --wait (does not limit connect/logs)")
	root.PersistentFlags().StringVarP(&opts.Output, "output", "o", output.OutputTable, "output format: table, json or yaml")
	root.PersistentFlags().BoolVarP(&opts.Yes, "yes", "y", false, "skip confirmation prompts")

	root.AddCommand(
		newClusterCommand(opts),
		newBackupCommand(opts),
		newStatusCommand(opts),
	)

	return root
}
