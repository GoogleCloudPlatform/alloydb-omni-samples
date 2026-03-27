#!/bin/bash

# Check if the correct number of arguments is provided
if [ "$#" -ne 2 ]; then
    echo "Usage: $0 <DB_CLUSTER_NAME> <DB_CLUSTER_NAMESPACE>"
    exit 1
fi

DB_CLUSTER_NAME=$1
DB_CLUSTER_NAMESPACE=$2
SNAPSHOT_DIR="snapshot-${DB_CLUSTER_NAMESPACE}-${DB_CLUSTER_NAME}"

echo "Starting debug snapshot capture for cluster: $DB_CLUSTER_NAME in namespace: $DB_CLUSTER_NAMESPACE"
echo "Output directory: $SNAPSHOT_DIR"

# 1. Take an operator snapshot
echo "Capturing operator snapshot..."
mkdir -p "$SNAPSHOT_DIR/operator"
OPERATOR_NAMESPACE=$(kubectl get pods -A -l app.kubernetes.io/name=alloydb-omni-operator -o jsonpath='{.items[0].metadata.namespace}' 2>/dev/null)
if [ -z "$OPERATOR_NAMESPACE" ]; then
    echo "Warning: AlloyDB Omni operator not found."
else
    echo "Operator found in namespace: $OPERATOR_NAMESPACE"
    kubectl describe deployment -n "$OPERATOR_NAMESPACE" -l app.kubernetes.io/name=alloydb-omni-operator > "$SNAPSHOT_DIR/operator/operator-statuses.txt"
    OPERATOR_PODS=$(kubectl get pods -n "$OPERATOR_NAMESPACE" -l app.kubernetes.io/name=alloydb-omni-operator -o jsonpath='{.items[*].metadata.name}')
    for OP_POD in $OPERATOR_PODS; do
        kubectl logs -n "$OPERATOR_NAMESPACE" "$OP_POD" --tail=2000 > "$SNAPSHOT_DIR/operator/$OP_POD.log"
    done
fi

# 2. Create database snapshot directory structure
echo "Creating directory structure..."
mkdir -p "$SNAPSHOT_DIR/manifests/external"
mkdir -p "$SNAPSHOT_DIR/manifests/internal"
mkdir -p "$SNAPSHOT_DIR/logs"
mkdir -p "$SNAPSHOT_DIR/k8s-state"

# 3. Capture public resource manifests
echo "Capturing public resource manifests..."
PUBLIC_CRDS=$(kubectl get crds -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | grep "alloydbomni.dbadmin.goog" | grep -v "internal" || true)
for CRD in $PUBLIC_CRDS; do
    SHORT_NAME=$(echo "$CRD" | cut -d'.' -f1)
    echo "  - $SHORT_NAME"
    kubectl get "$CRD" -n "$DB_CLUSTER_NAMESPACE" -o yaml > "$SNAPSHOT_DIR/manifests/external/$SHORT_NAME.yaml" 2>/dev/null || true
done

# 4. Capture internal resource manifests
echo "Capturing internal resource manifests..."
INTERNAL_CRDS=$(kubectl get crds -o jsonpath='{.items[*].metadata.name}' | tr ' ' '\n' | grep "alloydbomni.internal.dbadmin.goog" || true)
for CRD in $INTERNAL_CRDS; do
    SHORT_NAME=$(echo "$CRD" | cut -d'.' -f1)
    echo "  - $SHORT_NAME"
    kubectl get "$CRD" -n "$DB_CLUSTER_NAMESPACE" -o yaml > "$SNAPSHOT_DIR/manifests/internal/$SHORT_NAME.yaml" 2>/dev/null || true
done

# 5. Capture Kubernetes resource manifests
echo "Capturing Kubernetes state..."
kubectl get all -n "$DB_CLUSTER_NAMESPACE" -o wide > "$SNAPSHOT_DIR/k8s-state/all.txt"
kubectl get configmaps -n "$DB_CLUSTER_NAMESPACE" -o wide > "$SNAPSHOT_DIR/k8s-state/configmaps.txt"
kubectl get secrets -n "$DB_CLUSTER_NAMESPACE" -o wide > "$SNAPSHOT_DIR/k8s-state/secrets.txt"
kubectl describe pvc -n "$DB_CLUSTER_NAMESPACE" > "$SNAPSHOT_DIR/k8s-state/pvcs.txt"
kubectl describe services -n "$DB_CLUSTER_NAMESPACE" > "$SNAPSHOT_DIR/k8s-state/services.txt"
kubectl describe pods -n "$DB_CLUSTER_NAMESPACE" -l "alloydbomni.internal.dbadmin.goog/dbcluster=$DB_CLUSTER_NAME" > "$SNAPSHOT_DIR/k8s-state/pod-descriptions.txt"
kubectl get events -n "$DB_CLUSTER_NAMESPACE" --sort-by='.lastTimestamp' > "$SNAPSHOT_DIR/k8s-state/events.txt"

# 6. Capture Backup repository logs
echo "Capturing backup repository logs..."
kubectl logs -n "$DB_CLUSTER_NAMESPACE" -l "alloydbomni.internal.dbadmin.goog/dbcluster=$DB_CLUSTER_NAME" -c backuprepo --tail=2000 > "$SNAPSHOT_DIR/logs/backuprepo.log"

# 7. Capture Database instance logs
echo "Identifying database pods..."
PODS=$(kubectl get pods -n "$DB_CLUSTER_NAMESPACE" -l "alloydbomni.internal.dbadmin.goog/dbcluster=$DB_CLUSTER_NAME,alloydbomni.internal.dbadmin.goog/task-type=database" -o jsonpath='{.items[*].metadata.name}')

for POD_NAME in $PODS; do
    echo "Capturing logs for pod: $POD_NAME"
    kubectl logs -n "$DB_CLUSTER_NAMESPACE" "$POD_NAME" -c database --tail=2000 > "$SNAPSHOT_DIR/logs/$POD_NAME-dbdaemon.log"
    kubectl exec -n "$DB_CLUSTER_NAMESPACE" "$POD_NAME" -c database -- cat /obs/diagnostic/postgresql.log > "$SNAPSHOT_DIR/logs/$POD_NAME-postgresql.log"
done

# 8. Review for sensitive information
echo "Snapshot captured in '$SNAPSHOT_DIR' directory."
echo "IMPORTANT: Review the files for sensitive information (PII, credentials, proprietary data) before sharing."

# 9. Compress the snapshot
echo "Compressing snapshot..."
tar -czf "$SNAPSHOT_DIR.tar.gz" "$SNAPSHOT_DIR"
echo "Done. Please attach '$SNAPSHOT_DIR.tar.gz' to your support case."
