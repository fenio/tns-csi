#!/bin/bash
# Debug script for snapshot test failures
# Run this during a snapshot test to capture diagnostic information

set -e

NAMESPACE="${1:-tns-csi-test}"
PVC_NAME="${2:-test-pvc-from-snapshot-nfs}"
SNAPSHOT_NAME="${3:-test-snapshot-nfs}"

echo "========================================"
echo "CSI Snapshot Test Diagnostics"
echo "========================================"
echo "Namespace: $NAMESPACE"
echo "PVC: $PVC_NAME"
echo "Snapshot: $SNAPSHOT_NAME"
echo ""

echo "=== 1. Check VolumeSnapshot Status ==="
kubectl get volumesnapshot -n "$NAMESPACE" "$SNAPSHOT_NAME" -o yaml 2>&1 || echo "Snapshot not found"
echo ""

echo "=== 2. Check VolumeSnapshotContent ==="
CONTENT_NAME=$(kubectl get volumesnapshot -n "$NAMESPACE" "$SNAPSHOT_NAME" -o jsonpath='{.status.boundVolumeSnapshotContentName}' 2>/dev/null || echo "")
if [ -n "$CONTENT_NAME" ]; then
    kubectl get volumesnapshotcontent "$CONTENT_NAME" -o yaml
else
    echo "No VolumeSnapshotContent found"
fi
echo ""

echo "=== 3. Check PVC Status ==="
kubectl get pvc -n "$NAMESPACE" "$PVC_NAME" -o yaml 2>&1 || echo "PVC not found"
echo ""

echo "=== 4. Check PVC Events ==="
kubectl describe pvc -n "$NAMESPACE" "$PVC_NAME" 2>&1 | grep -A 20 "Events:" || echo "No events"
echo ""

echo "=== 5. Check Controller Capabilities ==="
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin --tail=1000 | \
    grep -i "ControllerGetCapabilities\|CLONE_VOLUME\|CREATE_DELETE_SNAPSHOT" || echo "No capability logs found"
echo ""

echo "=== 6. Check CSI Provisioner Logs (last 100 lines) ==="
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c csi-provisioner --tail=100 || echo "No provisioner logs"
echo ""

echo "=== 7. Check CSI Snapshotter Logs (last 100 lines) ==="
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c csi-snapshotter --tail=100 2>/dev/null || echo "No snapshotter sidecar found"
echo ""

echo "=== 8. Check Driver CreateVolume Logs for Snapshot Restore ==="
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin --tail=500 | \
    grep -A 30 -B 5 "SNAPSHOT RESTORE DETECTED\|createVolumeFromSnapshot\|VolumeContentSource" || echo "No snapshot restore logs found"
echo ""

echo "=== 9. Check for CreateVolume Errors ==="
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin --tail=500 | \
    grep -i "error\|failed" | tail -50 || echo "No recent errors"
echo ""

echo "=== 10. Check StorageClass Parameters ==="
SC_NAME=$(kubectl get pvc -n "$NAMESPACE" "$PVC_NAME" -o jsonpath='{.spec.storageClassName}' 2>/dev/null || echo "")
if [ -n "$SC_NAME" ]; then
    echo "StorageClass: $SC_NAME"
    kubectl get storageclass "$SC_NAME" -o yaml
else
    echo "No StorageClass found"
fi
echo ""

echo "=== 11. Check if PVC is waiting for provisioner ==="
kubectl get pvc -n "$NAMESPACE" "$PVC_NAME" -o jsonpath='{.status.phase}' 2>/dev/null || echo "No status"
echo ""

echo "=== 12. Check TrueNAS Connection ==="
kubectl logs -n kube-system -l app.kubernetes.io/component=controller -c tns-csi-plugin --tail=200 | \
    grep -i "websocket\|connected\|ping\|pong" | tail -20 || echo "No connection logs"
echo ""

echo "========================================"
echo "Diagnostics complete"
echo "========================================"
