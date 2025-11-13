#!/bin/bash
# Snapshot Restore Verification Test
# Comprehensive test for snapshot creation and restoration
# Verifies data integrity, restore correctness, and multiple snapshot handling

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="Snapshot Restore Verification"
PVC_NAME_SOURCE="snapshot-source-pvc"
PVC_NAME_RESTORE1="snapshot-restore-pvc-1"
PVC_NAME_RESTORE2="snapshot-restore-pvc-2"
POD_NAME_SOURCE="snapshot-source-pod"
POD_NAME_RESTORE1="snapshot-restore-pod-1"
POD_NAME_RESTORE2="snapshot-restore-pod-2"
SNAPSHOT_NAME_1="test-snapshot-1"
SNAPSHOT_NAME_2="test-snapshot-2"

echo "================================================"
echo "TrueNAS CSI - Snapshot Restore Verification Test"
echo "================================================"
echo ""
echo "This test verifies:"
echo "  • Snapshot creation from active volume"
echo "  • Multiple snapshots of same volume"
echo "  • Restore from snapshot creates independent volume"
echo "  • Restored data matches snapshot point-in-time"
echo "  • Source volume unchanged after snapshot operations"
echo "================================================"

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME_SOURCE}" "${PVC_NAME_SOURCE}"; cleanup_test "${POD_NAME_SOURCE}" "${PVC_NAME_SOURCE}"; cleanup_test "${POD_NAME_RESTORE1}" "${PVC_NAME_RESTORE1}"; cleanup_test "${POD_NAME_RESTORE2}" "${PVC_NAME_RESTORE2}"; kubectl delete volumesnapshot ${SNAPSHOT_NAME_1} ${SNAPSHOT_NAME_2} -n "${TEST_NAMESPACE}" --ignore-not-found=true; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

# Run test steps
verify_cluster
deploy_driver "nfs" --set snapshots.enabled=true
wait_for_driver

#######################################
# Check snapshot support
#######################################
test_step 4 14 "Verifying snapshot support"

# Check if VolumeSnapshotClass exists (created by Helm chart as <storage-class-name>-snapshot)
if ! kubectl get volumesnapshotclass tns-csi-nfs-snapshot &>/dev/null; then
    test_error "VolumeSnapshotClass 'tns-csi-nfs-snapshot' not found"
    test_info "Please ensure snapshot CRDs and snapshot controller are installed"
    test_info "And driver is deployed with snapshots.enabled=true"
    exit 1
fi

test_success "VolumeSnapshotClass available"

#######################################
# Test 1: Create source volume and write initial data
#######################################
test_step 5 14 "Creating source PVC and pod"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME_SOURCE}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 2Gi
  storageClassName: tns-csi-nfs
EOF

kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME_SOURCE}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_PVC}"

test_success "Source PVC created and bound"

# Create pod
cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME_SOURCE}
spec:
  containers:
  - name: test-container
    image: busybox:latest
    command: ["sleep", "600"]
    volumeMounts:
    - name: source-volume
      mountPath: /data
  volumes:
  - name: source-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME_SOURCE}
EOF

kubectl wait --for=condition=Ready pod/"${POD_NAME_SOURCE}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_POD}"

test_success "Source pod ready"

#######################################
# Test 2: Write data for first snapshot
#######################################
test_step 6 14 "Writing initial data (version 1)"

echo ""
test_info "Creating dataset version 1..."
kubectl exec "${POD_NAME_SOURCE}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'Version 1 data' > /data/version.txt && \
           echo 'Snapshot 1 timestamp: '$(date) > /data/timestamp1.txt && \
           mkdir -p /data/v1 && \
           for i in 1 2 3 4 5; do echo \"File \$i version 1\" > /data/v1/file\$i.txt; done && \
           sync"

test_success "Version 1 data written"

# Store checksums
V1_VERSION=$(kubectl exec "${POD_NAME_SOURCE}" -n "${TEST_NAMESPACE}" -- cat /data/version.txt)
V1_FILE_COUNT=$(kubectl exec "${POD_NAME_SOURCE}" -n "${TEST_NAMESPACE}" -- ls /data/v1/ | wc -l)
test_info "Version 1 contains ${V1_FILE_COUNT} files in v1/ directory"

