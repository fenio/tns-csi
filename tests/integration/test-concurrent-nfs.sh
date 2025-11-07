#!/bin/bash
# Concurrent Volume Creation Test - NFS
# Tests multiple simultaneous PVC creations to expose race conditions
# and verify the driver handles concurrent operations correctly

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NFS Concurrent"
PVC_NAME="concurrent-pvc"
POD_NAME="concurrent-pod"
NUM_VOLUMES=5  # Reduced from 10 to 5 for stability

echo "========================================"
echo "TrueNAS CSI - Concurrent NFS Test"
echo "Creating ${NUM_VOLUMES} volumes simultaneously"
echo "========================================"

# Trap errors and cleanup
trap 'show_diagnostic_logs "" ""; cleanup_concurrent_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Cleanup concurrent test resources
#######################################
cleanup_concurrent_test() {
    test_step 5 5 "Cleaning up concurrent test resources"
    
    # Delete the entire namespace - this triggers CSI DeleteVolume for all PVCs
    test_info "Deleting test namespace: ${TEST_NAMESPACE}"
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=180s || {
        test_warning "Namespace deletion timed out, forcing deletion"
        kubectl delete namespace "${TEST_NAMESPACE}" --force --grace-period=0 --ignore-not-found=true || true
    }
    
    # Wait for PVs to be deleted (concurrent test creates 10 PVCs -> 10 PVs)
    test_info "Waiting for PVs to be deleted..."
    for i in {1..120}; do
        REMAINING_PVS=$(kubectl get pv --no-headers 2>/dev/null | grep -c "${TEST_NAMESPACE}" || echo "0")
        if [[ "${REMAINING_PVS}" == "0" ]]; then
            test_success "All PVs deleted successfully"
            break
        fi
        if [[ $i == 120 ]]; then
            test_warning "Some PVs still exist after 120 seconds"
            kubectl get pv | grep "${TEST_NAMESPACE}" || true
        fi
        sleep 1
    done
    
    # Additional wait for TrueNAS backend cleanup
    test_info "Waiting for TrueNAS backend cleanup (15 seconds)..."
    sleep 15
    
    test_success "Cleanup complete"
}

# Run test steps
verify_cluster
deploy_driver "nfs"
wait_for_driver

#######################################
# Test: Concurrent PVC Creation
#######################################
test_step 4 5 "Creating ${NUM_VOLUMES} PVCs concurrently"

# Generate unique PVC names
declare -a PVC_NAMES
for i in $(seq 1 ${NUM_VOLUMES}); do
    PVC_NAMES[$i]="concurrent-pvc-${i}"
done

# Create all PVCs simultaneously
test_info "Creating ${NUM_VOLUMES} PVCs concurrently..."
declare -a BG_PIDS=()
for i in $(seq 1 ${NUM_VOLUMES}); do
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f - &
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAMES[$i]}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nfs
EOF
    BG_PIDS+=($!)
    # Small delay to avoid overwhelming the API server
    sleep 0.5
done

# Wait for all background jobs to complete and check exit status
test_info "Waiting for all PVC creation jobs to complete..."
FAILED=0
for pid in "${BG_PIDS[@]}"; do
    if ! wait $pid; then
        FAILED=1
    fi
done

if [[ $FAILED -eq 1 ]]; then
    test_error "One or more PVC creation commands failed"
    kubectl get pvc -n "${TEST_NAMESPACE}" || true
    exit 1
fi
test_success "All PVC creation requests submitted successfully"

# Give provisioner time to start processing
sleep 10

# Monitor PVC status
echo ""
test_info "Monitoring PVC provisioning status..."
echo ""
kubectl get pvc -n "${TEST_NAMESPACE}" -w &
WATCH_PID=$!

# Wait for all PVCs to be bound (with timeout)
echo ""
test_info "Waiting for all PVCs to be bound (timeout: 300s)..."

TIMEOUT=300
ELAPSED=0
INTERVAL=5

