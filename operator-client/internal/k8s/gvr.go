package k8s

import "k8s.io/apimachinery/pkg/runtime/schema"

const (
	group         = "alloydbomni.dbadmin.goog"
	internalGroup = "alloydbomni.internal.dbadmin.goog"
)

// Public AlloyDB Omni CRDs. Resource names match `kubectl api-resources`.
var (
	DBClusterGVR       = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "dbclusters"}
	DBClusterHealthGVR = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "dbclusterhealths"}
	DBInstanceGVR      = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "dbinstances"}
	BackupPlanGVR      = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "backupplans"}
	BackupGVR          = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "backups"}
	RestoreGVR         = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "restores"}
	FailoverGVR        = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "failovers"}
	SwitchoverGVR      = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "switchovers"}
	PgBouncerGVR       = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "pgbouncers"}

	// Instance is the internal CR that holds primary/standby roles.
	// Switchover.spec.newPrimary is this object's name.
	InstanceGVR = schema.GroupVersionResource{Group: internalGroup, Version: "v1", Resource: "instances"}
)

// APIVersion is the apiVersion field for objects we create.
const APIVersion = group + "/v1"

// AdminSecretName is the operator's required password Secret: db-pw-<cluster>,
// key = cluster name. Shared by create, delete, and connect.
func AdminSecretName(cluster string) string {
	return "db-pw-" + cluster
}
