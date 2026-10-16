package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/cli-runtime/pkg/genericclioptions"
)

func NewRootCommand() *cobra.Command {
	opts := &GlobalOptions{
			KubeFlags: genericclioptions.NewConfigFlags(true),
	}
	
	root := &cobra.Command{
			Use: 		"adbo",
			Short: 		"Manage AlloyDB Omni clusters on Kubernetes",
			SilenceUsage: true,


			// Runs before any subcommand's RunE
			// Good place for checks that every command needs.
			PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
				switch opts.Output {
				case "table", "json", "yaml":
					return nil
				}
				return fmt.Errorf(
					"invalid --output %q: use table, json, or yaml",
					opts.Output,
				)
			},
	}

	// Persistent flags: available on root and every command below it.
	opts.KubeFlags.AddFlags(root.PersistentFlags())
	root.PersistentFlags().DurationVar(&opts.Timeout, "timeout", 30*time.Minute, "how long to wait for operations (e.g. 10m, 1h)")
	root.PersistentFlags().StringVarP(&opts.Output, "output", "o", "table", "output format: table, json or yaml")
	root.PersistentFlags().BoolVarP(&opts.Yes, "yes", "y", false, "skip confirmation prompts")

	// Attach the top-level groups and commands.
	root.AddCommand(
		// newBackupCommand(opts),
		// Add your command groups here
	)

	return root
}