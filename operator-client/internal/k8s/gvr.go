package k8s

import "k8s.io/apimachinery/pkg/runtime/schema"

const group = "alloydbomni.dbadmin.goog" // the operator's public API group

var (
	DBClusterGVR  = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "dbclusters"}
	BackupPlanGVR = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "backupplans"}
	BackupGVR     = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "backups"}
	RestoreGVR    = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "restores"}
	FailoverGVR   = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "failovers"}
	SwitchoverGVR = schema.GroupVersionResource{Group: group, Version: "v1", Resource: "switchovers"}
)

const APIVersion = group + "/v1" // for the "apiVersion:" field of objects you create