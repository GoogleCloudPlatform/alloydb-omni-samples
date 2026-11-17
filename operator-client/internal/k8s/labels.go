package k8s

import "k8s.io/apimachinery/pkg/labels"

// Labels the operator puts on the pods it runs for a DBCluster.
const (
	DBClusterLabel = "alloydbomni.internal.dbadmin.goog/dbcluster"
	TaskTypeLabel  = "alloydbomni.internal.dbadmin.goog/task-type"

	// DatabaseContainer runs PostgreSQL and dbdaemon in a database pod.
	DatabaseContainer = "database"
)

// DatabasePodSelector matches a cluster's primary and standby database pods,
// but not its other pods (such as backuprepo).
func DatabasePodSelector(cluster string) string {
	return labels.Set{DBClusterLabel: cluster, TaskTypeLabel: "database"}.String()
}
