#!/bin/bash
# Common test library for CSI driver integration tests
# Provides standardized functions for deploying, testing, and cleaning up

set -e

# Colors for output
export GREEN='\033[0;32m'
export YELLOW='\033[1;33m'
export RED='\033[0;31m'
export BLUE='\033[0;34m'
export CYAN='\033[0;36m'
export NC='\033[0m' # No Color

# Test configuration
export TEST_NAMESPACE="${TEST_NAMESPACE:-default}"
export TIMEOUT_PVC="${TIMEOUT_PVC:-120s}"
export TIMEOUT_POD="${TIMEOUT_POD:-120s}"
export TIMEOUT_DRIVER="${TIMEOUT_DRIVER:-120s}"

#######################################
# Print a test step header
# Arguments:
#   Step number
#   Total steps
#   Description
#######################################
test_step() {
    local step=$1
    local total=$2
    local description=$3
    echo ""
    echo -e "${BLUE}[Step ${step}/${total}]${NC} ${description}"
    echo ""
}

#######################################
# Print success message
# Arguments:
#   Message
#######################################
test_success() {
    echo -e "${GREEN}✓${NC} $1"
}

#######################################
# Print error message
# Arguments:
#   Message
#######################################
test_error() {
    echo -e "${RED}✗${NC} $1"
}

#######################################
# Print warning message
# Arguments:
#   Message
#######################################
test_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

#######################################
# Print info message
# Arguments:
#   Message
#######################################
test_info() {
    echo -e "${CYAN}ℹ${NC} $1"
}

#######################################
# Verify cluster is accessible
#######################################
verify_cluster() {
    test_step 1 8 "Verifying cluster access"
    
    if ! kubectl cluster-info &>/dev/null; then
        test_error "Cannot access cluster"
        return 1
    fi
    
    test_success "Cluster is accessible"
    kubectl get nodes
}

#######################################
# Deploy CSI driver using Helm
# Arguments:
#   Protocol (nfs, nvmeof, iscsi)
#   Additional helm values (optional)
#######################################
deploy_driver() {
    local protocol=$1
    shift
    local helm_args=("$@")
    
    test_step 2 8 "Deploying CSI driver for ${protocol}"
    
    # Check required environment variables
    if [[ -z "${TRUENAS_HOST}" ]]; then
        test_error "TRUENAS_HOST environment variable not set"
        return 1
    fi
    
    if [[ -z "${TRUENAS_API_KEY}" ]]; then
        test_error "TRUENAS_API_KEY environment variable not set"
        return 1
    fi
    
    if [[ -z "${TRUENAS_POOL}" ]]; then
        test_error "TRUENAS_POOL environment variable not set"
        return 1
    fi
    
    # Construct TrueNAS WebSocket URL
    local truenas_url="wss://${TRUENAS_HOST}/api/current"
    test_info "TrueNAS URL: ${truenas_url}"
    
    # Base Helm values
    local base_args=(
        --namespace kube-system
        --create-namespace
        --set image.repository=bfenski/tns-csi
        --set image.tag=latest
        --set image.pullPolicy=Never
        --set truenas.url="${truenas_url}"
        --set truenas.apiKey="${TRUENAS_API_KEY}"
    )
    
    # Protocol-specific configuration
    case "${protocol}" in
        nfs)
            base_args+=(
                --set storageClasses.nfs.enabled=true
                --set storageClasses.nfs.name=tns-csi-nfs
                --set storageClasses.nfs.pool="${TRUENAS_POOL}"
                --set storageClasses.nfs.server="${TRUENAS_HOST}"
                --set storageClasses.nvmeof.enabled=false
            )
            ;;
        nvmeof)
            base_args+=(
                --set storageClasses.nfs.enabled=false
                --set storageClasses.nvmeof.enabled=true
                --set storageClasses.nvmeof.name=tns-nvmeof
                --set storageClasses.nvmeof.pool="${TRUENAS_POOL}"
                --set storageClasses.nvmeof.server="${TRUENAS_HOST}"
                --set storageClasses.nvmeof.transport=tcp
                --set storageClasses.nvmeof.port=4420
            )
            ;;
        iscsi)
            base_args+=(
                --set storageClasses.nfs.enabled=false
                --set storageClasses.nvmeof.enabled=false
                --set storageClasses.iscsi.enabled=true
                --set storageClasses.iscsi.name=tns-iscsi
                --set storageClasses.iscsi.pool="${TRUENAS_POOL}"
                --set storageClasses.iscsi.server="${TRUENAS_HOST}"
            )
            ;;
        *)
            test_error "Unknown protocol: ${protocol}"
            return 1
            ;;
    esac
    
    # Deploy with Helm
    helm upgrade --install tns-csi ./charts/tns-csi-driver \
        "${base_args[@]}" \
        "${helm_args[@]}" \
        --wait --timeout 5m
    
    test_success "CSI driver deployed"
    
    # Verify deployment
    echo ""
    echo "=== Helm deployment status ==="
    helm list -n kube-system
    
    echo ""
    echo "=== CSI driver pods ==="
    kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver
}

