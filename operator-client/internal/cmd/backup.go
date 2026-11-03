package cmd

import (
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

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
	c.AddCommand(
		newBackupTriggerCommand(opts),
		newBackupListCommand(opts),
	)
	return c
}

func dbclusterRef(obj *unstructured.Unstructured) string {
	ref, _, _ := unstructured.NestedString(obj.Object, "spec", "dbclusterRef")
	return ref
}
