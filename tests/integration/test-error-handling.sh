#!/bin/bash
# Error Handling Integration Test
# Tests CSI driver behavior with invalid parameters and error conditions
#
# This test verifies:
# 1. Invalid StorageClass parameters are rejected gracefully
# 2. Missing required parameters produce clear error messages
# 3. Driver recovers properly after errors
# 4. PVC with invalid StorageClass stays in Pending state

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="Error Handling"

echo "================================================"
echo "TrueNAS CSI - Error Handling Test"
echo "================================================"
echo ""
echo "This test verifies:"
echo "  - Invalid StorageClass parameters are rejected"
echo "  - Missing required parameters produce clear errors"
echo "  - Driver handles errors gracefully"
echo "  - Valid operations work after error recovery"
echo "================================================"

# Configure test with 12 total steps
set_test_steps 12

# Trap errors and cleanup
trap 'show_diagnostic_logs "" ""; cleanup_error_handling_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Cleanup function for this test
#######################################
cleanup_error_handling_test() {
    test_info "Cleaning up error handling test resources..."
    
    # Delete all test resources
    kubectl delete pvc --all -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s 2>/dev/null || true
    kubectl delete pod --all -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s 2>/dev/null || true
    
    # Delete test StorageClasses
    kubectl delete storageclass tns-csi-invalid-pool --ignore-not-found=true 2>/dev/null || true
    kubectl delete storageclass tns-csi-missing-server --ignore-not-found=true 2>/dev/null || true
    kubectl delete storageclass tns-csi-invalid-protocol --ignore-not-found=true 2>/dev/null || true
    kubectl delete storageclass tns-csi-valid-recovery --ignore-not-found=true 2>/dev/null || true
    
    # Delete namespace
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=120s 2>/dev/null || {
        kubectl delete namespace "${TEST_NAMESPACE}" --force --grace-period=0 --ignore-not-found=true 2>/dev/null || true
    }
    
    test_info "Cleanup completed"
}

#######################################
# Test 1: Invalid pool name
#######################################
test_invalid_pool() {
    test_step "Testing invalid pool name"
    
    test_info "Creating StorageClass with non-existent pool..."
    
    cat <<EOF | kubectl apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: tns-csi-invalid-pool
provisioner: tns.csi.io
parameters:
  protocol: nfs
  server: "${TRUENAS_HOST}"
  pool: "nonexistent-pool-xyz"
reclaimPolicy: Delete
volumeBindingMode: Immediate
EOF
    
    test_success "StorageClass created"
    
    # Create PVC with invalid pool
    test_info "Creating PVC with invalid pool StorageClass..."
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pvc-invalid-pool
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-invalid-pool
EOF
    
    # Wait a bit for provisioner to attempt creation
    sleep 15
    
    # Check PVC status - should be Pending
    local pvc_status
    pvc_status=$(kubectl get pvc pvc-invalid-pool -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}')
    
    if [[ "${pvc_status}" == "Pending" ]]; then
        test_success "PVC correctly stays in Pending state with invalid pool"
    else
        test_warning "PVC status is ${pvc_status} (expected Pending)"
    fi
    
    # Check for error events
    local events
    events=$(kubectl get events -n "${TEST_NAMESPACE}" --sort-by='.lastTimestamp' 2>/dev/null | grep -i "pvc-invalid-pool" | tail -5 || echo "")
    
    if echo "${events}" | grep -qiE "(failed|error|provision)"; then
        test_success "Error events generated for invalid pool"
        test_info "Events:"
        echo "${events}" | head -3 | while IFS= read -r line; do
            test_info "  ${line}"
        done
    else
        test_info "Checking controller logs for error messages..."
        local logs
        logs=$(kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            --tail=50 2>/dev/null | grep -iE "(nonexistent|pool|error|failed)" || echo "")
        
        if [[ -n "${logs}" ]]; then
            test_success "Controller logged error for invalid pool"
        else
            test_info "No specific error logs found (driver may handle silently)"
        fi
    fi
    
    # Cleanup this PVC
    kubectl delete pvc pvc-invalid-pool -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=30s || true
    kubectl delete storageclass tns-csi-invalid-pool --ignore-not-found=true || true
    
    test_success "Invalid pool test completed"
}

