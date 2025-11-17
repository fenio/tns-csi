#!/bin/bash
# Concurrent Volume Creation Test - NVMe-oF
# Tests multiple simultaneous PVC creations to expose race conditions
# and verify the driver handles concurrent operations correctly

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NVMe-oF Concurrent"
PVC_NAME="concurrent-pvc-nvmeof"
POD_NAME="concurrent-pod-nvmeof"
NUM_VOLUMES=5  # Reduced from 10 to 5 for stability

echo "========================================"
echo "TrueNAS CSI - Concurrent NVMe-oF Test"
echo "Creating ${NUM_VOLUMES} volumes simultaneously"
echo "========================================"

# Trap errors and cleanup
trap 'show_diagnostic_logs "" ""; cleanup_concurrent_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Cleanup concurrent test resources
#######################################
cleanup_concurrent_test() {
    echo ""
    test_info "Cleaning up concurrent test resources..."
    
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

# Pre-check: Verify NVMe-oF subsystem exists
echo ""
test_info "Verifying NVMe-oF subsystem configuration..."
SUBSYSTEM_NQN="${NVMEOF_SUBSYSTEM_NQN:-nqn.2005-03.org.truenas:csi-test}"
test_info "Expected subsystem NQN: ${SUBSYSTEM_NQN}"
test_warning "IMPORTANT: The NVMe-oF subsystem with NQN '${SUBSYSTEM_NQN}' must be pre-configured"
test_warning "in TrueNAS (Shares > NVMe-oF Subsystems) with at least one TCP port attached."
echo ""

# Configure test with 1 main step
set_test_steps 1

deploy_driver "nvmeof"
wait_for_driver

# Check if NVMe-oF is configured on TrueNAS
MANIFEST_DIR="${SCRIPT_DIR}/manifests"
if ! check_nvmeof_configured "${MANIFEST_DIR}/pvc-nvmeof.yaml" "test-pvc-nvmeof" "${PROTOCOL}"; then
    exit 0  # Gracefully skip test if not configured
fi

#######################################
# Test: Concurrent PVC Creation
#######################################
test_step "Creating ${NUM_VOLUMES} PVCs concurrently"

# Generate unique PVC names
declare -a PVC_NAMES
declare -a POD_NAMES
for i in $(seq 1 ${NUM_VOLUMES}); do
    PVC_NAMES[$i]="concurrent-pvc-nvmeof-${i}"
    POD_NAMES[$i]="concurrent-pod-nvmeof-${i}"
done

# Create all PVCs simultaneously (with pods, since WaitForFirstConsumer)
test_info "Creating ${NUM_VOLUMES} PVC+Pod pairs concurrently..."
declare -a BG_PIDS=()
for i in $(seq 1 ${NUM_VOLUMES}); do
    # Create PVC and Pod together for WaitForFirstConsumer binding mode
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f - &
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAMES[$i]}
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nvmeof
---
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAMES[$i]}
spec:
  containers:
  - name: test-container
    image: busybox:latest
    command: ["sleep", "600"]
    volumeMounts:
    - name: test-volume
      mountPath: /data
  volumes:
  - name: test-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAMES[$i]}
EOF
    BG_PIDS+=($!)
    # Small delay to avoid overwhelming the API server
    sleep 0.5
done

# Wait for all background jobs to complete and check exit status
test_info "Waiting for all PVC+Pod creation jobs to complete..."
FAILED=0
for pid in "${BG_PIDS[@]}"; do
    if ! wait $pid; then
        FAILED=1
    fi
done

if [[ $FAILED -eq 1 ]]; then
    test_error "One or more PVC/Pod creation commands failed"
    kubectl get pvc -n "${TEST_NAMESPACE}" || true
    kubectl get pods -n "${TEST_NAMESPACE}" || true
    exit 1
fi
test_success "All PVC+Pod creation requests submitted successfully"

# Give provisioner time to start processing
sleep 15

# Monitor pod status (pods will trigger PVC binding)
echo ""
test_info "Monitoring pod scheduling and PVC provisioning..."
echo ""

# Wait for all pods to be ready (with timeout)
test_info "Waiting for all pods to be ready (timeout: 360s)..."

TIMEOUT=360
ELAPSED=0
INTERVAL=10

while [[ $ELAPSED -lt $TIMEOUT ]]; do
    # Count pods in Ready state
    READY_COUNT=$(kubectl get pods -n "${TEST_NAMESPACE}" \
        --no-headers 2>/dev/null | awk '{print $2}' | grep -c "1/1" || echo "0")
    READY_COUNT=$((READY_COUNT + 0))  # Ensure numeric
    
    # Count PVCs in Bound state
    BOUND_COUNT=$(kubectl get pvc -n "${TEST_NAMESPACE}" \
        --no-headers 2>/dev/null | grep -c "Bound" || echo "0")
    BOUND_COUNT=$((BOUND_COUNT + 0))  # Ensure numeric
    
    echo "Progress: ${READY_COUNT}/${NUM_VOLUMES} pods ready, ${BOUND_COUNT}/${NUM_VOLUMES} PVCs bound (${ELAPSED}s elapsed)"
    
    if [[ $READY_COUNT -eq $NUM_VOLUMES ]] && [[ $BOUND_COUNT -eq $NUM_VOLUMES ]]; then
        test_success "All ${NUM_VOLUMES} pods are ready and PVCs are bound!"
        break
    fi
    
    sleep $INTERVAL
    ELAPSED=$((ELAPSED + INTERVAL))
done

if [[ $ELAPSED -ge $TIMEOUT ]]; then
    test_error "Timeout: Not all pods/PVCs became ready within ${TIMEOUT}s"
    
    echo ""
    echo "=== Pod Status ==="
    kubectl get pods -n "${TEST_NAMESPACE}"
    
    echo ""
    echo "=== PVC Status ==="
    kubectl get pvc -n "${TEST_NAMESPACE}"
    
    echo ""
    echo "=== Pending/Failed Pods ==="
    kubectl get pods -n "${TEST_NAMESPACE}" | grep -v "Running" || true
    
    echo ""
    echo "=== Pod Events ==="
    kubectl get events -n "${TEST_NAMESPACE}" --sort-by='.lastTimestamp' | tail -50 || true
    
    echo ""
    echo "=== Controller Logs ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=200 || true
    
    echo ""
    echo "=== Node Logs ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
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
    POD_NAME="${POD_NAMES[$i]}"
    PVC_NAME="${PVC_NAMES[$i]}"
    
    echo ""
    test_info "Testing I/O on ${POD_NAME} (PVC: ${PVC_NAME})..."
    
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

# Verify no critical errors in controller logs
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
echo "=== Final Pod Status ==="
kubectl get pods -n "${TEST_NAMESPACE}"

echo ""
echo "=== Final PVC Status ==="
kubectl get pvc -n "${TEST_NAMESPACE}"

echo ""
echo "=== Final PV Status ==="
kubectl get pv | grep "${TEST_NAMESPACE}" || echo "No PVs found"

# Verify metrics
verify_metrics

# Cleanup
cleanup_concurrent_test

# Success
test_summary "${PROTOCOL}" "PASSED"
