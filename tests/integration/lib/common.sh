#!/bin/bash
# Common test library for CSI driver distro compatibility tests
# Provides standardized functions for deploying, testing, and cleaning up
#
# USAGE:
#   1. Source this file: source "${SCRIPT_DIR}/lib/common.sh"
#   2. Set total test steps: set_test_steps 6
#   3. Call test functions (verify_cluster, deploy_driver, etc.)

set -e

# Colors for output
export GREEN='\033[0;32m'
export YELLOW='\033[1;33m'
export RED='\033[0;31m'
export BLUE='\033[0;34m'
export CYAN='\033[0;36m'
export NC='\033[0m' # No Color

# Test configuration
export TEST_NAMESPACE="${TEST_NAMESPACE:-test-csi-$(date +%s)-${RANDOM}}"
export TIMEOUT_PVC="${TIMEOUT_PVC:-120s}"
export TIMEOUT_POD="${TIMEOUT_POD:-120s}"
export TIMEOUT_DRIVER="${TIMEOUT_DRIVER:-120s}"

# Test results tracking
declare -a TEST_RESULTS=()
declare -A TEST_DURATIONS=()
export TEST_START_TIME=$(date +%s)

# Test step tracking
export TEST_TOTAL_STEPS=6
export TEST_CURRENT_STEP=0

# Debug and verbosity
export TEST_DEBUG="${TEST_DEBUG:-0}"
export TEST_VERBOSE="${TEST_VERBOSE:-0}"

#######################################
# Check if verbose output is enabled at given level
#######################################
is_verbose() {
    local level=${1:-1}
    [[ "${TEST_VERBOSE}" -ge "${level}" ]]
}

#######################################
# Show YAML manifest contents (verbose only)
#######################################
show_yaml_manifest() {
    local manifest=$1
    local description=${2:-"YAML Manifest"}
    
    if ! is_verbose 1; then
        return 0
    fi
    
    echo ""
    echo "=== ${description} ==="
    echo "File: ${manifest}"
    echo "---"
    cat "${manifest}"
    echo "---"
}

#######################################
# Show Kubernetes resource details (verbose only)
#######################################
show_resource_yaml() {
    local resource_type=$1
    local resource_name=$2
    local namespace=${3:-}
    
    if ! is_verbose 1; then
        return 0
    fi
    
    local namespace_arg=""
    if [[ -n "${namespace}" ]]; then
        namespace_arg="-n ${namespace}"
    fi
    
    echo ""
    echo "=== ${resource_type}/${resource_name} (YAML) ==="
    kubectl get "${resource_type}" "${resource_name}" ${namespace_arg} -o yaml 2>&1 || echo "Resource not found"
}

#######################################
# Show mount information from pod (verbose only)
#######################################
show_pod_mounts() {
    local pod_name=$1
    local namespace=$2
    
    if ! is_verbose 1; then
        return 0
    fi
    
    echo ""
    echo "=== Mount Information for ${pod_name} ==="
    kubectl exec "${pod_name}" -n "${namespace}" -- df -h 2>&1 || echo "df command failed"
}

#######################################
# Show NVMe-oF device details (verbose only)
#######################################
show_nvmeof_details() {
    local pod_name=$1
    local namespace=$2
    
    if ! is_verbose 1; then
        return 0
    fi
    
    echo ""
    echo "=== NVMe-oF Device Details for ${pod_name} ==="
    kubectl exec "${pod_name}" -n "${namespace}" -- sh -c "ls -la /dev/nvme* 2>/dev/null || echo 'No NVMe devices'" 2>&1
}

#######################################
# Show node-level mount info (verbose only)
#######################################
show_node_mounts() {
    if ! is_verbose 1; then
        return 0
    fi
    
    echo ""
    echo "=== CSI Node Driver Logs (mount operations) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
        --tail=50 2>&1 | grep -E "NodeStageVolume|NodePublishVolume|mount|Mount" || echo "No mount-related logs found"
}

#######################################
# Wait for resource to be deleted
#######################################
wait_for_resource_deleted() {
    local resource_type=$1
    local resource_name=$2
    local namespace=${3:-}
    local timeout=${4:-30}
    
    local namespace_arg=""
    if [[ -n "${namespace}" ]]; then
        namespace_arg="-n ${namespace}"
    fi
    
    local elapsed=0
    local interval=2
    
    while [[ $elapsed -lt $timeout ]]; do
        if ! kubectl get "${resource_type}" "${resource_name}" ${namespace_arg} &>/dev/null; then
            return 0
        fi
        sleep "${interval}"
        elapsed=$((elapsed + interval))
    done
    
    return 1
}

