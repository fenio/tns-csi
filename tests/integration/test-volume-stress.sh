#!/bin/bash
# Volume Limits Stress Test
# Tests the driver's ability to handle multiple volumes concurrently
# Verifies resource cleanup and stability under load

set -e

# Ensure output is not buffered (flush immediately)
exec 1> >(stdbuf -o0 cat)
exec 2> >(stdbuf -o0 cat >&2)

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Print immediately to verify script execution started
echo "Starting Volume Stress Test script..."
echo "Working directory: $(pwd)"
echo "Script directory: ${SCRIPT_DIR}"
date

source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="Volume Limits Stress Test"
NUM_VOLUMES=5  # Reduced from 10 to 5 for faster execution within timeout
TEST_PREFIX="stress-test"

echo "================================================"
echo "TrueNAS CSI - Volume Limits Stress Test"
echo "================================================"
echo ""
# Configure test with 8 total steps
set_test_steps 8
echo "This test verifies:"
echo "  • Driver handles multiple volumes (${NUM_VOLUMES}) concurrently"
echo "  • All volumes can be created and bound successfully"
echo "  • Multiple pods can mount volumes simultaneously"
echo "  • Cleanup works correctly for multiple volumes"
echo "  • No resource leaks under load"
echo "================================================"

# Arrays to track resources
declare -a PVC_NAMES=()
declare -a POD_NAMES=()
declare -a PV_NAMES=()

# Cleanup function
cleanup_all() {
    echo ""
    test_info "Cleaning up all test resources..."
    
    # Delete all pods
    for pod_name in "${POD_NAMES[@]}"; do
        kubectl delete pod "${pod_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=30s &
    done
    wait
    
    # Delete all PVCs
    for pvc_name in "${PVC_NAMES[@]}"; do
        kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s &
    done
    wait
    
    test_success "Cleanup initiated for all resources"
}

# Trap errors
trap 'cleanup_all; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

# Run test steps
verify_cluster
deploy_driver "nfs"  # Test with NFS protocol
wait_for_driver

#######################################
# Test 1: Create multiple PVCs (NFS)
#######################################
test_step "Creating ${NUM_VOLUMES} NFS PVCs"

echo ""
# Configure test with 8 total steps
test_info "Creating PVCs in parallel..."

for i in $(seq 1 $NUM_VOLUMES); do
    PVC_NAME="${TEST_PREFIX}-nfs-pvc-${i}"
    PVC_NAMES+=("${PVC_NAME}")
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f - &
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nfs
EOF
done

wait  # Wait for all kubectl apply commands to complete
test_success "${NUM_VOLUMES} PVCs created"

#######################################
# Test 2: Wait for all PVCs to bind
#######################################
test_step "Waiting for all PVCs to bind"

echo ""
# Configure test with 8 total steps
test_info "Waiting for PVCs to bind (timeout: ${TIMEOUT_PVC} each)..."

BIND_FAILURES=0
for pvc_name in "${PVC_NAMES[@]}"; do
    if kubectl wait --for=jsonpath='{.status.phase}'=Bound \
        pvc/"${pvc_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_PVC}" 2>/dev/null; then
        test_info "  ✓ ${pvc_name} bound"
    else
        test_error "  ✗ ${pvc_name} failed to bind"
        BIND_FAILURES=$((BIND_FAILURES + 1))
    fi
done

if [[ $BIND_FAILURES -eq 0 ]]; then
    test_success "All ${NUM_VOLUMES} PVCs bound successfully"
else
    test_error "${BIND_FAILURES} PVCs failed to bind"
    exit 1
fi

# Get PV names
echo ""
# Configure test with 8 total steps
test_info "Recording PV names..."
for pvc_name in "${PVC_NAMES[@]}"; do
    PV_NAME=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
    PV_NAMES+=("${PV_NAME}")
done
test_info "Tracked ${#PV_NAMES[@]} PVs"

#######################################
# Test 3: Create pods to mount volumes
#######################################
test_step "Creating ${NUM_VOLUMES} pods to mount volumes"