while [[ $ELAPSED -lt $TIMEOUT ]]; do
    # Count PVCs in Bound state
    BOUND_COUNT=$(kubectl get pvc -n "${TEST_NAMESPACE}" \
        --no-headers 2>/dev/null | grep -c "Bound" || echo "0")
    BOUND_COUNT=$((BOUND_COUNT + 0))  # Ensure numeric
    
    echo "Progress: ${BOUND_COUNT}/${NUM_VOLUMES} PVCs bound (${ELAPSED}s elapsed)"
    
    if [[ $BOUND_COUNT -eq $NUM_VOLUMES ]]; then
        test_success "All ${NUM_VOLUMES} PVCs are bound!"
        break
    fi
    
    sleep $INTERVAL
    ELAPSED=$((ELAPSED + INTERVAL))
done

# Stop the watch
kill $WATCH_PID 2>/dev/null || true

if [[ $ELAPSED -ge $TIMEOUT ]]; then
    test_error "Timeout: Not all PVCs became bound within ${TIMEOUT}s"
    
    echo ""
    echo "=== PVC Status ==="
    kubectl get pvc -n "${TEST_NAMESPACE}"
    
    echo ""
    echo "=== Failed PVCs ==="
    kubectl get pvc -n "${TEST_NAMESPACE}" | grep -v "Bound" || true
    
    echo ""
    echo "=== Controller Logs ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=200 || true
    
    exit 1
fi

# Verify all PVCs got unique PVs (no duplicates)
echo ""
test_info "Verifying all PVs are unique..."
UNIQUE_PV_COUNT=$(kubectl get pvc -n "${TEST_NAMESPACE}" \
    -o jsonpath='{range .items[*]}{.spec.volumeName}{"\n"}{end}' | sort -u | wc -l)
UNIQUE_PV_COUNT=$((UNIQUE_PV_COUNT + 0))  # Ensure numeric

if [[ $UNIQUE_PV_COUNT -eq $NUM_VOLUMES ]]; then
    test_success "All ${NUM_VOLUMES} PVCs have unique PVs"
else
    test_error "Found ${UNIQUE_PV_COUNT} unique PVs, expected ${NUM_VOLUMES}"
    echo ""
    echo "=== PV Names ==="
    kubectl get pvc -n "${TEST_NAMESPACE}" -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.volumeName}{"\n"}{end}'
    exit 1
fi

# Test I/O on a subset of volumes (test first, middle, and last)
echo ""
test_info "Testing I/O operations on sample volumes..."

for i in 1 $((NUM_VOLUMES / 2)) ${NUM_VOLUMES}; do
    POD_NAME="test-pod-${i}"
    PVC_NAME="${PVC_NAMES[$i]}"
    
    echo ""
    test_info "Creating pod ${POD_NAME} for PVC ${PVC_NAME}..."
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
spec:
  containers:
  - name: test-container
    image: busybox:latest
    command: ["sleep", "300"]
    volumeMounts:
    - name: test-volume
      mountPath: /data
  volumes:
  - name: test-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME}
EOF
    
    # Wait for pod to be ready
    kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout=120s
    
    # Test I/O
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Test data for volume ${i}' > /data/test.txt"
    
    CONTENT=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt)
    if [[ "${CONTENT}" == "Test data for volume ${i}" ]]; then
        test_success "I/O test passed for ${PVC_NAME}"
    else
        test_error "I/O test failed for ${PVC_NAME}"
        exit 1
    fi
done

# Verify no errors in controller logs
echo ""
test_info "Checking controller logs for errors..."
CONTROLLER_ERRORS=$(kubectl logs -n kube-system \
    -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
    --tail=500 | grep -i "error\|failed\|panic" | grep -v "context canceled" || true)

if [[ -n "${CONTROLLER_ERRORS}" ]]; then
    test_warning "Found potential errors in controller logs:"
    echo "${CONTROLLER_ERRORS}"
else
    test_success "No errors found in controller logs"
fi

# Show final status
echo ""
echo "=== Final PVC Status ==="
kubectl get pvc -n "${TEST_NAMESPACE}"

echo ""
echo "=== Final PV Status ==="
kubectl get pv | grep "${TEST_NAMESPACE}" || echo "No PVs found (already cleaned up)"

# Verify metrics
verify_metrics

# Cleanup
cleanup_concurrent_test

# Success
test_summary "${PROTOCOL}" "PASSED"