#######################################
# Check if NVMe-oF is configured on TrueNAS
#######################################
check_nvmeof_configured() {
    local pvc_manifest=$1
    local pvc_name=$2
    local protocol_name=${3:-"NVMe-oF"}
    
    test_info "Checking if NVMe-oF is configured on TrueNAS..."
    
    kubectl apply -f "${pvc_manifest}" -n "${TEST_NAMESPACE}" || true
    
    local timeout=10
    local elapsed=0
    while [[ $elapsed -lt $timeout ]]; do
        if kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" &>/dev/null; then
            sleep 2
            break
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    
    local logs=$(kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=20 2>/dev/null || true)
    
    if grep -q "No TCP NVMe-oF port" <<< "$logs"; then
        test_warning "NVMe-oF ports not configured on TrueNAS server"
        test_warning "Skipping ${protocol_name} tests"
        kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true
        kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
        test_summary "${protocol_name}" "SKIPPED"
        return 1
    fi
    
    test_success "NVMe-oF is configured, proceeding with tests"
    kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true
    wait_for_resource_deleted "pvc" "${pvc_name}" "${TEST_NAMESPACE}" 10 || true
    return 0
}

#######################################
# Check if iSCSI is configured on TrueNAS
#######################################
check_iscsi_configured() {
    local pvc_manifest=$1
    local pvc_name=$2
    local protocol_name=${3:-"iSCSI"}

    test_info "Checking if iSCSI is configured on TrueNAS..."

    kubectl apply -f "${pvc_manifest}" -n "${TEST_NAMESPACE}" || true

    local timeout=10
    local elapsed=0
    while [[ $elapsed -lt $timeout ]]; do
        if kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" &>/dev/null; then
            sleep 2
            break
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done

    local logs=$(kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=20 2>/dev/null || true)

    if grep -q "No iSCSI portal configured" <<< "$logs"; then
        test_warning "iSCSI portal not configured on TrueNAS server"
        test_warning "Skipping ${protocol_name} tests"
        kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true
        kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
        test_summary "${protocol_name}" "SKIPPED"
        return 1
    fi

    if grep -q "No iSCSI initiator group configured" <<< "$logs"; then
        test_warning "iSCSI initiator group not configured on TrueNAS server"
        test_warning "Skipping ${protocol_name} tests"
        kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true
        kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
        test_summary "${protocol_name}" "SKIPPED"
        return 1
    fi

    test_success "iSCSI is configured, proceeding with tests"
    kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true
    wait_for_resource_deleted "pvc" "${pvc_name}" "${TEST_NAMESPACE}" 10 || true
    return 0
}

#######################################
# Test result recording
#######################################
record_test_result() {
    local test_name=$1
    local status=$2
    local duration=${3:-0}
    TEST_RESULTS+=("${test_name}:${status}:${duration}")
    TEST_DURATIONS["${test_name}"]=${duration}
}

start_test_timer() {
    local test_name=$1
    export "TEST_TIMER_${test_name}=$(date +%s%N)"
}

stop_test_timer() {
    local test_name=$1
    local status=$2
    local start_var="TEST_TIMER_${test_name}"
    local start_time=${!start_var}
    
    if [[ -n "${start_time}" ]]; then
        local end_time=$(date +%s%N)
        local duration_ns=$((end_time - start_time))
        local duration_ms=$((duration_ns / 1000000))
        record_test_result "${test_name}" "${status}" "${duration_ms}"
        unset "${start_var}"
    else
        record_test_result "${test_name}" "${status}"
    fi
}

#######################################
# Output helpers
#######################################
test_success() { echo -e "${GREEN}✓${NC} $1"; }
test_error() { echo -e "${RED}✗${NC} $1"; }
test_warning() { echo -e "${YELLOW}⚠${NC} $1"; }
test_info() { echo -e "${CYAN}ℹ${NC} $1"; }
test_debug() { [[ "${TEST_DEBUG}" == "1" ]] && echo -e "${CYAN}[DEBUG]${NC} $1"; }

set_test_steps() {
    export TEST_TOTAL_STEPS=$1
    export TEST_CURRENT_STEP=0
}

test_step() {
    TEST_CURRENT_STEP=$((TEST_CURRENT_STEP + 1))
    echo ""
    echo -e "${BLUE}[Step ${TEST_CURRENT_STEP}/${TEST_TOTAL_STEPS}]${NC} $1"
    echo ""
}