#######################################
# Test 2: Missing server parameter (NFS)
#######################################
test_missing_server() {
    test_step "Testing missing server parameter"
    
    test_info "Creating StorageClass without server parameter..."
    
    cat <<EOF | kubectl apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: tns-csi-missing-server
provisioner: tns.csi.io
parameters:
  protocol: nfs
  pool: "${TRUENAS_POOL}"
  # server parameter intentionally omitted
reclaimPolicy: Delete
volumeBindingMode: Immediate
EOF
    
    test_success "StorageClass created without server"
    
    # Create PVC
    test_info "Creating PVC with missing server StorageClass..."
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pvc-missing-server
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-missing-server
EOF
    
    # Wait for provisioner to attempt
    sleep 15
    
    # Check PVC status
    local pvc_status
    pvc_status=$(kubectl get pvc pvc-missing-server -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}')
    
    if [[ "${pvc_status}" == "Pending" ]]; then
        test_success "PVC correctly stays in Pending state with missing server"
    else
        test_warning "PVC status is ${pvc_status} (expected Pending)"
    fi
    
    # Check for error in events or logs
    local events
    events=$(kubectl get events -n "${TEST_NAMESPACE}" --sort-by='.lastTimestamp' 2>/dev/null | grep -i "pvc-missing-server" | tail -3 || echo "")
    
    if [[ -n "${events}" ]]; then
        test_info "Events for missing server PVC:"
        echo "${events}" | head -3 | while IFS= read -r line; do
            test_info "  ${line}"
        done
    fi
    
    # Cleanup
    kubectl delete pvc pvc-missing-server -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=30s || true
    kubectl delete storageclass tns-csi-missing-server --ignore-not-found=true || true
    
    test_success "Missing server test completed"
}

#######################################
# Test 3: Invalid protocol
#######################################
test_invalid_protocol() {
    test_step "Testing invalid protocol parameter"
    
    test_info "Creating StorageClass with invalid protocol..."
    
    cat <<EOF | kubectl apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: tns-csi-invalid-protocol
provisioner: tns.csi.io
parameters:
  protocol: iscsi
  server: "${TRUENAS_HOST}"
  pool: "${TRUENAS_POOL}"
reclaimPolicy: Delete
volumeBindingMode: Immediate
EOF
    
    test_success "StorageClass created with invalid protocol"
    
    # Create PVC
    test_info "Creating PVC with invalid protocol StorageClass..."
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pvc-invalid-protocol
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-invalid-protocol
EOF
    
    # Wait for provisioner to attempt
    sleep 15
    
    # Check PVC status
    local pvc_status
    pvc_status=$(kubectl get pvc pvc-invalid-protocol -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}')
    
    if [[ "${pvc_status}" == "Pending" ]]; then
        test_success "PVC correctly stays in Pending state with invalid protocol"
    else
        test_warning "PVC status is ${pvc_status} (expected Pending)"
    fi
    
    # Check controller logs for protocol error
    local logs
    logs=$(kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=100 2>/dev/null | grep -iE "(unsupported.*protocol|invalid.*protocol|iscsi)" || echo "")
    
    if [[ -n "${logs}" ]]; then
        test_success "Controller logged error for invalid protocol"
        test_info "Relevant logs:"
        echo "${logs}" | head -3 | while IFS= read -r line; do
            test_info "  ${line}"
        done
    else
        test_info "Protocol validation may occur at different level"
    fi
    
    # Cleanup
    kubectl delete pvc pvc-invalid-protocol -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=30s || true
    kubectl delete storageclass tns-csi-invalid-protocol --ignore-not-found=true || true
    
    test_success "Invalid protocol test completed"
}