#######################################
# Test 3: Create first snapshot
#######################################
test_step 7 14 "Creating first snapshot"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: ${SNAPSHOT_NAME_1}
spec:
  volumeSnapshotClassName: tns-csi-nfs-snapshot
  source:
    persistentVolumeClaimName: ${PVC_NAME_SOURCE}
EOF

# Wait for snapshot to be ready
test_info "Waiting for snapshot to be ready (timeout: 120s)..."
timeout=120
elapsed=0
last_status=""
while [[ $elapsed -lt $timeout ]]; do
    READY_TO_USE=$(kubectl get volumesnapshot "${SNAPSHOT_NAME_1}" -n "${TEST_NAMESPACE}" \
        -o jsonpath='{.status.readyToUse}' 2>/dev/null || echo "false")
    ERROR_MSG=$(kubectl get volumesnapshot "${SNAPSHOT_NAME_1}" -n "${TEST_NAMESPACE}" \
        -o jsonpath='{.status.error.message}' 2>/dev/null || echo "")
    
    # Log progress every 15 seconds
    if [[ $((elapsed % 15)) -eq 0 ]] && [[ $elapsed -gt 0 ]]; then
        current_status="readyToUse=${READY_TO_USE}"
        if [[ -n "${ERROR_MSG}" ]]; then
            current_status="${current_status}, error: ${ERROR_MSG}"
        fi
        if [[ "${current_status}" != "${last_status}" ]]; then
            test_info "Snapshot status after ${elapsed}s: ${current_status}"
            last_status="${current_status}"
        fi
    fi
    
    if [[ "${READY_TO_USE}" == "true" ]]; then
        break
    fi
    
    # Fail fast if error detected
    if [[ -n "${ERROR_MSG}" ]]; then
        test_error "Snapshot creation failed: ${ERROR_MSG}"
        kubectl describe volumesnapshot "${SNAPSHOT_NAME_1}" -n "${TEST_NAMESPACE}"
        kubectl logs -n kube-system -l app.kubernetes.io/component=controller --tail=50
        exit 1
    fi
    
    sleep 5
    elapsed=$((elapsed + 5))
done

if [[ "${READY_TO_USE}" != "true" ]]; then
    test_error "Snapshot failed to become ready after ${timeout}s"
    echo ""
    echo "=== Snapshot Status ==="
    kubectl describe volumesnapshot "${SNAPSHOT_NAME_1}" -n "${TEST_NAMESPACE}"
    echo ""
    echo "=== Controller Logs (last 100 lines) ==="
    kubectl logs -n kube-system -l app.kubernetes.io/component=controller --tail=100
    exit 1
fi

SNAPSHOT_CONTENT_NAME=$(kubectl get volumesnapshot "${SNAPSHOT_NAME_1}" -n "${TEST_NAMESPACE}" \
    -o jsonpath='{.status.boundVolumeSnapshotContentName}')
test_success "Snapshot 1 created: ${SNAPSHOT_CONTENT_NAME}"

#######################################
# Test 4: Modify data and create second snapshot
#######################################
test_step 8 14 "Modifying data and creating second snapshot"

echo ""
test_info "Creating dataset version 2..."
kubectl exec "${POD_NAME_SOURCE}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'Version 2 data' > /data/version.txt && \
           echo 'Snapshot 2 timestamp: '$(date) > /data/timestamp2.txt && \
           mkdir -p /data/v2 && \
           for i in 1 2 3; do echo \"File \$i version 2\" > /data/v2/file\$i.txt; done && \
           echo 'Modified after snapshot 1' > /data/v1/modified.txt && \
           sync"

test_success "Version 2 data written"