#######################################
# Wait for CSI driver to be ready
#######################################
wait_for_driver() {
    test_step 3 8 "Waiting for CSI driver to be ready"
    
    kubectl wait --for=condition=Ready pod \
        -l app.kubernetes.io/name=tns-csi-driver \
        -n kube-system \
        --timeout="${TIMEOUT_DRIVER}"
    
    test_success "CSI driver is ready"
    
    # Verify image version
    echo ""
    echo "=== Driver image version ==="
    kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver \
        -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.containers[*].image}{"\n"}{end}'
}

#######################################
# Create PVC from manifest
# Arguments:
#   Manifest file path
#   PVC name
#   Wait for binding (optional, default: true, set to "false" for WaitForFirstConsumer)
#######################################
create_pvc() {
    local manifest=$1
    local pvc_name=$2
    local wait_for_binding="${3:-true}"
    
    test_step 4 8 "Creating PersistentVolumeClaim: ${pvc_name}"
    
    kubectl apply -f "${manifest}"
    
    # Give it a moment to start provisioning
    sleep 5
    
    # Check PVC status
    echo ""
    echo "=== PVC Status ==="
    kubectl describe pvc "${pvc_name}" -n "${TEST_NAMESPACE}"
    
    # Check controller logs
    echo ""
    echo "=== Controller Logs (last 30 lines) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=30 || true
    
    # Wait for PVC to be bound (skip if volumeBindingMode is WaitForFirstConsumer)
    if [[ "${wait_for_binding}" == "true" ]]; then
        echo ""
        test_info "Waiting for PVC to be bound (timeout: ${TIMEOUT_PVC})..."
        kubectl wait --for=jsonpath='{.status.phase}'=Bound \
            pvc/"${pvc_name}" \
            -n "${TEST_NAMESPACE}" \
            --timeout="${TIMEOUT_PVC}"
        
        test_success "PVC is bound"
        
        # Get PV name
        local pv_name
        pv_name=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
        test_info "Created PV: ${pv_name}"
    else
        echo ""
        test_info "Skipping PVC binding wait (volumeBindingMode: WaitForFirstConsumer)"
        test_success "PVC created (will bind when pod is scheduled)"
    fi
}

#######################################
# Create test pod from manifest
# Arguments:
#   Manifest file path
#   Pod name
#######################################
create_test_pod() {
    local manifest=$1
    local pod_name=$2
    
    test_step 5 8 "Creating test pod: ${pod_name}"
    
    kubectl apply -f "${manifest}"
    
    # Wait for pod to be ready
    echo ""
    test_info "Waiting for pod to be ready (timeout: ${TIMEOUT_POD})..."
    
    if ! kubectl wait --for=condition=Ready pod/"${pod_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"; then
        
        test_error "Pod failed to become ready"
        
        echo ""
        echo "=== Pod Status ==="
        kubectl describe pod "${pod_name}" -n "${TEST_NAMESPACE}" || true
        
        echo ""
        echo "=== Pod Events ==="
        kubectl get events -n "${TEST_NAMESPACE}" \
            --field-selector involvedObject.name="${pod_name}" \
            --sort-by='.lastTimestamp' || true
        
        echo ""
        echo "=== Node Logs ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
            --tail=50 || true
        
        return 1
    fi
    
    test_success "Pod is ready"
    
    # Show pod logs
    echo ""
    echo "=== Pod Logs ==="
    kubectl logs "${pod_name}" -n "${TEST_NAMESPACE}" || true
}