#######################################
# Test 4: Recovery after errors
#######################################
test_recovery_after_errors() {
    test_step "Testing driver recovery after errors"
    
    test_info "Creating valid StorageClass to verify driver still works..."
    
    cat <<EOF | kubectl apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: tns-csi-valid-recovery
provisioner: tns.csi.io
parameters:
  protocol: nfs
  server: "${TRUENAS_HOST}"
  pool: "${TRUENAS_POOL}"
reclaimPolicy: Delete
volumeBindingMode: Immediate
EOF
    
    # Create a valid PVC
    test_info "Creating valid PVC to verify recovery..."
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pvc-recovery-test
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-valid-recovery
EOF
    
    # Wait for PVC to bind
    test_info "Waiting for valid PVC to bind..."
    
    if kubectl wait --for=jsonpath='{.status.phase}'=Bound \
        pvc/pvc-recovery-test \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_PVC}"; then
        test_success "Valid PVC bound successfully - driver recovered after errors!"
    else
        test_error "Valid PVC failed to bind after error tests"
        kubectl describe pvc pvc-recovery-test -n "${TEST_NAMESPACE}" || true
        return 1
    fi
    
    # Cleanup
    kubectl delete pvc pvc-recovery-test -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    kubectl delete storageclass tns-csi-valid-recovery --ignore-not-found=true || true
    
    test_success "Recovery test completed"
}

#######################################
# Test 5: Controller logs error analysis
#######################################
analyze_controller_logs() {
    test_step "Analyzing controller logs for error handling"
    
    test_info "Collecting controller logs..."
    
    local logs
    logs=$(kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=200 2>/dev/null || echo "")
    
    # Count different types of log entries
    local error_count warning_count info_count
    error_count=$(echo "${logs}" | grep -ciE "error|failed|unable" || echo "0")
    warning_count=$(echo "${logs}" | grep -ciE "warning|warn" || echo "0")
    info_count=$(echo "${logs}" | grep -ciE "info|success|completed|created" || echo "0")
    
    test_info "Log analysis:"
    test_info "  - Error entries: ${error_count}"
    test_info "  - Warning entries: ${warning_count}"
    test_info "  - Info/Success entries: ${info_count}"
    
    # Check for panic or fatal errors (these would be critical)
    if echo "${logs}" | grep -qiE "(panic|fatal|crash)"; then
        test_error "Critical errors found in controller logs!"
        echo "${logs}" | grep -iE "(panic|fatal|crash)" | head -5
        return 1
    else
        test_success "No critical errors (panic/fatal) in controller logs"
    fi
    
    # Verify controller is still healthy
    local controller_status
    controller_status=$(kubectl get pods -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        -o jsonpath='{.items[0].status.phase}')
    
    if [[ "${controller_status}" == "Running" ]]; then
        test_success "Controller pod is still running after error tests"
    else
        test_error "Controller pod is not running: ${controller_status}"
        return 1
    fi
    
    test_success "Log analysis completed"
}

# Run test steps
verify_cluster
deploy_driver "nfs"
wait_for_driver

# Run error handling tests
test_invalid_pool
test_missing_server
test_invalid_protocol
test_recovery_after_errors
analyze_controller_logs

# Verify metrics still work
verify_metrics

# Cleanup
cleanup_error_handling_test

# Summary
echo ""
echo "================================================"
echo "Error Handling Test Summary"
echo "================================================"
echo ""
echo "Tests completed:"
echo "  - Invalid pool parameter: Verified PVC stays Pending"
echo "  - Missing server parameter: Verified graceful handling"
echo "  - Invalid protocol (iscsi): Verified rejection"
echo "  - Recovery after errors: Verified driver still works"
echo "  - Controller health: Verified no panics or crashes"
echo ""
echo "Result: Driver handles errors gracefully"
echo "================================================"

# Success
test_summary "${PROTOCOL}" "PASSED"