V2_VERSION=$(kubectl exec "${POD_NAME_SOURCE}" -n "${TEST_NAMESPACE}" -- cat /data/version.txt)
V2_V1_FILE_COUNT=$(kubectl exec "${POD_NAME_SOURCE}" -n "${TEST_NAMESPACE}" -- ls /data/v1/ | wc -l)
V2_V2_FILE_COUNT=$(kubectl exec "${POD_NAME_SOURCE}" -n "${TEST_NAMESPACE}" -- ls /data/v2/ | wc -l)
test_info "Version 2: v1/ has ${V2_V1_FILE_COUNT} files, v2/ has ${V2_V2_FILE_COUNT} files"

# Create second snapshot
cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: ${SNAPSHOT_NAME_2}
spec:
  volumeSnapshotClassName: tns-csi-nfs-snapshot
  source:
    persistentVolumeClaimName: ${PVC_NAME_SOURCE}
EOF

# Wait for second snapshot
test_info "Waiting for second snapshot (timeout: 120s)..."
timeout=120
elapsed=0
last_status=""
while [[ $elapsed -lt $timeout ]]; do
    READY_TO_USE=$(kubectl get volumesnapshot "${SNAPSHOT_NAME_2}" -n "${TEST_NAMESPACE}" \
        -o jsonpath='{.status.readyToUse}' 2>/dev/null || echo "false")
    ERROR_MSG=$(kubectl get volumesnapshot "${SNAPSHOT_NAME_2}" -n "${TEST_NAMESPACE}" \
        -o jsonpath='{.status.error.message}' 2>/dev/null || echo "")
    
    # Log progress every 15 seconds
    if [[ $((elapsed % 15)) -eq 0 ]] && [[ $elapsed -gt 0 ]]; then
        current_status="readyToUse=${READY_TO_USE}"
        if [[ -n "${ERROR_MSG}" ]]; then
            current_status="${current_status}, error: ${ERROR_MSG}"
        fi
        if [[ "${current_status}" != "${last_status}" ]]; then
            test_info "Snapshot status after ${elapsed}s: ${current_status}"
            last_status="${current_status}"
        fi
    fi
    
    if [[ "${READY_TO_USE}" == "true" ]]; then
        break
    fi
    
    # Fail fast if error detected
    if [[ -n "${ERROR_MSG}" ]]; then
        test_error "Second snapshot creation failed: ${ERROR_MSG}"
        kubectl describe volumesnapshot "${SNAPSHOT_NAME_2}" -n "${TEST_NAMESPACE}"
        kubectl logs -n kube-system -l app.kubernetes.io/component=controller --tail=50
        exit 1
    fi
    
    sleep 5
    elapsed=$((elapsed + 5))
done

if [[ "${READY_TO_USE}" != "true" ]]; then
    test_error "Second snapshot failed to become ready after ${timeout}s"
    echo ""
    echo "=== Snapshot Status ==="
    kubectl describe volumesnapshot "${SNAPSHOT_NAME_2}" -n "${TEST_NAMESPACE}"
    echo ""
    echo "=== Controller Logs (last 100 lines) ==="
    kubectl logs -n kube-system -l app.kubernetes.io/component=controller --tail=100
    exit 1
fi

test_success "Snapshot 2 created"

#######################################
# Test 5: Restore from first snapshot
#######################################
test_step 9 14 "Restoring volume from snapshot 1"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME_RESTORE1}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 2Gi
  storageClassName: tns-csi-nfs
  dataSource:
    name: ${SNAPSHOT_NAME_1}
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
EOF

kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME_RESTORE1}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_PVC}"

test_success "Restore PVC 1 created and bound"

#######################################
# Test 6: Verify restored data (snapshot 1)
#######################################
test_step 10 14 "Verifying data from snapshot 1 restore"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME_RESTORE1}
spec:
  containers:
  - name: test-container
    image: busybox:latest
    command: ["sleep", "600"]
    volumeMounts:
    - name: restored-volume
      mountPath: /data
  volumes:
  - name: restored-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME_RESTORE1}
EOF

kubectl wait --for=condition=Ready pod/"${POD_NAME_RESTORE1}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_POD}"

test_success "Restore pod 1 ready"

