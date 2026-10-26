package cmd

import "github.com/spf13/cobra"

// newBackupCommand is the `adbo backup` group (aliases: backups, bk).
// Add verbs here, not in root.go, so parallel PRs do not collide.
func newBackupCommand(opts *GlobalOptions) *cobra.Command {
	c := &cobra.Command{
		Use:     "backup",
		Aliases: []string{"backups", "bk"},
		Short:   "Take and list backups",
		Args:    cobra.NoArgs, // reject mistyped verbs instead of printing help and exiting 0
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	_ = opts
	// c.AddCommand(
	// 	newBackupTriggerCommand(opts),
	// 	newBackupListCommand(opts),
	// )
	return c
}