#######################################
# Run I/O tests on the mounted volume
# Arguments:
#   Pod name
#   Mount path or device path
#   Test type (filesystem or block)
#######################################
test_io_operations() {
    local pod_name=$1
    local path=$2
    local test_type=${3:-filesystem}
    
    test_step 6 8 "Testing I/O operations (${test_type})"
    
    if [[ "${test_type}" == "filesystem" ]]; then
        # Filesystem tests
        echo "Writing test file..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            sh -c "echo 'CSI Test Data' > ${path}/test.txt"
        test_success "Write operation successful"
        
        echo ""
        echo "Reading test file..."
        local content
        content=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "${path}/test.txt")
        if [[ "${content}" == "CSI Test Data" ]]; then
            test_success "Read operation successful: ${content}"
        else
            test_error "Read verification failed: expected 'CSI Test Data', got '${content}'"
            return 1
        fi
        
        echo ""
        echo "Writing large test file (100MB)..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            dd if=/dev/zero of="${path}/iotest.bin" bs=1M count=100 2>&1 | tail -3
        test_success "Large file write successful"
        
        echo ""
        echo "Verifying file size..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            ls -lh "${path}/"
        test_success "I/O operations completed successfully"
        
    elif [[ "${test_type}" == "block" ]]; then
        # Block device tests
        echo "Writing to block device..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            dd if=/dev/zero of="${path}" bs=1M count=10 2>&1 | tail -3
        test_success "Block device write successful"
        
        echo ""
        echo "Reading from block device..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            dd if="${path}" of=/dev/null bs=1M count=10 2>&1 | tail -3
        test_success "Block device read successful"
    else
        test_error "Unknown test type: ${test_type}"
        return 1
    fi
}

#######################################
# Test volume expansion
# Arguments:
#   PVC name
#   Pod name
#   Mount path (for filesystem verification)
#   New size (e.g., "3Gi")
#######################################
test_volume_expansion() {
    local pvc_name=$1
    local pod_name=$2
    local mount_path=$3
    local new_size=$4
    
    test_step 7 8 "Testing volume expansion to ${new_size}"
    
    # Get current PVC size
    local current_size
    current_size=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.resources.requests.storage}')
    test_info "Current PVC size: ${current_size}"
    
    # Get current filesystem capacity (if applicable)
    if [[ -n "${mount_path}" ]]; then
        echo ""
        echo "=== Current filesystem usage ==="
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- df -h "${mount_path}" || true
    fi
    
    # Patch PVC to request larger size
    echo ""
    test_info "Expanding PVC from ${current_size} to ${new_size}..."
    kubectl patch pvc "${pvc_name}" -n "${TEST_NAMESPACE}" \
        -p "{\"spec\":{\"resources\":{\"requests\":{\"storage\":\"${new_size}\"}}}}"
    
    test_success "PVC expansion request submitted"
    
    # Wait for PVC condition to show expansion in progress or completed
    echo ""
    test_info "Waiting for volume expansion to complete (timeout: ${TIMEOUT_PVC})..."
    
    # Wait for capacity to update in status (this indicates controller expansion completed)
    local retries=0
    local max_retries=60
    while [[ $retries -lt $max_retries ]]; do
        local status_capacity
        status_capacity=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.capacity.storage}' 2>/dev/null || echo "") || true
        
        if [[ "${status_capacity}" == "${new_size}" ]]; then
            test_success "Volume expanded to ${new_size}"
            break
        fi
        
        sleep 2
        retries=$((retries + 1))
    done
    
    if [[ $retries -eq $max_retries ]]; then
        test_error "Timeout waiting for volume expansion"
        echo ""
        echo "=== PVC Status ==="
        kubectl describe pvc "${pvc_name}" -n "${TEST_NAMESPACE}"
        echo ""
        echo "=== Controller Logs ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            --tail=50 || true
        return 1
    fi
    
    # Verify filesystem expansion (if applicable)
    if [[ -n "${mount_path}" ]]; then
        echo ""
        test_info "Verifying filesystem expansion..."
        sleep 5  # Give filesystem time to expand
        
        echo ""
        echo "=== New filesystem usage ==="
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- df -h "${mount_path}"
        
        # Verify we can still write to the volume after expansion
        echo ""
        test_info "Testing I/O after expansion..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            sh -c "echo 'Post-expansion test' > ${mount_path}/post-expansion.txt"
        
        local content
        content=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "${mount_path}/post-expansion.txt")
        if [[ "${content}" == "Post-expansion test" ]]; then
            test_success "I/O operations work after expansion"
        else
            test_error "I/O verification failed after expansion"
            return 1
        fi
    fi
    
    test_success "Volume expansion completed successfully"
}