#######################################
# Print test summary and exit
#######################################
test_summary() {
    local protocol=$1
    local status=$2
    
    report_test_results
    
    echo ""
    echo "========================================"
    if [[ "${status}" == "PASSED" ]]; then
        echo -e "${GREEN}${protocol} Integration Test: PASSED${NC}"
        echo "========================================"
        exit 0
    elif [[ "${status}" == "SKIPPED" ]]; then
        echo -e "${YELLOW}${protocol} Integration Test: SKIPPED${NC}"
        echo "========================================"
        exit 0
    else
        echo -e "${RED}${protocol} Integration Test: FAILED${NC}"
        echo "========================================"
        exit 1
    fi
}

#######################################
# Verify cluster access and create namespace
#######################################
verify_cluster() {
    start_test_timer "verify_cluster"
    test_step "Verifying cluster access"
    
    if ! kubectl cluster-info &>/dev/null; then
        stop_test_timer "verify_cluster" "FAILED"
        test_error "Cannot access cluster"
        false
    fi
    
    test_success "Cluster is accessible"
    kubectl get nodes
    
    echo ""
    test_info "Creating test namespace: ${TEST_NAMESPACE}"
    kubectl create namespace "${TEST_NAMESPACE}" || true
    kubectl label namespace "${TEST_NAMESPACE}" test-csi=true --overwrite
    test_success "Test namespace ready: ${TEST_NAMESPACE}"
    stop_test_timer "verify_cluster" "PASSED"
}

#######################################
# Deploy CSI driver using Helm
#######################################
deploy_driver() {
    local protocol=$1
    shift
    local helm_args=("$@")
    
    start_test_timer "deploy_driver"
    test_step "Deploying CSI driver for ${protocol}"
    
    if [[ -z "${TRUENAS_HOST}" ]]; then
        stop_test_timer "deploy_driver" "FAILED"
        test_error "TRUENAS_HOST not set"
        false
    fi
    
    if [[ -z "${TRUENAS_API_KEY}" ]]; then
        stop_test_timer "deploy_driver" "FAILED"
        test_error "TRUENAS_API_KEY not set"
        false
    fi
    
    if [[ -z "${TRUENAS_POOL}" ]]; then
        stop_test_timer "deploy_driver" "FAILED"
        test_error "TRUENAS_POOL not set"
        false
    fi
    
    local truenas_url="wss://${TRUENAS_HOST}/api/current"
    test_info "TrueNAS URL: ${truenas_url}"
    
    local image_tag="${CSI_IMAGE_TAG:-latest}"
    local image_repo="${CSI_IMAGE_REPOSITORY:-ghcr.io/fenio/tns-csi}"
    local kubelet_path="${KUBELET_PATH:-/var/lib/kubelet}"
    
    local base_args=(
        --namespace kube-system
        --create-namespace
        --set image.repository="${image_repo}"
        --set image.tag="${image_tag}"
        --set image.pullPolicy=Always
        --set truenas.url="${truenas_url}"
        --set truenas.apiKey="${TRUENAS_API_KEY}"
        --set truenas.skipTLSVerify=true
        --set node.kubeletPath="${kubelet_path}"
    )
    
    case "${protocol}" in
        nfs)
            base_args+=(
                --set storageClasses[0].name=tns-csi-nfs
                --set storageClasses[0].enabled=true
                --set storageClasses[0].protocol=nfs
                --set storageClasses[0].pool="${TRUENAS_POOL}"
                --set storageClasses[0].server="${TRUENAS_HOST}"
            )
            ;;
        nvmeof)
            base_args+=(
                --set storageClasses[0].name=tns-csi-nvmeof
                --set storageClasses[0].enabled=true
                --set storageClasses[0].protocol=nvmeof
                --set storageClasses[0].pool="${TRUENAS_POOL}"
                --set storageClasses[0].server="${TRUENAS_HOST}"
                --set storageClasses[0].transport=tcp
                --set storageClasses[0].port=4420
            )
            ;;
        iscsi)
            base_args+=(
                --set storageClasses[0].name=tns-csi-iscsi
                --set storageClasses[0].enabled=true
                --set storageClasses[0].protocol=iscsi
                --set storageClasses[0].pool="${TRUENAS_POOL}"
                --set storageClasses[0].server="${TRUENAS_HOST}"
            )
            ;;
        *)
            stop_test_timer "deploy_driver" "FAILED"
            test_error "Unknown protocol: ${protocol}"
            false
            ;;
    esac
    
    test_info "Executing Helm deployment..."
    if ! helm upgrade --install tns-csi ./charts/tns-csi-driver \
        "${base_args[@]}" \
        "${helm_args[@]}" \
        --wait --timeout 10m; then
        stop_test_timer "deploy_driver" "FAILED"
        test_error "Helm deployment failed"
        
        echo ""
        echo "=== Pod Status ==="
        kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver -o wide || true
        echo ""
        echo "=== Controller Logs ==="
        kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller --all-containers --tail=50 || true
        false
    fi
    
    test_success "CSI driver deployed"
    stop_test_timer "deploy_driver" "PASSED"
}

