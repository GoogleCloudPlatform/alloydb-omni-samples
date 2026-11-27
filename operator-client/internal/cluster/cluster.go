// Package cluster holds DBCluster facts shared by connect, doctor, and support export.
package cluster

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/GoogleCloudPlatform/alloydb-omni-samples/operator-client/internal/k8s"
)

const (
	// LabelDBCluster selects pods that belong to a DBCluster.
	// Documented by the AlloyDB Omni connect guide.
	LabelDBCluster = "alloydbomni.internal.dbadmin.goog/dbcluster"
	// LabelTaskType separates database pods from sidecar pods.
	LabelTaskType = "alloydbomni.internal.dbadmin.goog/task-type"
	// TaskDatabase is the database pod task-type value.
	TaskDatabase = "database"
	// LabelHARole is set on Instance CRs and sometimes copied onto pods.
	LabelHARole = "dbs.internal.dbadmin.goog/ha-role"
	// RolePrimary is the HA role of the writable instance.
	RolePrimary = "Primary"
	// RoleStandby is the HA role of a replica that can serve reads.
	RoleStandby = "Standby"
	// ContainerDatabase is the container that runs PostgreSQL.
	ContainerDatabase = "database"
	// DefaultDBPort is PostgreSQL's port when spec.primarySpec.port is unset.
	DefaultDBPort int32 = 5432
	// PhaseReady is status.phase once the DBCluster accepts connections.
	PhaseReady = "DBClusterReady"
	// OperatorNamespace is where the Omni operator Deployments run.
	OperatorNamespace = "alloydb-omni-system"
	// CertManagerNamespace is the Helm install namespace for cert-manager.
	CertManagerNamespace = "cert-manager"
	// DiagnosticPath is the in-pod file that holds the real PostgreSQL crash reason.
	// Kubernetes events only say the primary is not ready.
	DiagnosticPath = "/obs/diagnostic/postgresql.internal"
)

// PodInfo is the subset of a Pod that primary selection needs.
type PodInfo struct {
	Name   string
	Labels map[string]string
}

// InstanceInfo is the subset of an internal Instance used to find the primary.
type InstanceInfo struct {
	Name string
	Role string
}

// PasswordSecretName is spec.primarySpec.adminUser.passwordRef.name,
// or db-pw-<cluster> when the spec does not name one.
func PasswordSecretName(clusterName string, obj *unstructured.Unstructured) string {
	if obj != nil {
		if name, ok, _ := unstructured.NestedString(obj.Object, "spec", "primarySpec", "adminUser", "passwordRef", "name"); ok && name != "" {
			return name
		}
	}
	return k8s.AdminSecretName(clusterName)
}

// CASecret is the secret that holds the server CA.
// status.certificateReference wins. Otherwise the operator default is
// dbs-al-cert-<cluster>, key ca.crt.
func CASecret(clusterName string, obj *unstructured.Unstructured) (name, key string) {
	name = "dbs-al-cert-" + clusterName
	key = "ca.crt"
	if obj == nil {
		return name, key
	}
	if n, ok, _ := unstructured.NestedString(obj.Object, "status", "certificateReference", "secretRef", "name"); ok && n != "" {
		name = n
	}
	if k, ok, _ := unstructured.NestedString(obj.Object, "status", "certificateReference", "certificateKey"); ok && k != "" {
		key = k
	}
	return name, key
}

// DBPort reads spec.primarySpec.port. Missing or out of range becomes 5432.
func DBPort(obj *unstructured.Unstructured) int32 {
	if obj == nil {
		return DefaultDBPort
	}
	v, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "primarySpec", "port")
	if !found || v == nil {
		return DefaultDBPort
	}
	var n int64
	switch t := v.(type) {
	case int:
		n = int64(t)
	case int32:
		n = int64(t)
	case int64:
		n = t
	case float64:
		n = int64(t)
	default:
		return DefaultDBPort
	}
	if n < 1 || n > 65535 {
		return DefaultDBPort
	}
	return int32(n)
}

// Phase reads status.phase.
func Phase(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	p, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	return p
}

// DatabaseVersion is spec.databaseVersion.
func DatabaseVersion(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	s, _, _ := unstructured.NestedString(obj.Object, "spec", "databaseVersion")
	return s
}

// RunningDatabaseVersion prefers status.currentDatabaseVersion and falls
// back to status.primary.currentDatabaseVersion (the field the CRD defines).
func RunningDatabaseVersion(obj *unstructured.Unstructured) string {
	return firstVersion(obj, "currentDatabaseVersion")
}

// AgentsVersion is spec.controlPlaneAgentsVersion.
func AgentsVersion(obj *unstructured.Unstructured) string {
	if obj == nil {
		return ""
	}
	s, _, _ := unstructured.NestedString(obj.Object, "spec", "controlPlaneAgentsVersion")
	return s
}

// RunningAgentsVersion is the observed control-plane agents version.
func RunningAgentsVersion(obj *unstructured.Unstructured) string {
	return firstVersion(obj, "currentControlPlaneAgentsVersion")
}

func firstVersion(obj *unstructured.Unstructured, field string) string {
	if obj == nil {
		return ""
	}
	if s, ok, _ := unstructured.NestedString(obj.Object, "status", field); ok && s != "" {
		return s
	}
	s, _, _ := unstructured.NestedString(obj.Object, "status", "primary", field)
	return s
}

