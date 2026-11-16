package cmd

import (
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/duration"
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

// dbclusterRef reads spec.dbclusterRef. The Omni CRD types this as a
// string (the DBCluster name), not a LocalObjectReference {name: ...}.
func dbclusterRef(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	ref, _, _ := unstructured.NestedString(obj.Object, "spec", "dbclusterRef")
	return ref
}

// physicalBackupType reads spec.physicalBackupSpec.backupType.
// The Backup CRD printer column Type uses the same path (full, diff, or incr).
func physicalBackupType(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	t, _, _ := unstructured.NestedString(obj.Object, "spec", "physicalBackupSpec", "backupType")
	return t
}

func resourceAge(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	ts := obj.GetCreationTimestamp()
	if ts.IsZero() {
		return ""
	}
	return duration.HumanDuration(time.Since(ts.Time))
}