#######################################
# Wait for CSI driver to be ready
#######################################
wait_for_driver() {
    start_test_timer "wait_for_driver"
    test_step "Waiting for CSI driver to be ready"
    
    if ! kubectl wait --for=condition=Ready pod \
        -l app.kubernetes.io/name=tns-csi-driver \
        -n kube-system \
        --timeout="${TIMEOUT_DRIVER}"; then
        stop_test_timer "wait_for_driver" "FAILED"
        test_error "CSI driver failed to become ready"
        false
    fi
    
    local image_version
    image_version=$(kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver \
        -o jsonpath='{.items[0].spec.containers[?(@.name=="tns-csi-driver")].image}' 2>/dev/null | sed 's/.*://' || echo "unknown")
    test_success "CSI driver is ready (image=${image_version})"
    
    # Wait for StorageClasses
    test_info "Verifying StorageClasses..."
    local timeout=30
    local elapsed=0
    
    while [[ $elapsed -lt $timeout ]]; do
        local scs=$(kubectl get storageclass -o jsonpath='{.items[?(@.provisioner=="tns.csi.io")].metadata.name}' 2>/dev/null || echo "")
        if [[ -n "${scs}" ]]; then
            test_success "StorageClasses verified: ${scs}"
            break
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done
    
    sleep 2
    stop_test_timer "wait_for_driver" "PASSED"
}

#######################################
# Create PVC from manifest
#######################################
create_pvc() {
    local manifest=$1
    local pvc_name=$2
    local wait_for_binding="${3:-true}"
    
    start_test_timer "create_pvc"
    test_step "Creating PersistentVolumeClaim: ${pvc_name}"
    
    show_yaml_manifest "${manifest}" "PVC Manifest"
    
    test_info "Applying PVC manifest..."
    kubectl apply -f "${manifest}" -n "${TEST_NAMESPACE}"
    
    # Wait for PVC to appear
    local timeout=10
    local elapsed=0
    while [[ $elapsed -lt $timeout ]]; do
        if kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" &>/dev/null; then
            break
        fi
        sleep 1
        elapsed=$((elapsed + 1))
    done
    
    show_resource_yaml "pvc" "${pvc_name}" "${TEST_NAMESPACE}"
    
    if [[ "${wait_for_binding}" == "true" ]]; then
        test_info "Waiting for PVC to be bound..."
        if ! kubectl wait --for=jsonpath='{.status.phase}'=Bound \
            pvc/"${pvc_name}" \
            -n "${TEST_NAMESPACE}" \
            --timeout="${TIMEOUT_PVC}"; then
            stop_test_timer "create_pvc" "FAILED"
            test_error "PVC failed to bind"
            false
        fi
        test_success "PVC is bound"
    else
        test_success "PVC created (WaitForFirstConsumer)"
    fi
    stop_test_timer "create_pvc" "PASSED"
}

#######################################
# Create test pod from manifest
#######################################
create_test_pod() {
    local manifest=$1
    local pod_name=$2
    
    start_test_timer "create_test_pod"
    test_step "Creating test pod: ${pod_name}"
    
    show_yaml_manifest "${manifest}" "Pod Manifest"
    
    test_info "Applying pod manifest..."
    kubectl apply -f "${manifest}" -n "${TEST_NAMESPACE}"
    
    test_info "Waiting for pod to be ready..."
    if ! kubectl wait --for=condition=Ready pod/"${pod_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"; then
        stop_test_timer "create_test_pod" "FAILED"
        test_error "Pod failed to become ready"
        
        echo ""
        kubectl describe pod "${pod_name}" -n "${TEST_NAMESPACE}" || true
        echo ""
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
            --tail=200 || true
        false
    fi
    
    test_success "Pod is ready"
    show_pod_mounts "${pod_name}" "${TEST_NAMESPACE}"
    show_node_mounts
    stop_test_timer "create_test_pod" "PASSED"
}