echo ""
test_info "Verifying snapshot 1 data..."

# Check version
RESTORE1_VERSION=$(kubectl exec "${POD_NAME_RESTORE1}" -n "${TEST_NAMESPACE}" -- cat /data/version.txt)
if [[ "${RESTORE1_VERSION}" == "${V1_VERSION}" ]]; then
    test_success "Version file matches snapshot 1: '${RESTORE1_VERSION}'"
else
    test_error "Version mismatch: expected '${V1_VERSION}', got '${RESTORE1_VERSION}'"
    exit 1
fi

# Check v1 directory
RESTORE1_V1_COUNT=$(kubectl exec "${POD_NAME_RESTORE1}" -n "${TEST_NAMESPACE}" -- ls /data/v1/ 2>/dev/null | wc -l || echo "0")
if [[ $RESTORE1_V1_COUNT -eq $V1_FILE_COUNT ]]; then
    test_success "V1 directory file count correct: ${RESTORE1_V1_COUNT}"
else
    test_error "V1 file count mismatch: expected ${V1_FILE_COUNT}, got ${RESTORE1_V1_COUNT}"
    exit 1
fi

# Check that v2 directory doesn't exist (snapshot was before v2 created)
if kubectl exec "${POD_NAME_RESTORE1}" -n "${TEST_NAMESPACE}" -- ls /data/v2/ &>/dev/null; then
    test_error "V2 directory should not exist in snapshot 1"
    exit 1
else
    test_success "V2 directory correctly absent (snapshot 1 was before v2 creation)"
fi

# Check that modified.txt doesn't exist
if kubectl exec "${POD_NAME_RESTORE1}" -n "${TEST_NAMESPACE}" -- ls /data/v1/modified.txt &>/dev/null; then
    test_error "Modified.txt should not exist in snapshot 1"
    exit 1
else
    test_success "Modified.txt correctly absent (snapshot 1 was before modification)"
fi

#######################################
# Test 7: Restore from second snapshot
#######################################
test_step 11 14 "Restoring volume from snapshot 2"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME_RESTORE2}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 2Gi
  storageClassName: tns-csi-nfs
  dataSource:
    name: ${SNAPSHOT_NAME_2}
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
EOF

kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME_RESTORE2}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_PVC}"

test_success "Restore PVC 2 created and bound"

#######################################
# Test 8: Verify restored data (snapshot 2)
#######################################
test_step 12 14 "Verifying data from snapshot 2 restore"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME_RESTORE2}
spec:
  containers:
  - name: test-container
    image: busybox:latest
    command: ["sleep", "600"]
    volumeMounts:
    - name: restored-volume
      mountPath: /data
  volumes:
  - name: restored-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME_RESTORE2}
EOF

kubectl wait --for=condition=Ready pod/"${POD_NAME_RESTORE2}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_POD}"

test_success "Restore pod 2 ready"

echo ""
test_info "Verifying snapshot 2 data..."

# Check version
RESTORE2_VERSION=$(kubectl exec "${POD_NAME_RESTORE2}" -n "${TEST_NAMESPACE}" -- cat /data/version.txt)
if [[ "${RESTORE2_VERSION}" == "${V2_VERSION}" ]]; then
    test_success "Version file matches snapshot 2: '${RESTORE2_VERSION}'"
else
    test_error "Version mismatch: expected '${V2_VERSION}', got '${RESTORE2_VERSION}'"
    exit 1
fi

# Check v2 directory exists
RESTORE2_V2_COUNT=$(kubectl exec "${POD_NAME_RESTORE2}" -n "${TEST_NAMESPACE}" -- ls /data/v2/ 2>/dev/null | wc -l || echo "0")
if [[ $RESTORE2_V2_COUNT -eq $V2_V2_FILE_COUNT ]]; then
    test_success "V2 directory present with correct file count: ${RESTORE2_V2_COUNT}"
else
    test_error "V2 file count mismatch: expected ${V2_V2_FILE_COUNT}, got ${RESTORE2_V2_COUNT}"
    exit 1
fi