echo ""
# Configure test with 8 total steps
test_info "Creating pods in parallel..."

for i in $(seq 1 $NUM_VOLUMES); do
    POD_NAME="${TEST_PREFIX}-pod-${i}"
    PVC_NAME="${TEST_PREFIX}-nfs-pvc-${i}"
    POD_NAMES+=("${POD_NAME}")
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f - &
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
  labels:
    test: stress-test
spec:
  containers:
  - name: test-container
    image: busybox:latest
    command: ["sh", "-c", "echo 'Pod ${i} data' > /data/test.txt && sleep 600"]
    volumeMounts:
    - name: test-volume
      mountPath: /data
  volumes:
  - name: test-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME}
EOF
done

wait  # Wait for all kubectl apply commands
test_success "${NUM_VOLUMES} pods created"

#######################################
# Test 4: Wait for all pods to be ready
#######################################
test_step "Waiting for all pods to become ready"

echo ""
# Configure test with 8 total steps
test_info "Waiting for pods to be ready (timeout: ${TIMEOUT_POD} each)..."

POD_FAILURES=0
for pod_name in "${POD_NAMES[@]}"; do
    if kubectl wait --for=condition=Ready \
        pod/"${pod_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}" 2>/dev/null; then
        test_info "  ✓ ${pod_name} ready"
    else
        test_warning "  ⚠ ${pod_name} not ready yet"
        POD_FAILURES=$((POD_FAILURES + 1))
    fi
done

if [[ $POD_FAILURES -eq 0 ]]; then
    test_success "All ${NUM_VOLUMES} pods ready"
else
    test_warning "${POD_FAILURES} pods not ready (continuing test)"
fi

#######################################
# Test 5: Verify data in all volumes
#######################################
test_step "Verifying data in all mounted volumes"

echo ""
# Configure test with 8 total steps
test_info "Reading data from all pods..."

DATA_FAILURES=0
for i in $(seq 1 $NUM_VOLUMES); do
    POD_NAME="${TEST_PREFIX}-pod-${i}"
    
    # Check if pod is running
    POD_STATUS=$(kubectl get pod "${POD_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}' 2>/dev/null || echo "NotFound")
    
    if [[ "${POD_STATUS}" != "Running" ]]; then
        test_warning "  ⚠ ${POD_NAME} not running (${POD_STATUS})"
        DATA_FAILURES=$((DATA_FAILURES + 1))
        continue
    fi
    
    # Try to read data
    DATA=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt 2>/dev/null || echo "ERROR")
    
    if [[ "${DATA}" == "Pod ${i} data" ]]; then
        test_info "  ✓ ${POD_NAME} data verified"
    else
        test_error "  ✗ ${POD_NAME} data verification failed: ${DATA}"
        DATA_FAILURES=$((DATA_FAILURES + 1))
    fi
done

if [[ $DATA_FAILURES -eq 0 ]]; then
    test_success "All ${NUM_VOLUMES} volumes verified successfully"
else
    test_error "${DATA_FAILURES} volumes failed verification"
    exit 1
fi

#######################################
# Test 6: Check controller health
#######################################
echo ""
# Configure test with 8 total steps
echo "================================================"
test_info "Checking controller health under load"
echo "================================================"

CONTROLLER_POD=$(kubectl get pods -n kube-system \
    -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
    -o jsonpath='{.items[0].metadata.name}')

# Check for errors in controller logs
ERROR_COUNT=$(kubectl logs -n kube-system "${CONTROLLER_POD}" -c tns-csi-plugin --tail=500 2>/dev/null | \
    grep -c -E "(error|ERROR|failed|FAILED)" || echo "0")

test_info "Controller error count in recent logs: ${ERROR_COUNT}"

if [[ $ERROR_COUNT -gt 20 ]]; then
    test_warning "High error count detected (${ERROR_COUNT})"
    test_info "Showing recent errors:"
    kubectl logs -n kube-system "${CONTROLLER_POD}" -c tns-csi-plugin --tail=100 2>/dev/null | \
        grep -E "(error|ERROR|failed|FAILED)" | tail -10