#######################################
# Cleanup test resources
# Arguments:
#   Pod name
#   PVC name
#######################################
cleanup_test() {
    local pod_name=$1
    local pvc_name=$2
    
    test_step 8 8 "Cleaning up test resources"
    
    test_info "Deleting pod: ${pod_name}"
    kubectl delete pod "${pod_name}" -n "${TEST_NAMESPACE}" \
        --ignore-not-found=true --wait=false
    
    test_info "Deleting PVC: ${pvc_name}"
    kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" \
        --ignore-not-found=true --wait=false
    
    # Give Kubernetes time to cleanup
    sleep 5
    
    # Verify cleanup
    if kubectl get pod "${pod_name}" -n "${TEST_NAMESPACE}" &>/dev/null; then
        test_warning "Pod still exists (cleanup in progress)"
    else
        test_success "Pod deleted"
    fi
    
    if kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" &>/dev/null; then
        test_warning "PVC still exists (cleanup in progress)"
    else
        test_success "PVC deleted"
    fi
}

#######################################
# Show diagnostic logs on failure
# Arguments:
#   Pod name (optional)
#   PVC name (optional)
#######################################
show_diagnostic_logs() {
    local pod_name=${1:-}
    local pvc_name=${2:-}
    
    echo ""
    echo "=== DIAGNOSTIC INFORMATION ==="
    echo ""
    
    echo "=== Controller Logs ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=100 || true
    
    echo ""
    echo "=== Node Logs ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
        --tail=100 || true
    
    if [[ -n "${pvc_name}" ]]; then
        echo ""
        echo "=== PVC Status ==="
        kubectl describe pvc "${pvc_name}" -n "${TEST_NAMESPACE}" || true
    fi
    
    if [[ -n "${pod_name}" ]]; then
        echo ""
        echo "=== Pod Status ==="
        kubectl describe pod "${pod_name}" -n "${TEST_NAMESPACE}" || true
        
        echo ""
        echo "=== Pod Logs ==="
        kubectl logs "${pod_name}" -n "${TEST_NAMESPACE}" || true
    fi
    
    echo ""
    echo "=== All PVCs ==="
    kubectl get pvc -A || true
    
    echo ""
    echo "=== All PVs ==="
    kubectl get pv || true
}

#######################################
# Print test summary
# Arguments:
#   Protocol name
#   Status (PASSED or FAILED)
#######################################
test_summary() {
    local protocol=$1
    local status=$2
    
    echo ""
    echo "========================================"
    if [[ "${status}" == "PASSED" ]]; then
        echo -e "${GREEN}${protocol} Integration Test: PASSED${NC}"
    else
        echo -e "${RED}${protocol} Integration Test: FAILED${NC}"
    fi
    echo "========================================"
    echo ""
}