#######################################
# Run I/O tests on mounted volume
#######################################
test_io_operations() {
    local pod_name=$1
    local path=$2
    local test_type=${3:-filesystem}
    
    start_test_timer "test_io_operations"
    test_step "Testing I/O operations (${test_type})"
    
    if [[ "${test_type}" == "filesystem" ]]; then
        test_info "Writing test file..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            sh -c "echo 'CSI Test Data' > ${path}/test.txt"
        test_success "Write successful"
        
        test_info "Reading test file..."
        local content
        content=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "${path}/test.txt")
        if [[ "${content}" == "CSI Test Data" ]]; then
            test_success "Read successful: ${content}"
        else
            stop_test_timer "test_io_operations" "FAILED"
            test_error "Read verification failed"
            false
        fi
        
        test_info "Writing large file (100MB)..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            dd if=/dev/zero of="${path}/iotest.bin" bs=1M count=100 2>&1 | tail -1
        test_success "Large file write successful"
    elif [[ "${test_type}" == "block" ]]; then
        test_info "Writing to block device..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            dd if=/dev/zero of="${path}" bs=1M count=10 2>&1 | tail -1
        test_success "Block write successful"
        
        test_info "Reading from block device..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            dd if="${path}" of=/dev/null bs=1M count=10 2>&1 | tail -1
        test_success "Block read successful"
    fi
    
    stop_test_timer "test_io_operations" "PASSED"
}

#######################################
# Cleanup test resources
#######################################
cleanup_test() {
    local pod_name=$1
    local pvc_name=$2
    
    start_test_timer "cleanup_test"
    echo ""
    test_info "Cleaning up test resources..."
    
    test_info "Deleting test namespace: ${TEST_NAMESPACE}"
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=120s || {
        test_warning "Namespace deletion timed out, forcing"
        kubectl delete namespace "${TEST_NAMESPACE}" --force --grace-period=0 --ignore-not-found=true || true
    }
    
    test_info "Waiting for backend cleanup..."
    local pv_list=$(kubectl get pv -o json | jq -r ".items[] | select(.spec.claimRef.namespace==\"${TEST_NAMESPACE}\") | .metadata.name" 2>/dev/null || echo "")
    
    if [[ -n "${pv_list}" ]]; then
        local timeout=60
        local elapsed=0
        while [[ $elapsed -lt $timeout ]]; do
            local remaining=$(kubectl get pv -o json | jq -r ".items[] | select(.spec.claimRef.namespace==\"${TEST_NAMESPACE}\") | .metadata.name" 2>/dev/null || echo "")
            if [[ -z "${remaining}" ]]; then
                test_success "Backend cleanup complete"
                break
            fi
            sleep 2
            elapsed=$((elapsed + 2))
        done
    fi
    
    stop_test_timer "cleanup_test" "PASSED"
}

#######################################
# Show diagnostic logs on failure
#######################################
show_diagnostic_logs() {
    local pod_name=${1:-}
    local pvc_name=${2:-}
    
    echo ""
    echo "========================================"
    echo "=== DIAGNOSTIC INFORMATION ==="
    echo "========================================"
    
    echo ""
    echo "=== Controller Logs (last 200 lines) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        -c tns-csi-plugin \
        --tail=200 || true
    
    echo ""
    echo "=== Node Logs (last 200 lines) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
        --tail=200 || true
    
    if [[ -n "${pvc_name}" ]]; then
        echo ""
        echo "=== PVC Status ==="
        kubectl describe pvc "${pvc_name}" -n "${TEST_NAMESPACE}" || true
    fi
    
    if [[ -n "${pod_name}" ]]; then
        echo ""
        echo "=== Pod Status ==="
        kubectl describe pod "${pod_name}" -n "${TEST_NAMESPACE}" || true
    fi
    
    echo ""
    echo "=== Events ==="
    kubectl get events -n "${TEST_NAMESPACE}" --sort-by='.lastTimestamp' || true
    
    echo ""
    echo "=== CSI Driver Pods ==="
    kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver -o wide || true
    
    echo "========================================"
}

#######################################
# Report test results summary
#######################################
report_test_results() {
    local total_tests=${#TEST_RESULTS[@]}
    declare -i passed=0
    declare -i failed=0
    
    echo ""
    echo "========================================"
    echo "TEST RESULTS SUMMARY"
    echo "========================================"
    
    for result in "${TEST_RESULTS[@]}"; do
        IFS=':' read -r test_name status duration <<< "${result}"
        if [[ "${status}" == "PASSED" ]]; then
            echo -e "${GREEN}✓${NC} ${test_name} (${duration}ms)"
            passed=$((passed + 1))
        else
            echo -e "${RED}✗${NC} ${test_name}"
            failed=$((failed + 1))
        fi
    done
    
    local total_time=$(( $(date +%s) - TEST_START_TIME ))
    echo ""
    echo "Total: ${total_tests} | Passed: ${passed} | Failed: ${failed}"
    echo "Duration: ${total_time}s"
    echo "========================================"
}