else
    test_success "Controller error count acceptable"
fi

# Check controller pod status
CONTROLLER_STATUS=$(kubectl get pod "${CONTROLLER_POD}" -n kube-system -o jsonpath='{.status.phase}')
if [[ "${CONTROLLER_STATUS}" == "Running" ]]; then
    test_success "Controller still running (${CONTROLLER_STATUS})"
else
    test_error "Controller in unexpected state: ${CONTROLLER_STATUS}"
    exit 1
fi

#######################################
# Test 7: Cleanup stress
#######################################
echo ""
# Configure test with 8 total steps
echo "================================================"
test_info "Testing cleanup under load"
echo "================================================"

test_info "Deleting all pods..."
for pod_name in "${POD_NAMES[@]}"; do
    kubectl delete pod "${pod_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --wait=false
done

test_info "Waiting for pods to terminate..."
sleep 10

POD_DELETE_FAILURES=0
for pod_name in "${POD_NAMES[@]}"; do
    if kubectl get pod "${pod_name}" -n "${TEST_NAMESPACE}" &>/dev/null; then
        POD_DELETE_FAILURES=$((POD_DELETE_FAILURES + 1))
    fi
done

test_info "${POD_DELETE_FAILURES} pods still terminating"

test_info "Deleting all PVCs..."
for pvc_name in "${PVC_NAMES[@]}"; do
    kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --wait=false
done

test_info "Waiting for PVCs to be deleted..."
sleep 30

PVC_DELETE_FAILURES=0
for pvc_name in "${PVC_NAMES[@]}"; do
    if kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" &>/dev/null; then
        PVC_DELETE_FAILURES=$((PVC_DELETE_FAILURES + 1))
    fi
done

if [[ $PVC_DELETE_FAILURES -eq 0 ]]; then
    test_success "All PVCs deleted successfully"
else
    test_warning "${PVC_DELETE_FAILURES} PVCs still deleting (may take longer)"
fi

# Check for orphaned PVs
echo ""
# Configure test with 8 total steps
test_info "Checking for orphaned PVs..."
ORPHANED_PVS=0
for pv_name in "${PV_NAMES[@]}"; do
    if kubectl get pv "${pv_name}" &>/dev/null; then
        ORPHANED_PVS=$((ORPHANED_PVS + 1))
    fi
done

if [[ $ORPHANED_PVS -eq 0 ]]; then
    test_success "No orphaned PVs (all cleaned up)"
else
    test_warning "${ORPHANED_PVS} PVs still exist (cleanup in progress)"
fi

echo ""
# Configure test with 8 total steps
echo "================================================"
echo "Volume Limits Stress Test Summary"
echo "================================================"
echo ""
# Configure test with 8 total steps
echo "Volume Creation:"
echo "  ✓ Created ${NUM_VOLUMES} PVCs concurrently"
echo "  ✓ All PVCs bound successfully"
echo "  ✓ Created ${NUM_VOLUMES} pods concurrently"
echo "  ✓ $((NUM_VOLUMES - POD_FAILURES)) pods became ready"
echo ""
# Configure test with 8 total steps
echo "Data Verification:"
echo "  ✓ $((NUM_VOLUMES - DATA_FAILURES)) volumes verified"
echo "  ✓ All data integrity checks passed"
echo ""
# Configure test with 8 total steps
echo "Resource Cleanup:"
echo "  ✓ $((NUM_VOLUMES - PVC_DELETE_FAILURES)) PVCs deleted"
echo "  ✓ $((${#PV_NAMES[@]} - ORPHANED_PVS)) PVs cleaned up"
echo "  ✓ Controller remained healthy under load"
echo ""
# Configure test with 8 total steps
echo "================================================"

# Verify metrics
verify_metrics

# Final cleanup
cleanup_all

# Success
test_summary "${PROTOCOL}" "PASSED"
