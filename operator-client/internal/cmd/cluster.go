package cmd

import "github.com/spf13/cobra"

// newClusterCommand is the `adbo cluster` group (aliases: clusters).
// Add verbs here (create, list, delete, ...), not in root.go, so
// parallel PRs do not collide on NewRootCommand.
func newClusterCommand(opts *GlobalOptions) *cobra.Command {
	c := &cobra.Command{
		Use:     "cluster",
		Aliases: []string{"clusters"},
		Short:   "Manage AlloyDB Omni database clusters",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	_ = opts
	// c.AddCommand(
	// 	newClusterCreateCommand(opts),
	// 	newClusterListCommand(opts),
	// )
	return c
}