# Check modified.txt exists in snapshot 2
if kubectl exec "${POD_NAME_RESTORE2}" -n "${TEST_NAMESPACE}" -- cat /data/v1/modified.txt &>/dev/null; then
    test_success "Modified.txt present in snapshot 2 restore"
else
    test_error "Modified.txt should exist in snapshot 2"
    exit 1
fi

#######################################
# Test 9: Verify independence of restored volumes
#######################################
test_step 13 14 "Verifying restored volumes are independent"

echo ""
test_info "Writing to restored volume 1..."
kubectl exec "${POD_NAME_RESTORE1}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'Modified in restore 1' > /data/restore1-modification.txt"

test_info "Checking source volume is unchanged..."
if kubectl exec "${POD_NAME_SOURCE}" -n "${TEST_NAMESPACE}" -- \
    cat /data/restore1-modification.txt &>/dev/null; then
    test_error "Source volume should not have restore1-modification.txt"
    exit 1
else
    test_success "Source volume independent from restore 1"
fi

test_info "Checking restore 2 is unchanged..."
if kubectl exec "${POD_NAME_RESTORE2}" -n "${TEST_NAMESPACE}" -- \
    cat /data/restore1-modification.txt &>/dev/null; then
    test_error "Restore 2 should not have restore1-modification.txt"
    exit 1
else
    test_success "Restore 2 independent from restore 1"
fi

#######################################
# Test 10: Cleanup snapshots
#######################################
test_step 14 14 "Testing snapshot cleanup"

echo ""
test_info "Deleting snapshots..."
kubectl delete volumesnapshot "${SNAPSHOT_NAME_1}" -n "${TEST_NAMESPACE}"
kubectl delete volumesnapshot "${SNAPSHOT_NAME_2}" -n "${TEST_NAMESPACE}"

test_info "Waiting for snapshots to be deleted..."
sleep 10

if kubectl get volumesnapshot "${SNAPSHOT_NAME_1}" -n "${TEST_NAMESPACE}" &>/dev/null; then
    test_warning "Snapshot 1 still exists (cleanup may take longer)"
else
    test_success "Snapshot 1 deleted"
fi

if kubectl get volumesnapshot "${SNAPSHOT_NAME_2}" -n "${TEST_NAMESPACE}" &>/dev/null; then
    test_warning "Snapshot 2 still exists (cleanup may take longer)"
else
    test_success "Snapshot 2 deleted"
fi

echo ""
echo "================================================"
echo "Snapshot Restore Verification Summary"
echo "================================================"
echo ""
echo "Source Volume Operations:"
echo "  ✓ Created source volume with initial data (v1)"
echo "  ✓ Created first snapshot"
echo "  ✓ Modified data (v2)"
echo "  ✓ Created second snapshot"
echo ""
echo "Snapshot 1 Verification:"
echo "  ✓ Restored volume from snapshot 1"
echo "  ✓ Version matched: '${V1_VERSION}'"
echo "  ✓ File count correct: ${V1_FILE_COUNT} files"
echo "  ✓ No v2 data (correct point-in-time)"
echo ""
echo "Snapshot 2 Verification:"
echo "  ✓ Restored volume from snapshot 2"
echo "  ✓ Version matched: '${V2_VERSION}'"
echo "  ✓ V2 data present: ${V2_V2_FILE_COUNT} files"
echo "  ✓ Modified data present (correct point-in-time)"
echo ""
echo "Independence Verification:"
echo "  ✓ Restored volumes independent of source"
echo "  ✓ Restored volumes independent of each other"
echo "  ✓ Snapshot deletion successful"
echo ""
echo "================================================"

# Verify metrics
verify_metrics

# Cleanup
cleanup_test "${POD_NAME_SOURCE}" "${PVC_NAME_SOURCE}"
cleanup_test "${POD_NAME_RESTORE1}" "${PVC_NAME_RESTORE1}"
cleanup_test "${POD_NAME_RESTORE2}" "${PVC_NAME_RESTORE2}"

# Success
test_summary "${PROTOCOL}" "PASSED"