// CriticalIncidents formats status.criticalIncidents as "CODE message" lines.
func CriticalIncidents(obj *unstructured.Unstructured) []string {
	if obj == nil {
		return nil
	}
	items, _, _ := unstructured.NestedSlice(obj.Object, "status", "criticalIncidents")
	var lines []string
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		code, _ := m["code"].(string)
		msg, _ := m["message"].(string)
		line := strings.TrimSpace(code + " " + msg)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// SecretBytes returns a copy of one key. The error names the secret and the
// key, never the bytes.
func SecretBytes(sec *corev1.Secret, key string) ([]byte, error) {
	if sec == nil {
		return nil, fmt.Errorf("secret is missing")
	}
	b, ok := sec.Data[key]
	if !ok || len(b) == 0 {
		return nil, fmt.Errorf("secret %q has no %q key", sec.Name, key)
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
}

// Belongs reports whether obj is part of cluster.
// An empty cluster matches every object. A DBCluster matches on its own name.
// Other objects match spec.dbclusterRef, spec.sourceDBCluster, or the dbcluster label.
func Belongs(obj *unstructured.Unstructured, cluster string) bool {
	if cluster == "" || obj == nil {
		return cluster == ""
	}
	if obj.GetName() == cluster && (obj.GetKind() == "DBCluster" || obj.GetKind() == "") {
		return true
	}
	if ref, ok, _ := unstructured.NestedString(obj.Object, "spec", "dbclusterRef"); ok && ref == cluster {
		return true
	}
	if ref, ok, _ := unstructured.NestedString(obj.Object, "spec", "sourceDBCluster"); ok && ref == cluster {
		return true
	}
	if obj.GetLabels()[LabelDBCluster] == cluster {
		return true
	}
	return false
}

// SelectPrimary picks the database pod to forward to.
// explicit, when set, is returned as-is (--pod overrides discovery).
// One database pod is used directly. Several pods require a single Primary
// role on the pod or on a matching Instance. Otherwise the error lists names
// and asks for --pod.
func SelectPrimary(pods []PodInfo, instances []InstanceInfo, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if len(pods) == 0 {
		return "", fmt.Errorf("DBCluster has no database pod yet. Check the cluster phase and wait for the primary pod")
	}
	if len(pods) == 1 {
		return pods[0].Name, nil
	}

	var primaries []string
	for _, p := range pods {
		if strings.EqualFold(p.Labels[LabelHARole], RolePrimary) {
			primaries = append(primaries, p.Name)
		}
	}
	if len(primaries) == 1 {
		return primaries[0], nil
	}

	var fromInstance []string
	for _, inst := range instances {
		if !strings.EqualFold(inst.Role, RolePrimary) {
			continue
		}
		for _, p := range pods {
			if podMatchesInstance(p, inst) {
				fromInstance = append(fromInstance, p.Name)
			}
		}
	}
	fromInstance = unique(fromInstance)
	if len(fromInstance) == 1 {
		return fromInstance[0], nil
	}

	names := make([]string, len(pods))
	for i, p := range pods {
		names[i] = p.Name
	}
	return "", fmt.Errorf("multiple database pods (%s). Re-run with --pod <name>", strings.Join(names, ", "))
}

// SelectReadOnly picks a pod for --read-only.
// A single pod that belongs to one of readPoolNames wins.
// With no read-pool pod, a single Standby pod is used.
// Zero matches, or more than one match that cannot be narrowed, is an error.
func SelectReadOnly(pods []PodInfo, readPoolNames []string, instances []InstanceInfo) (string, error) {
	var matched []string
	for _, p := range pods {
		for _, name := range readPoolNames {
			if podMatchesInstance(p, InstanceInfo{Name: name}) {
				matched = append(matched, p.Name)
				break
			}
		}
	}
	matched = unique(matched)
	if len(matched) == 1 {
		return matched[0], nil
	}
	if len(matched) > 1 {
		return "", fmt.Errorf("multiple read pool pods (%s). Re-run with --pod <name>", strings.Join(matched, ", "))
	}

	var standbys []string
	for _, p := range pods {
		if strings.EqualFold(p.Labels[LabelHARole], RoleStandby) {
			standbys = append(standbys, p.Name)
		}
	}
	// ha-role is stored on Instance CRs and is not always copied onto pods.
	if len(standbys) == 0 {
		for _, inst := range instances {
			if !strings.EqualFold(inst.Role, RoleStandby) {
				continue
			}
			for _, p := range pods {
				if podMatchesInstance(p, inst) {
					standbys = append(standbys, p.Name)
				}
			}
		}
	}
	standbys = unique(standbys)
	if len(standbys) == 1 {
		return standbys[0], nil
	}
	if len(standbys) > 1 {
		return "", fmt.Errorf("multiple standby pods (%s). Re-run with --pod <name>", strings.Join(standbys, ", "))
	}
	return "", fmt.Errorf("no read-only pod found. Add a ReadPool or a standby read replica, or pass --pod <name>")
}

func podMatchesInstance(p PodInfo, inst InstanceInfo) bool {
	if p.Name == inst.Name {
		return true
	}
	if v := p.Labels["alloydbomni.internal.dbadmin.goog/instance"]; v != "" && v == inst.Name {
		return true
	}
	if v := p.Labels["dbs.internal.dbadmin.goog/instance"]; v != "" && v == inst.Name {
		return true
	}
	if inst.Name != "" && strings.HasPrefix(p.Name, inst.Name+"-") {
		return true
	}
	return false
}

func unique(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// PodSelector is the label selector for database pods of one cluster.
func PodSelector(clusterName string) string {
	return LabelDBCluster + "=" + clusterName + "," + LabelTaskType + "=" + TaskDatabase
}
