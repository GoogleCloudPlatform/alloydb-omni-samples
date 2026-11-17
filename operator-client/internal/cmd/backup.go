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

// backupID reads status.physicalBackupStatus.backupID.
func backupID(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	id, _, _ := unstructured.NestedString(obj.Object, "status", "physicalBackupStatus", "backupID")
	return id
}

// backupPlanRef reads spec.backupPlanRef.
func backupPlanRef(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	ref, _, _ := unstructured.NestedString(obj.Object, "spec", "backupPlanRef")
	return ref
}

// statusTime reads status.createTime or status.completeTime.
func statusTime(obj *unstructured.Unstructured, field string) string {
	if obj == nil {
		return ""
	}
	s, _, _ := unstructured.NestedString(obj.Object, "status", field)
	return s
}
