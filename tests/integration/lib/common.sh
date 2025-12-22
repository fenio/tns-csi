#!/bin/bash
# Common test library for CSI driver integration tests
# Provides standardized functions for deploying, testing, and cleaning up
#
# USAGE:
#   1. Source this file: source "${SCRIPT_DIR}/lib/common.sh"
#   2. Set total test steps: set_test_steps 9
#   3. Call test_step with description only: test_step "Description"
#   4. Steps will auto-increment with correct numbering
#
# DEBUG MODE:
#   Set TEST_DEBUG=1 to enable verbose debug output
#
# VERBOSITY MODES:
#   Set TEST_VERBOSE=0 (default) for minimal output - shows only steps, success/error, and summaries
#   Set TEST_VERBOSE=1 for detailed output - includes YAML manifests, kubectl describe, logs
#   Set TEST_VERBOSE=2 for full debug output - includes all diagnostic information

set -e

# Colors for output
export GREEN='\033[0;32m'
export YELLOW='\033[1;33m'
export RED='\033[0;31m'
export BLUE='\033[0;34m'
export CYAN='\033[0;36m'
export NC='\033[0m' # No Color

# Test configuration
# Generate unique namespace for each test run to ensure isolation
# Use timestamp + random suffix to guarantee uniqueness across parallel jobs
export TEST_NAMESPACE="${TEST_NAMESPACE:-test-csi-$(date +%s)-${RANDOM}}"
export TIMEOUT_PVC="${TIMEOUT_PVC:-120s}"
export TIMEOUT_POD="${TIMEOUT_POD:-120s}"
export TIMEOUT_DRIVER="${TIMEOUT_DRIVER:-120s}"

# Test results tracking
declare -a TEST_RESULTS=()
declare -A TEST_DURATIONS=()
export TEST_START_TIME=$(date +%s)

# Test step tracking - each test can set its own total steps
# Tests should call: set_test_steps <total_number_of_steps>
export TEST_TOTAL_STEPS=9  # Default fallback
export TEST_CURRENT_STEP=0

# Debug mode - set TEST_DEBUG=1 for verbose output
export TEST_DEBUG="${TEST_DEBUG:-0}"

# Verbosity level - controls how much output is shown
# 0 = minimal (default): steps, success/error messages, summaries only
# 1 = detailed: adds YAML manifests, kubectl describe output, logs
# 2 = full: adds all diagnostic information
export TEST_VERBOSE="${TEST_VERBOSE:-0}"

#######################################
# Check if verbose output is enabled at given level
# Arguments:
#   Level to check (1 or 2)
# Returns:
#   0 if verbose output should be shown
#   1 if verbose output should be suppressed
#######################################
is_verbose() {
    local level=${1:-1}
    [[ "${TEST_VERBOSE}" -ge "${level}" ]]
}

#######################################
# Print output only if verbose mode is enabled
# Arguments:
#   Level (1 or 2)
#   Message or command output
#######################################
verbose_output() {
    local level=${1:-1}
    shift
    if is_verbose "${level}"; then
        echo "$@"
    fi
}

#######################################
# Run a command and show output only if verbose
# Arguments:
#   Level (1 or 2)
#   Command to run
#######################################
verbose_run() {
    local level=${1:-1}
    shift
    if is_verbose "${level}"; then
        "$@"
    else
        "$@" >/dev/null 2>&1 || true
    fi
}

#######################################
# Show YAML manifest contents with formatting
# Only shown when TEST_VERBOSE >= 1
# Arguments:
#   Manifest file path
#   Description (optional)
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
# Show Kubernetes resource details in YAML format
# Only shown when TEST_VERBOSE >= 1
# Arguments:
#   Resource type (e.g., pvc, pod, pv)
#   Resource name
#   Namespace (optional)
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
    kubectl get "${resource_type}" "${resource_name}" ${namespace_arg} -o yaml 2>&1 || echo "Resource not found or error occurred"
}

#######################################
# Show mount information from pod
# Only shown when TEST_VERBOSE >= 1
# Arguments:
#   Pod name
#   Namespace
#######################################
show_pod_mounts() {
    local pod_name=$1
    local namespace=$2
    
    if ! is_verbose 1; then
        return 0
    fi
    
    echo ""
    echo "=== Mount Information for ${pod_name} ==="
    echo ""
    echo "--- /proc/mounts ---"
    kubectl exec "${pod_name}" -n "${namespace}" -- cat /proc/mounts 2>&1 || echo "Failed to read /proc/mounts"
    
    echo ""
    echo "--- mount command output ---"
    kubectl exec "${pod_name}" -n "${namespace}" -- mount 2>&1 || echo "mount command failed"
    
    echo ""
    echo "--- df -h output ---"
    kubectl exec "${pod_name}" -n "${namespace}" -- df -h 2>&1 || echo "df command failed"
}

#######################################
# Show NVMe-oF device and connection details from pod
# Only shown when TEST_VERBOSE >= 1
# Arguments:
#   Pod name
#   Namespace
#######################################
show_nvmeof_details() {
    local pod_name=$1
    local namespace=$2
    
    if ! is_verbose 1; then
        return 0
    fi
    
    echo ""
    echo "=== NVMe-oF Device Details for ${pod_name} ==="
    
    echo ""
    echo "--- nvme list output ---"
    kubectl exec "${pod_name}" -n "${namespace}" -- nvme list 2>&1 || echo "nvme list failed (nvme-cli may not be installed)"
    
    echo ""
    echo "--- /sys/class/nvme devices ---"
    kubectl exec "${pod_name}" -n "${namespace}" -- sh -c "ls -la /sys/class/nvme* 2>/dev/null || echo 'No NVMe devices found'" 2>&1
    
    echo ""
    echo "--- /dev/nvme* devices ---"
    kubectl exec "${pod_name}" -n "${namespace}" -- sh -c "ls -la /dev/nvme* 2>/dev/null || echo 'No NVMe block devices found'" 2>&1
    
    echo ""
    echo "--- Block device details ---"
    kubectl exec "${pod_name}" -n "${namespace}" -- sh -c "lsblk 2>/dev/null || echo 'lsblk not available'" 2>&1
}

#######################################
# Show node-level mount and device information
# Only shown when TEST_VERBOSE >= 1
# Requires access to node via privileged pod or node debugging
#######################################
show_node_mounts() {
    if ! is_verbose 1; then
        return 0
    fi
    
    echo ""
    echo "=== Node-Level Mount Information ==="
    
    # Get node name (assuming single-node cluster for tests)
    local node_name
    node_name=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')
    
    echo "Node: ${node_name}"
    echo ""
    
    # Check node logs for mount operations
    echo "--- CSI Node Driver Logs (mount operations) ---"
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
        --tail=50 2>&1 | grep -E "NodeStageVolume|NodePublishVolume|mount|Mount" || echo "No mount-related logs found"
}

# Test tags for selective execution
#######################################
# Check if test should be skipped based on tags
# Arguments:
#   Test tags (comma-separated)
#######################################
should_skip_test() {
    local test_tags=$1
    if [[ -z "${TEST_SKIP_TAGS}" ]]; then
        return 1  # Don't skip
    fi
    
    # Check if any test tag matches skip tags
    IFS=',' read -ra SKIP_TAGS <<< "${TEST_SKIP_TAGS}"
    IFS=',' read -ra TEST_TAG_ARRAY <<< "${test_tags}"
    
    for skip_tag in "${SKIP_TAGS[@]}"; do
        for test_tag in "${TEST_TAG_ARRAY[@]}"; do
            if [[ "${skip_tag}" == "${test_tag}" ]]; then
                return 0  # Skip
            fi
        done
    done
    return 1  # Don't skip
}

#######################################
# Check if NVMe-oF is configured on TrueNAS
# Returns: 0 if configured, 1 if not configured (should skip)
# Arguments:
#   PVC manifest path
#   PVC name
#   Protocol name (for messaging)
#######################################
check_nvmeof_configured() {
    local pvc_manifest=$1
    local pvc_name=$2
    local protocol_name=${3:-"NVMe-oF"}
    
    test_info "Checking if NVMe-oF is configured on TrueNAS..."
    
    # Create a pre-check PVC to see if provisioning works
    kubectl apply -f "${pvc_manifest}" -n "${TEST_NAMESPACE}" || true
    
    # Wait for PVC to be processed and logs to be generated
    local timeout=10
    local elapsed=0
    local interval=2
    
    while [[ $elapsed -lt $timeout ]]; do
        if kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" &>/dev/null; then
            sleep 2  # Give controller a moment to process and log
            break
        fi
        sleep "${interval}"
        elapsed=$((elapsed + interval))
    done
    
    # Check controller logs for port configuration error
    local logs=$(kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=20 2>/dev/null || true)
    
    if grep -q "No TCP NVMe-oF port" <<< "$logs"; then
        test_warning "NVMe-oF ports not configured on TrueNAS server"
        test_warning "Skipping ${protocol_name} tests - this is expected if NVMe-oF is not set up"
        test_info "To enable NVMe-oF: Configure an NVMe-oF TCP portal in TrueNAS UI"
        kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true
        kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
        test_summary "${protocol_name}" "SKIPPED"
        return 1
    fi
    
    test_success "NVMe-oF is configured, proceeding with tests"
    
    # Delete pre-check PVC before running actual test
    kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true
    
    # Wait for PVC to be actually deleted
    if ! wait_for_resource_deleted "pvc" "${pvc_name}" "${TEST_NAMESPACE}" 10; then
        test_warning "Pre-check PVC deletion took longer than expected"
    fi
    
    return 0
}

#######################################
# Record test result
# Arguments:
#   Test name
#   Status (PASSED/FAILED)
#   Duration (optional)
#######################################
record_test_result() {
    local test_name=$1
    local status=$2
    local duration=${3:-0}
    
    TEST_RESULTS+=("${test_name}:${status}:${duration}")
    TEST_DURATIONS["${test_name}"]=${duration}
}

#######################################
# Start timing a test step
# Arguments:
#   Test name
#######################################
start_test_timer() {
    local test_name=$1
    export "TEST_TIMER_${test_name}=$(date +%s%N)"
}

#######################################
# Stop timing a test step and record result
# Arguments:
#   Test name
#   Status (PASSED/FAILED)
#######################################
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
# Print test summary
# Arguments:
#   Protocol name
#   Status (PASSED or FAILED)
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
        echo ""
        exit 0
    else
        echo -e "${RED}${protocol} Integration Test: FAILED${NC}"
        echo "========================================"
        echo ""
        exit 1
    fi
}

#######################################
# Set total number of steps for the current test
# Arguments:
#   Total steps
#######################################
set_test_steps() {
    export TEST_TOTAL_STEPS=$1
    export TEST_CURRENT_STEP=0
    test_debug "Test configured with ${TEST_TOTAL_STEPS} steps"
}

#######################################
# Print a test step header
# Arguments:
#   Description (step number auto-incremented)
# OR (legacy compatibility):
#   Step number
#   Total steps (optional, uses TEST_TOTAL_STEPS if omitted)
#   Description
#######################################
test_step() {
    # Support both new and legacy calling conventions
    if [[ $# -eq 1 ]]; then
        # New style: test_step "Description"
        TEST_CURRENT_STEP=$((TEST_CURRENT_STEP + 1))
        local description=$1
        echo ""
        echo -e "${BLUE}[Step ${TEST_CURRENT_STEP}/${TEST_TOTAL_STEPS}]${NC} ${description}"
        echo ""
    elif [[ $# -eq 2 ]]; then
        # Legacy style with auto total: test_step 1 "Description"
        TEST_CURRENT_STEP=$1
        local description=$2
        echo ""
        echo -e "${BLUE}[Step ${TEST_CURRENT_STEP}/${TEST_TOTAL_STEPS}]${NC} ${description}"
        echo ""
    else
        # Legacy style: test_step 1 9 "Description"
        TEST_CURRENT_STEP=$1
        local total=$2
        local description=$3
        echo ""
        echo -e "${BLUE}[Step ${TEST_CURRENT_STEP}/${total}]${NC} ${description}"
        echo ""
    fi
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
# Print debug message (only if TEST_DEBUG=1)
# Arguments:
#   Message
#######################################
test_debug() {
    if [[ "${TEST_DEBUG}" == "1" ]]; then
        echo -e "${CYAN}[DEBUG]${NC} $1"
    fi
}

#######################################
# Verify cluster is accessible and create test namespace
#######################################
verify_cluster() {
    start_test_timer "verify_cluster"
    test_step "Verifying cluster access"
    test_debug "Checking kubectl cluster-info"
    
    if ! kubectl cluster-info &>/dev/null; then
        stop_test_timer "verify_cluster" "FAILED"
        test_error "Cannot access cluster"
        false  # Trigger ERR trap
    fi
    
    test_success "Cluster is accessible"
    kubectl get nodes
    
    # Create unique test namespace for isolation
    echo ""
    test_info "Creating test namespace: ${TEST_NAMESPACE}"
    kubectl create namespace "${TEST_NAMESPACE}" || true
    # Label namespace for easy cleanup of orphaned test namespaces
    kubectl label namespace "${TEST_NAMESPACE}" test-csi=true --overwrite
    test_success "Test namespace ready: ${TEST_NAMESPACE}"
    stop_test_timer "verify_cluster" "PASSED"
}

#######################################
# Deploy CSI driver using Helm
# Arguments:
#   Protocol (nfs, nvmeof, both, iscsi)
#   Additional helm values (optional)
#######################################
deploy_driver() {
    local protocol=$1
    shift
    local helm_args=("$@")
    
    start_test_timer "deploy_driver"
    test_step "Deploying CSI driver for ${protocol}"
    test_debug "Protocol: ${protocol}, Helm args: ${helm_args[*]}"
    
    # Check required environment variables
    if [[ -z "${TRUENAS_HOST}" ]]; then
        stop_test_timer "deploy_driver" "FAILED"
        test_error "TRUENAS_HOST environment variable not set"
        false  # Trigger ERR trap
    fi
    
    if [[ -z "${TRUENAS_API_KEY}" ]]; then
        stop_test_timer "deploy_driver" "FAILED"
        test_error "TRUENAS_API_KEY environment variable not set"
        false  # Trigger ERR trap
    fi
    
    if [[ -z "${TRUENAS_POOL}" ]]; then
        stop_test_timer "deploy_driver" "FAILED"
        test_error "TRUENAS_POOL environment variable not set"
        false  # Trigger ERR trap
    fi
    
    # Construct TrueNAS WebSocket URL
    local truenas_url="wss://${TRUENAS_HOST}/api/current"
    test_info "TrueNAS URL: ${truenas_url}"
    
    # Base Helm values
    # Use CSI_IMAGE_TAG env var if set, otherwise default to 'latest'
    local image_tag="${CSI_IMAGE_TAG:-latest}"
    # Use CSI_IMAGE_REPOSITORY env var if set, otherwise default to GHCR
    # GHCR is preferred over Docker Hub to avoid rate limiting (429 Too Many Requests)
    local image_repo="${CSI_IMAGE_REPOSITORY:-ghcr.io/fenio/tns-csi}"
    test_debug "Using image: ${image_repo}:${image_tag}"
    
    # Use KUBELET_PATH env var if set, otherwise default to '/var/lib/kubelet'
    # Different distros use different paths:
    # - Standard K8s, K3s, Minikube: /var/lib/kubelet
    # - K0s: /var/lib/k0s/kubelet
    local kubelet_path="${KUBELET_PATH:-/var/lib/kubelet}"
    test_debug "Using kubelet path: ${kubelet_path}"
    
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
            # NVMe-oF uses independent subsystem architecture - subsystems are auto-created per volume
            # Only the NVMe-oF port needs to be pre-configured in TrueNAS
            base_args+=(
                --set storageClasses.nfs.enabled=false
                --set storageClasses.nvmeof.enabled=true
                --set storageClasses.nvmeof.name=tns-csi-nvmeof
                --set storageClasses.nvmeof.pool="${TRUENAS_POOL}"
                --set storageClasses.nvmeof.server="${TRUENAS_HOST}"
                --set storageClasses.nvmeof.transport=tcp
                --set storageClasses.nvmeof.port=4420
            )
            ;;
        both)
            # Enable both NFS and NVMe-oF storage classes for tests that need both protocols
            base_args+=(
                --set storageClasses.nfs.enabled=true
                --set storageClasses.nfs.name=tns-csi-nfs
                --set storageClasses.nfs.pool="${TRUENAS_POOL}"
                --set storageClasses.nfs.server="${TRUENAS_HOST}"
                --set storageClasses.nvmeof.enabled=true
                --set storageClasses.nvmeof.name=tns-csi-nvmeof
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
                --set storageClasses.iscsi.name=tns-csi-iscsi
                --set storageClasses.iscsi.pool="${TRUENAS_POOL}"
                --set storageClasses.iscsi.server="${TRUENAS_HOST}"
            )
            ;;
        *)
            stop_test_timer "deploy_driver" "FAILED"
            test_error "Unknown protocol: ${protocol}"
            false  # Trigger ERR trap
            ;;
    esac
    
    # Show Helm command for debugging (verbose mode only)
    if is_verbose 1; then
        echo ""
        echo "=== Helm Installation Command ==="
        echo "helm upgrade --install tns-csi ./charts/tns-csi-driver \\"
        local all_args=("${base_args[@]}" "${helm_args[@]}")
        local i=0
        while [[ $i -lt ${#all_args[@]} ]]; do
            local arg="${all_args[$i]}"
            local next_arg="${all_args[$((i+1))]:-}"
            
            # Check if this is a flag that takes a value as the next argument
            if [[ "${arg}" == --* && -n "${next_arg}" && "${next_arg}" != --* ]]; then
                # Mask sensitive values
                if [[ "${next_arg}" == *"apiKey"* || "${arg}" == *"apiKey"* ]]; then
                    echo "  ${arg} ***MASKED*** \\"
                else
                    echo "  ${arg} ${next_arg} \\"
                fi
                i=$((i + 2))
            else
                # Standalone flag or flag with = syntax
                if [[ "${arg}" == *"apiKey="* ]]; then
                    echo "  ${arg/=*/=***MASKED***} \\"
                else
                    echo "  ${arg} \\"
                fi
                i=$((i + 1))
            fi
        done
        echo "  --wait --timeout 5m"
        echo ""
    fi
    
    # Deploy with Helm
    test_info "Executing Helm deployment..."
    if ! helm upgrade --install tns-csi ./charts/tns-csi-driver \
        "${base_args[@]}" \
        "${helm_args[@]}" \
        --wait --timeout 10m; then
        stop_test_timer "deploy_driver" "FAILED"
        test_error "Helm deployment failed"
        
        # Always show diagnostics on failure
        echo ""
        echo "=== DIAGNOSTIC: Pod Status After Helm Failure ==="
        kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver -o wide || true
        
        echo ""
        echo "=== DIAGNOSTIC: Pod Descriptions ==="
        kubectl describe pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver || true
        
        echo ""
        echo "=== DIAGNOSTIC: Controller Logs ==="
        kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller --all-containers --tail=50 || true
        
        echo ""
        echo "=== DIAGNOSTIC: Node Logs ==="
        kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node --all-containers --tail=50 || true
        
        false  # Trigger ERR trap
    fi
    
    test_success "CSI driver deployed"
    
    # Verify deployment (verbose mode only for detailed output)
    if is_verbose 1; then
        echo ""
        echo "=== Helm deployment status ==="
        helm list -n kube-system
        
        echo ""
        echo "=== Helm values (deployed) ==="
        helm get values tns-csi -n kube-system || true
        
        echo ""
        echo "=== CSI driver pods ==="
        kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver -o wide
    
        echo ""
        echo "=== StorageClasses ==="
        kubectl get storageclass
        
        echo ""
        echo "=== StorageClass Details (YAML) ==="
        case "${protocol}" in
            nfs)
                kubectl get storageclass tns-csi-nfs -o yaml || true
                ;;
            nvmeof)
                kubectl get storageclass tns-csi-nvmeof -o yaml || true
                ;;
            both)
                echo "--- NFS StorageClass ---"
                kubectl get storageclass tns-csi-nfs -o yaml || true
                echo ""
                echo "--- NVMe-oF StorageClass ---"
                kubectl get storageclass tns-csi-nvmeof -o yaml || true
                ;;
            iscsi)
                kubectl get storageclass tns-csi-iscsi -o yaml || true
                ;;
        esac
        
        echo ""
        echo "=== CSIDriver Resource ==="
        kubectl get csidriver tns.csi.io -o yaml || true
    fi
    
    stop_test_timer "deploy_driver" "PASSED"
}

#######################################
# Wait for resource to be deleted
# Arguments:
#   Resource type (e.g., pvc, pod, namespace)
#   Resource name
#   Namespace (optional, omit for cluster-scoped resources)
#   Timeout in seconds (default: 30)
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
            return 0  # Resource is deleted
        fi
        sleep "${interval}"
        elapsed=$((elapsed + interval))
    done
    
    return 1  # Timeout
}

#######################################
# Wait for CSI driver to be ready
#######################################
wait_for_driver() {
    start_test_timer "wait_for_driver"
    test_step "Waiting for CSI driver to be ready"
    test_debug "Timeout: ${TIMEOUT_DRIVER}"
    
    if ! kubectl wait --for=condition=Ready pod \
        -l app.kubernetes.io/name=tns-csi-driver \
        -n kube-system \
        --timeout="${TIMEOUT_DRIVER}"; then
        stop_test_timer "wait_for_driver" "FAILED"
        test_error "CSI driver failed to become ready"
        false  # Trigger ERR trap
    fi
    
    test_success "CSI driver is ready"
    
    # Verify image version
    echo ""
    echo "=== Driver image version ==="
    kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver \
        -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.containers[*].image}{"\n"}{end}'
    
    # Wait for StorageClasses to be fully registered in API server
    # This prevents race conditions where PVC creation happens before StorageClass is available
    echo ""
    test_info "Verifying StorageClasses are available..."
    
    local storageclass_timeout=30
    local elapsed=0
    local interval=2
    local all_ready=false
    
    while [[ $elapsed -lt $storageclass_timeout ]]; do
        # Get list of expected StorageClasses from deployed resources
        local expected_scs
        expected_scs=$(kubectl get storageclass -o jsonpath='{.items[?(@.provisioner=="tns.csi.io")].metadata.name}' 2>/dev/null || echo "")
        
        if [[ -n "${expected_scs}" ]]; then
            # Verify each StorageClass can be retrieved successfully
            all_ready=true
            for sc in ${expected_scs}; do
                if ! kubectl get storageclass "${sc}" &>/dev/null; then
                    test_debug "StorageClass ${sc} not yet available"
                    all_ready=false
                    break
                fi
            done
            
            if [[ "${all_ready}" == "true" ]]; then
                test_success "StorageClasses verified: ${expected_scs}"
                break
            fi
        else
            test_debug "No TNS StorageClasses found yet"
        fi
        
        sleep "${interval}"
        elapsed=$((elapsed + interval))
    done
    
    if [[ "${all_ready}" != "true" ]]; then
        test_warning "StorageClass verification timed out after ${storageclass_timeout}s"
        test_warning "PVC creation may fail if StorageClasses are not ready"
    fi
    
    # Additional safety: Give API server a moment to fully propagate StorageClass changes
    sleep 2
    
    stop_test_timer "wait_for_driver" "PASSED"
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
    
    start_test_timer "create_pvc"
    test_step "Creating PersistentVolumeClaim: ${pvc_name}"
    test_debug "Manifest: ${manifest}, Wait for binding: ${wait_for_binding}"
    
    # Show manifest contents (verbose only)
    show_yaml_manifest "${manifest}" "PVC Manifest - ${pvc_name}"
    
    test_info "Applying PVC manifest..."
    kubectl apply -f "${manifest}" -n "${TEST_NAMESPACE}"
    
    # Wait for PVC to be created and start provisioning (poll until it exists)
    local timeout=10
    local elapsed=0
    local interval=1
    
    while [[ $elapsed -lt $timeout ]]; do
        if kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" &>/dev/null; then
            break
        fi
        sleep "${interval}"
        elapsed=$((elapsed + interval))
    done
    
    if [[ $elapsed -ge $timeout ]]; then
        test_warning "PVC took longer than expected to appear in API server"
    fi
    
    # Check PVC status (verbose only)
    if is_verbose 1; then
        echo ""
        echo "=== PVC Status (describe) ==="
        kubectl describe pvc "${pvc_name}" -n "${TEST_NAMESPACE}"
    fi
    
    # Show full PVC YAML (verbose only - handled by show_resource_yaml)
    show_resource_yaml "pvc" "${pvc_name}" "${TEST_NAMESPACE}"
    
    # Check controller logs (verbose only)
    if is_verbose 1; then
        echo ""
        echo "=== Controller Logs (last 30 lines) ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            --tail=30 || true
    fi
    
    # Wait for PVC to be bound (skip if volumeBindingMode is WaitForFirstConsumer)
    if [[ "${wait_for_binding}" == "true" ]]; then
        test_info "Waiting for PVC to be bound (timeout: ${TIMEOUT_PVC})..."
        if ! kubectl wait --for=jsonpath='{.status.phase}'=Bound \
            pvc/"${pvc_name}" \
            -n "${TEST_NAMESPACE}" \
            --timeout="${TIMEOUT_PVC}"; then
            stop_test_timer "create_pvc" "FAILED"
            test_error "PVC failed to bind"
            false  # Trigger ERR trap
        fi
        
        test_success "PVC is bound"
        
        # Get PV name and show details
        local pv_name
        pv_name=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
        test_info "Created PV: ${pv_name}"
        
        # Show PV details (verbose only - handled by show_resource_yaml)
        show_resource_yaml "pv" "${pv_name}"
        
        if is_verbose 1; then
            echo ""
            echo "=== PV Details (describe) ==="
            kubectl describe pv "${pv_name}"
        fi
    else
        test_info "Skipping PVC binding wait (volumeBindingMode: WaitForFirstConsumer)"
        test_success "PVC created (will bind when pod is scheduled)"
    fi
    stop_test_timer "create_pvc" "PASSED"
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
    
    start_test_timer "create_test_pod"
    test_step "Creating test pod: ${pod_name}"
    test_debug "Manifest: ${manifest}, Timeout: ${TIMEOUT_POD}"
    
    # Show manifest contents
    show_yaml_manifest "${manifest}" "Pod Manifest - ${pod_name}"
    
    echo ""
    test_info "Applying pod manifest..."
    kubectl apply -f "${manifest}" -n "${TEST_NAMESPACE}"
    
    # Wait for pod to be ready
    echo ""
    test_info "Waiting for pod to be ready (timeout: ${TIMEOUT_POD})..."
    
    if ! kubectl wait --for=condition=Ready pod/"${pod_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"; then
        
        stop_test_timer "create_test_pod" "FAILED"
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
            --tail=200 || true
        
        # Use false to trigger ERR trap (return 1 doesn't work with set -e inside if blocks)
        false
    fi
    
    test_success "Pod is ready"
    
    # Show detailed pod information (verbose only)
    show_resource_yaml "pod" "${pod_name}" "${TEST_NAMESPACE}"
    
    if is_verbose 1; then
        echo ""
        echo "=== Pod Details (describe) ==="
        kubectl describe pod "${pod_name}" -n "${TEST_NAMESPACE}"
    fi
    
    # Show mount information (verbose only - handled by show_pod_mounts)
    show_pod_mounts "${pod_name}" "${TEST_NAMESPACE}"
    
    # Detect if this is NVMe-oF based on storage class or volume attributes
    local pvc_name
    pvc_name=$(kubectl get pod "${pod_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumes[0].persistentVolumeClaim.claimName}' 2>/dev/null || echo "")
    
    if [[ -n "${pvc_name}" ]]; then
        local storage_class
        storage_class=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.storageClassName}' 2>/dev/null || echo "")
        
        if [[ "${storage_class}" == *"nvmeof"* ]]; then
            if is_verbose 1; then
                test_info "Detected NVMe-oF volume, showing device details..."
            fi
            show_nvmeof_details "${pod_name}" "${TEST_NAMESPACE}"
        fi
    fi
    
    # Show node driver logs for this mount operation (verbose only - handled by show_node_mounts)
    show_node_mounts
    
    # Show pod logs (verbose only)
    if is_verbose 1; then
        echo ""
        echo "=== Pod Logs ==="
        kubectl logs "${pod_name}" -n "${TEST_NAMESPACE}" || true
    fi
    stop_test_timer "create_test_pod" "PASSED"
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
    
    start_test_timer "test_io_operations"
    test_step "Testing I/O operations (${test_type})"
    test_debug "Pod: ${pod_name}, Path: ${path}, Type: ${test_type}"
    
    if [[ "${test_type}" == "filesystem" ]]; then
        # Filesystem tests
        if is_verbose 1; then
            echo ""
            echo "=== I/O Test Command ==="
            echo "Command: kubectl exec ${pod_name} -n ${TEST_NAMESPACE} -- sh -c \"echo 'CSI Test Data' > ${path}/test.txt\""
            echo ""
        fi
        test_info "Writing test file..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            sh -c "echo 'CSI Test Data' > ${path}/test.txt"
        test_success "Write operation successful"
        
        if is_verbose 1; then
            echo ""
            echo "=== Read Test Command ==="
            echo "Command: kubectl exec ${pod_name} -n ${TEST_NAMESPACE} -- cat ${path}/test.txt"
            echo ""
        fi
        test_info "Reading test file..."
        local content
        content=$(kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- cat "${path}/test.txt")
        if [[ "${content}" == "CSI Test Data" ]]; then
            test_success "Read operation successful: ${content}"
        else
            stop_test_timer "test_io_operations" "FAILED"
            test_error "Read verification failed: expected 'CSI Test Data', got '${content}'"
            false  # Trigger ERR trap
        fi
        
        if is_verbose 1; then
            echo ""
            echo "=== Large File Write Command ==="
            echo "Command: kubectl exec ${pod_name} -n ${TEST_NAMESPACE} -- dd if=/dev/zero of=${path}/iotest.bin bs=1M count=100"
            echo ""
        fi
        test_info "Writing large test file (100MB)..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            dd if=/dev/zero of="${path}/iotest.bin" bs=1M count=100 2>&1 | tail -3
        test_success "Large file write successful"
        
        if is_verbose 1; then
            echo ""
            echo "Verifying file size..."
            kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
                ls -lh "${path}/"
        fi
        test_success "I/O operations completed successfully"
        
    elif [[ "${test_type}" == "block" ]]; then
        # Block device tests
        test_info "Writing to block device..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            dd if=/dev/zero of="${path}" bs=1M count=10 2>&1 | tail -3
        test_success "Block device write successful"
        
        test_info "Reading from block device..."
        kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
            dd if="${path}" of=/dev/null bs=1M count=10 2>&1 | tail -3
        test_success "Block device read successful"
    else
        stop_test_timer "test_io_operations" "FAILED"
        test_error "Unknown test type: ${test_type}"
        false  # Trigger ERR trap
    fi
    stop_test_timer "test_io_operations" "PASSED"
}

#######################################
# Apply inline YAML with debugging output
# Reads from stdin and applies, showing the manifest
# Arguments:
#   Description
#######################################
apply_inline_manifest() {
    local description=$1
    local manifest_content=$(cat)
    
    echo ""
    echo "=== Applying Inline Manifest: ${description} ==="
    echo "---"
    echo "${manifest_content}"
    echo "---"
    echo ""
    
    echo "${manifest_content}" | kubectl apply -f -
}

#######################################
# Safely execute kubectl exec and capture output
# Properly handles errors and shows diagnostics on failure
# Arguments:
#   pod_name
#   namespace
#   command (the full command to execute in the pod)
#   expected_value (optional - if provided, will validate output)
#   error_context (optional - description for error messages)
# Returns:
#   0 on success (output in stdout)
#   1 on failure (after showing diagnostics)
#######################################
safe_kubectl_exec() {
    local pod_name=$1
    local namespace=$2
    local command=$3
    local expected_value=${4:-}
    local error_context=${5:-"command execution"}
    
    local output
    local exit_code
    
    # Execute and capture both output and exit code
    if ! output=$(kubectl exec "${pod_name}" -n "${namespace}" -- sh -c "${command}" 2>&1); then
        exit_code=$?
        test_error "${pod_name}: Failed ${error_context}"
        test_error "Command: ${command}"
        test_error "Exit code: ${exit_code}"
        test_error "Output: ${output}"
        show_diagnostic_logs "${pod_name}" ""
        return 1
    fi
    
    # If expected value provided, validate it
    if [[ -n "${expected_value}" ]]; then
        if [[ "${output}" != "${expected_value}" ]]; then
            test_error "${pod_name}: ${error_context} - data mismatch"
            test_error "Expected: '${expected_value}'"
            test_error "Got: '${output}'"
            show_diagnostic_logs "${pod_name}" ""
            return 1
        fi
    fi
    
    # Output the result so it can be captured by caller
    echo "${output}"
    return 0
}

#######################################
# Check if a file exists in a pod
# Arguments:
#   pod_name
#   namespace
#   file_path
# Returns:
#   0 if file exists
#   1 if file doesn't exist or error
#######################################
pod_file_exists() {
    local pod_name=$1
    local namespace=$2
    local file_path=$3
    
    if ! kubectl exec "${pod_name}" -n "${namespace}" -- test -f "${file_path}" 2>/dev/null; then
        return 1
    fi
    return 0
}

#######################################
# Test tags for selective execution
#######################################
test_volume_expansion() {
    local pvc_name=$1
    local pod_name=$2
    local mount_path=$3
    local new_size=$4
    
    start_test_timer "test_volume_expansion"
    test_step "Testing volume expansion to ${new_size}"
    test_debug "PVC: ${pvc_name}, Pod: ${pod_name}, Mount: ${mount_path}"
    
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
    echo "=== Volume Expansion Command ==="
    echo "Command: kubectl patch pvc ${pvc_name} -n ${TEST_NAMESPACE} -p '{\"spec\":{\"resources\":{\"requests\":{\"storage\":\"${new_size}\"}}}}'"
    echo ""
    test_info "Expanding PVC from ${current_size} to ${new_size}..."
    kubectl patch pvc "${pvc_name}" -n "${TEST_NAMESPACE}" \
        -p "{\"spec\":{\"resources\":{\"requests\":{\"storage\":\"${new_size}\"}}}}"
    
    test_success "PVC expansion request submitted"
    
    # Show updated PVC YAML
    show_resource_yaml "pvc" "${pvc_name}" "${TEST_NAMESPACE}"
    
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
        stop_test_timer "test_volume_expansion" "FAILED"
        test_error "Timeout waiting for volume expansion"
        echo ""
        echo "=== PVC Status ==="
        kubectl describe pvc "${pvc_name}" -n "${TEST_NAMESPACE}"
        echo ""
        echo "=== StorageClass Configuration ==="
        kubectl get sc -o yaml | grep -A 20 "name: tns-csi-" || true
        echo ""
        echo "=== Controller Pod Status ==="
        kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller || true
        echo ""
        echo "=== CSI Resizer Logs ==="
        local controller_pod
        controller_pod=$(kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
        if [[ -n "${controller_pod}" ]]; then
            kubectl logs -n kube-system "${controller_pod}" -c csi-resizer --tail=100 || true
        else
            echo "No controller pod found"
        fi
        echo ""
        echo "=== Controller Driver Logs ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            --tail=200 || true
        false  # Trigger ERR trap
    fi
    
    # Verify filesystem expansion (if applicable)
    if [[ -n "${mount_path}" ]]; then
        echo ""
        test_info "Verifying filesystem expansion..."
        
        # Poll for filesystem to reflect new size (some filesystems need time to expand)
        local timeout=15
        local elapsed=0
        local interval=2
        local fs_expanded=false
        
        while [[ $elapsed -lt $timeout ]]; do
            # Try to write a file - if filesystem is still expanding, this gives it time
            if kubectl exec "${pod_name}" -n "${TEST_NAMESPACE}" -- \
                sh -c "echo 'expansion-test' > ${mount_path}/.expansion-test 2>/dev/null"; then
                fs_expanded=true
                break
            fi
            sleep "${interval}"
            elapsed=$((elapsed + interval))
        done
        
        if [[ "${fs_expanded}" == "true" ]]; then
            test_success "Filesystem is accessible after expansion (${elapsed}s)"
        else
            test_warning "Filesystem expansion took longer than expected, but continuing"
        fi
        
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
            stop_test_timer "test_volume_expansion" "FAILED"
            test_error "I/O verification failed after expansion"
            false  # Trigger ERR trap
        fi
    fi
    
    test_success "Volume expansion completed successfully"
    stop_test_timer "test_volume_expansion" "PASSED"
}

#######################################
# Verify Prometheus metrics are being collected
# This function checks if metrics are available and contain
# expected data after CSI operations have been performed
#######################################
verify_metrics() {
    start_test_timer "verify_metrics"
    test_info "Verifying Prometheus metrics collection"
    test_debug "Looking for controller pod in kube-system namespace"
    
    # Find the controller pod
    local controller_pod
    controller_pod=$(kubectl get pods -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    
    if [[ -z "${controller_pod}" ]]; then
        test_warning "No controller pod found, skipping metrics check"
        stop_test_timer "verify_metrics" "PASSED"
        return 0
    fi
    
    test_info "Fetching metrics from controller pod: ${controller_pod}"
    
    # Fetch metrics from the controller's metrics endpoint (default port 8080)
    local metrics_output
    if ! metrics_output=$(kubectl exec -n kube-system "${controller_pod}" -- \
        wget -q -O - http://localhost:8080/metrics 2>&1); then
        test_warning "Failed to fetch metrics: ${metrics_output}"
        stop_test_timer "verify_metrics" "PASSED"
        return 0
    fi
    
    # Check for expected custom metrics (skip printing full output)
    local expected_metrics=(
        "tns_csi_operations_total"
        "tns_csi_operation_duration_seconds"
        "tns_csi_volume_operations_total"
        "tns_csi_volume_operation_duration_seconds"
        "tns_csi_websocket_connection_status"
        "tns_csi_websocket_reconnections_total"
        "tns_csi_websocket_connection_duration_seconds"
        "tns_csi_websocket_messages_total"
        "tns_csi_websocket_message_duration_seconds"
        "tns_csi_volume_capacity_bytes"
    )
    
    local found_count=0
    local missing_metrics=()
    
    for metric in "${expected_metrics[@]}"; do
        if grep -q "^${metric}" <<< "${metrics_output}"; then
            found_count=$((found_count + 1))
        else
            missing_metrics+=("${metric}")
        fi
    done
    
    echo ""
    test_info "Metrics found: ${found_count}/${#expected_metrics[@]}"
    
    if [[ ${found_count} -eq 0 ]]; then
        stop_test_timer "verify_metrics" "FAILED"
        test_error "No custom metrics found - metrics collection may not be working"
        false  # Trigger ERR trap
    fi
    
    if [[ ${#missing_metrics[@]} -gt 0 ]]; then
        if is_verbose 1; then
            test_warning "Some metrics are missing (this may be expected if operations weren't performed)"
        fi
    fi
    
    # Check for metrics with actual data (non-zero values or labels)
    if is_verbose 1; then
        echo ""
        test_info "Checking for metrics with collected data..."
    fi
    
    local metrics_with_data=0
    if grep -E "^tns_csi_operations_total.*[1-9]" <<< "${metrics_output}" >/dev/null; then
        if is_verbose 1; then
            test_success "CSI operations were recorded"
        fi
        metrics_with_data=$((metrics_with_data + 1))
    fi
    
    if grep -E "^tns_csi_volume_operations_total.*[1-9]" <<< "${metrics_output}" >/dev/null; then
        if is_verbose 1; then
            test_success "Volume operations were recorded"
        fi
        metrics_with_data=$((metrics_with_data + 1))
    fi
    
    if grep -E "^tns_csi_websocket_connection_status" <<< "${metrics_output}" >/dev/null; then
        if is_verbose 1; then
            test_success "WebSocket connection status is tracked"
        fi
        metrics_with_data=$((metrics_with_data + 1))
    fi
    
    if grep -E "^tns_csi_websocket_messages_total.*[1-9]" <<< "${metrics_output}" >/dev/null; then
        if is_verbose 1; then
            test_success "WebSocket messages were recorded"
        fi
        metrics_with_data=$((metrics_with_data + 1))
    fi
    
    if [[ ${metrics_with_data} -gt 0 ]]; then
        test_success "Metrics are being collected during CSI operations (${metrics_with_data} metric types with data)"
    else
        test_warning "Metrics endpoint is available but no operation data was found"
    fi
    
    test_success "Metrics verification completed"
    stop_test_timer "verify_metrics" "PASSED"
}

#######################################
# Cleanup test resources
# Arguments:
#   Pod name (unused - kept for compatibility)
#   PVC name (unused - kept for compatibility)
#######################################
cleanup_test() {
    local pod_name=$1
    local pvc_name=$2
    
    start_test_timer "cleanup_test"
    echo ""
    test_info "Cleaning up test resources..."
    test_debug "Namespace: ${TEST_NAMESPACE}, Pod: ${pod_name}, PVC: ${pvc_name}"
    
    # Delete the entire namespace - this triggers CSI DeleteVolume
    test_info "Deleting test namespace: ${TEST_NAMESPACE}"
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=120s || {
        test_warning "Namespace deletion timed out, forcing deletion"
        kubectl delete namespace "${TEST_NAMESPACE}" --force --grace-period=0 --ignore-not-found=true || true
    }
    
    # Wait for TrueNAS backend cleanup to complete by monitoring PV deletion
    # The CSI driver's DeleteVolume removes datasets, NFS shares, and NVMe-oF subsystems
    # PVs are deleted after successful backend cleanup, so we poll for their deletion
    test_info "Waiting for TrueNAS backend cleanup (monitoring PV deletion)..."
    
    # Get list of PVs that were in this namespace before deletion
    local pv_list=$(kubectl get pv -o json | jq -r ".items[] | select(.spec.claimRef.namespace==\"${TEST_NAMESPACE}\") | .metadata.name" 2>/dev/null || echo "")
    
    if [[ -n "${pv_list}" ]]; then
        local timeout=60
        local elapsed=0
        local interval=2
        local all_deleted=false
        
        while [[ $elapsed -lt $timeout ]]; do
            local remaining_pvs=$(kubectl get pv -o json | jq -r ".items[] | select(.spec.claimRef.namespace==\"${TEST_NAMESPACE}\") | .metadata.name" 2>/dev/null || echo "")
            
            if [[ -z "${remaining_pvs}" ]]; then
                all_deleted=true
                break
            fi
            
            test_info "Waiting for PVs to be deleted (${elapsed}s elapsed)..."
            sleep "${interval}"
            elapsed=$((elapsed + interval))
        done
        
        if [[ "${all_deleted}" == "true" ]]; then
            test_success "TrueNAS backend cleanup complete (PVs deleted in ${elapsed}s)"
        else
            test_warning "Some PVs still exist after ${timeout}s, but continuing (may indicate slow cleanup)"
        fi
    else
        test_success "No PVs found for cleanup"
    fi
    stop_test_timer "cleanup_test" "PASSED"
}

#######################################
# Verify that a dataset/zvol was actually deleted from TrueNAS
# This uses Go with pkg/tnsapi to query TrueNAS WebSocket API
# (same approach as cleanup-truenas-artifacts.sh)
# 
# Arguments:
#   Volume handle (dataset path, e.g., "csi/pvc-xxx")
#   Timeout in seconds (optional, default: 30)
#
# Returns:
#   0 if dataset is confirmed deleted
#   1 if dataset still exists or error
#######################################
verify_truenas_deletion() {
    local volume_handle=$1
    local timeout=${2:-30}
    
    test_info "Verifying TrueNAS backend deletion for: ${volume_handle}"
    
    # Check required environment variables
    if [[ -z "${TRUENAS_HOST}" ]] || [[ -z "${TRUENAS_API_KEY}" ]]; then
        test_warning "TRUENAS_HOST or TRUENAS_API_KEY not set, skipping TrueNAS verification"
        return 0
    fi
    
    if [[ -z "${TRUENAS_POOL}" ]]; then
        test_warning "TRUENAS_POOL not set, skipping TrueNAS verification"
        return 0
    fi
    
    # Check if Go is available - skip verification if not (don't fail the test)
    if ! command -v go &>/dev/null; then
        test_warning "Go not installed - skipping TrueNAS backend verification"
        test_warning "The PV was deleted successfully, but cannot confirm backend cleanup"
        return 0
    fi
    
    # Construct full dataset path: pool/volume_handle
    local dataset_path="${TRUENAS_POOL}/${volume_handle}"
    
    test_info "Checking if dataset/zvol exists: ${dataset_path}"
    test_info "Timeout: ${timeout} seconds"
    
    # Find the repository root (where go.mod is located)
    local repo_root
    repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
    
    # Create temp directory for Go verification tool
    local verify_dir
    verify_dir=$(mktemp -d)
    
    # Generate Go verification tool (same pattern as cleanup-truenas-artifacts.sh)
    cat > "${verify_dir}/verify.go" <<'EOFGO'
package main

import (
    "context"
    "fmt"
    "os"
    "time"

    "github.com/fenio/tns-csi/pkg/tnsapi"
)

func main() {
    host := os.Getenv("TRUENAS_HOST")
    apiKey := os.Getenv("TRUENAS_API_KEY")
    datasetPath := os.Getenv("DATASET_PATH")

    if host == "" || apiKey == "" || datasetPath == "" {
        fmt.Println("ERROR: Missing required environment variables")
        os.Exit(2)
    }

    url := fmt.Sprintf("wss://%s/api/current", host)

    client, err := tnsapi.NewClient(url, apiKey, true)
    if err != nil {
        fmt.Printf("ERROR: Failed to connect to TrueNAS: %v\n", err)
        os.Exit(2)
    }
    defer client.Close()

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    // Query for the dataset using filter
    var datasets []map[string]interface{}
    filter := []interface{}{[]interface{}{"id", "=", datasetPath}}
    if err := client.Call(ctx, "pool.dataset.query", []interface{}{filter}, &datasets); err != nil {
        fmt.Printf("ERROR: Query failed: %v\n", err)
        os.Exit(2)
    }

    if len(datasets) == 0 {
        fmt.Printf("DELETED: Dataset '%s' does not exist on TrueNAS\n", datasetPath)
        os.Exit(0) // Success - dataset is deleted
    } else {
        fmt.Printf("EXISTS: Dataset '%s' still exists on TrueNAS\n", datasetPath)
        os.Exit(1) // Failure - dataset still exists
    }
}
EOFGO

    # Build the verification tool using the same pattern as cleanup scripts
    # This uses go mod edit -replace to use local pkg/tnsapi
    local original_dir
    original_dir=$(pwd)
    cd "${verify_dir}"
    
    test_info "Building TrueNAS verification tool..."
    if ! go mod init verify >/dev/null 2>&1; then
        test_warning "Failed to initialize Go module, skipping TrueNAS verification"
        cd "${original_dir}"
        rm -rf "${verify_dir}"
        return 0
    fi
    
    if ! go mod edit -replace "github.com/fenio/tns-csi=${repo_root}" >/dev/null 2>&1; then
        test_warning "Failed to set up Go module replace, skipping TrueNAS verification"
        cd "${original_dir}"
        rm -rf "${verify_dir}"
        return 0
    fi
    
    if ! go mod tidy >/dev/null 2>&1; then
        test_warning "Failed to tidy Go module, skipping TrueNAS verification"
        cd "${original_dir}"
        rm -rf "${verify_dir}"
        return 0
    fi
    
    # Poll for deletion with timeout
    local deadline=$((SECONDS + timeout))
    local attempt=0
    local result
    local exit_code
    
    while [[ $SECONDS -lt $deadline ]]; do
        attempt=$((attempt + 1))
        
        export DATASET_PATH="${dataset_path}"
        result=$(go run verify.go 2>&1) || true
        exit_code=$?
        
        # Check result based on output prefix
        if echo "${result}" | grep -q "^DELETED:"; then
            test_success "Dataset '${dataset_path}' confirmed deleted from TrueNAS (attempt ${attempt})"
            cd "${original_dir}"
            rm -rf "${verify_dir}"
            return 0
        elif echo "${result}" | grep -q "^EXISTS:"; then
            test_info "Attempt ${attempt}: Dataset still exists on TrueNAS, waiting..."
            sleep 2
        elif echo "${result}" | grep -q "^ERROR:"; then
            test_info "Attempt ${attempt}: ${result}"
            sleep 2
        else
            test_info "Attempt ${attempt}: Unexpected result: ${result}"
            sleep 2
        fi
    done
    
    # Cleanup
    cd "${original_dir}"
    rm -rf "${verify_dir}"
    
    # Timeout reached - dataset still exists
    test_error "Dataset '${dataset_path}' still exists on TrueNAS after ${timeout} seconds"
    test_error "This indicates the CSI DeleteVolume did not properly clean up the backend resource."
    return 1
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
    echo "========================================"
    echo "=== DIAGNOSTIC INFORMATION ==="
    echo "========================================"
    echo ""
    
    echo "=== System Information ==="
    echo "--- Hostname ---"
    hostname || true
    echo ""
    echo "--- Uptime ---"
    uptime || true
    echo ""
    echo "--- Kernel Version ---"
    uname -a || true
    echo ""
    
    echo "=== Network Diagnostics ==="
    echo "--- IP Configuration ---"
    ip addr show || ifconfig -a || true
    echo ""
    echo "--- DNS Resolution ---"
    echo "Resolving TRUENAS_HOST: ${TRUENAS_HOST:-<not set>}"
    if [[ -n "${TRUENAS_HOST}" ]]; then
        nslookup "${TRUENAS_HOST}" || host "${TRUENAS_HOST}" || echo "DNS resolution failed"
        echo ""
        echo "--- Connectivity Test to TrueNAS ---"
        # Extract host from wss:// URL if it's a full URL
        TRUENAS_IP="${TRUENAS_HOST#wss://}"
        TRUENAS_IP="${TRUENAS_IP%%/*}"
        ping -c 3 "${TRUENAS_IP}" || echo "Ping to ${TRUENAS_IP} failed"
        echo ""
        echo "--- TCP Port Check (443) ---"
        nc -zv "${TRUENAS_IP}" 443 2>&1 || timeout 5 bash -c "cat < /dev/null > /dev/tcp/${TRUENAS_IP}/443" 2>&1 || echo "Port 443 connectivity check failed"
    fi
    echo ""
    
    echo "=== Journalctl Logs (kubelet/containerd - last 200 lines) ==="
    if command -v journalctl &>/dev/null; then
        echo "--- Kubelet Logs ---"
        journalctl -u kubelet --no-pager -n 200 2>&1 || echo "No kubelet journal logs available"
        echo ""
        echo "--- Containerd/CRI-O Logs ---"
        journalctl -u containerd --no-pager -n 100 2>&1 || journalctl -u crio --no-pager -n 100 2>&1 || echo "No container runtime journal logs available"
        echo ""
        echo "--- Kubesolo Logs ---"
        journalctl -u kubesolo --no-pager -n 200 2>&1 || echo "Not a kubesolo system"
        echo ""
        echo "--- K3s Logs ---"
        journalctl -u k3s --no-pager -n 200 2>&1 || echo "Not a k3s system"
        echo ""
        echo "--- K0s Logs ---"
        journalctl -u k0scontroller --no-pager -n 100 2>&1 || journalctl -u k0sworker --no-pager -n 100 2>&1 || echo "Not a k0s system"
    else
        echo "journalctl not available on this system"
    fi
    echo ""
    
    echo "=== Container Runtime Status ==="
    echo "--- Containerd Status ---"
    systemctl status containerd --no-pager -l 2>&1 || echo "Containerd not running or systemctl not available"
    echo ""
    echo "--- CRI-O Status ---"
    systemctl status crio --no-pager -l 2>&1 || echo "CRI-O not running or systemctl not available"
    echo ""
    
    echo "=== Controller Logs - tns-csi-plugin (last 300 lines) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        -c tns-csi-plugin \
        --tail=300 || true
    
    echo ""
    echo "=== Controller Logs - csi-provisioner (last 200 lines) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        -c csi-provisioner \
        --tail=200 || true
    
    echo ""
    echo "=== Controller Logs - csi-snapshotter (last 200 lines) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        -c csi-snapshotter \
        --tail=200 2>/dev/null || echo "csi-snapshotter sidecar not found (snapshots may not be enabled)"
    
    echo ""
    echo "=== Node Logs (last 200 lines) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
        --tail=200 || true
    
    echo ""
    echo "=== Snapshot-Related Resources ==="
    echo ""
    echo "--- VolumeSnapshots in test namespace ---"
    kubectl get volumesnapshot -n "${TEST_NAMESPACE}" -o yaml 2>/dev/null || echo "No VolumeSnapshots found or CRD not installed"
    
    echo ""
    echo "--- VolumeSnapshotContents (cluster-wide) ---"
    kubectl get volumesnapshotcontent -o yaml 2>/dev/null || echo "No VolumeSnapshotContents found or CRD not installed"
    
    echo ""
    echo "--- VolumeSnapshotClasses ---"
    kubectl get volumesnapshotclass -o yaml 2>/dev/null || echo "No VolumeSnapshotClasses found or CRD not installed"
    
    if [[ -n "${pvc_name}" ]]; then
        echo ""
        echo "=== PVC Status (describe) ==="
        kubectl describe pvc "${pvc_name}" -n "${TEST_NAMESPACE}" || true
        
        echo ""
        echo "=== PVC Status (YAML) ==="
        kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o yaml || true
        
        # Get associated PV
        local pv_name
        pv_name=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}' 2>/dev/null || echo "")
        if [[ -n "${pv_name}" ]]; then
            echo ""
            echo "=== Associated PV: ${pv_name} (YAML) ==="
            kubectl get pv "${pv_name}" -o yaml || true
        fi
    fi
    
    if [[ -n "${pod_name}" ]]; then
        echo ""
        echo "=== Pod Status (describe) ==="
        kubectl describe pod "${pod_name}" -n "${TEST_NAMESPACE}" || true
        
        echo ""
        echo "=== Pod Status (YAML) ==="
        kubectl get pod "${pod_name}" -n "${TEST_NAMESPACE}" -o yaml || true
        
        echo ""
        echo "=== Pod Logs ==="
        kubectl logs "${pod_name}" -n "${TEST_NAMESPACE}" || true
        
        # Try to show mount information if pod is running
        if kubectl get pod "${pod_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}' 2>/dev/null | grep -q "Running"; then
            show_pod_mounts "${pod_name}" "${TEST_NAMESPACE}" || true
        fi
    fi
    
    echo ""
    echo "=== All PVCs in test namespace ==="
    kubectl get pvc -n "${TEST_NAMESPACE}" -o wide || true
    
    echo ""
    echo "=== All PVCs (cluster-wide) ==="
    kubectl get pvc -A || true
    
    echo ""
    echo "=== All PVs ==="
    kubectl get pv -o wide || true
    
    echo ""
    echo "=== Events in test namespace ==="
    kubectl get events -n "${TEST_NAMESPACE}" --sort-by='.lastTimestamp' || true
    
    echo ""
    echo "=== CSI Driver Pods ==="
    kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver -o wide || true
    
    echo ""
    echo "========================================"
    echo "=== END DIAGNOSTIC INFORMATION ==="
    echo "========================================"
}

#######################################
# Save diagnostic logs to file for artifact collection
# Arguments:
#   Test name (for filename)
#   Pod name (optional)
#   PVC name (optional)
#   Output directory (optional, defaults to /tmp/test-logs)
#######################################
save_diagnostic_logs() {
    local test_name=$1
    local pod_name=${2:-}
    local pvc_name=${3:-}
    local output_dir=${4:-/tmp/test-logs}
    
    mkdir -p "${output_dir}"
    local log_file="${output_dir}/${test_name}-diagnostics-$(date +%Y%m%d-%H%M%S).log"
    
    echo "Saving diagnostic logs to: ${log_file}"
    
    {
        echo "========================================" 
        echo "Diagnostic Logs for: ${test_name}"
        echo "Timestamp: $(date)"
        echo "Namespace: ${TEST_NAMESPACE}"
        echo "========================================"
        echo ""
        
        show_diagnostic_logs "${pod_name}" "${pvc_name}"
        
    } > "${log_file}" 2>&1
    
    echo "Diagnostic logs saved to: ${log_file}"
    echo "LOG_FILE=${log_file}" >> "${GITHUB_OUTPUT:-/dev/null}" 2>/dev/null || true
}

#######################################
# Report test results summary
#######################################
report_test_results() {
    local total_tests=${#TEST_RESULTS[@]}
    declare -i passed=0
    declare -i failed=0
    declare -i total_duration=0
    
    echo ""
    echo "========================================"
    echo "TEST RESULTS SUMMARY"
    echo "========================================"
    echo ""
    
    for result in "${TEST_RESULTS[@]}"; do
        IFS=':' read -r test_name status duration <<< "${result}"
        
        if [[ "${status}" == "PASSED" ]]; then
            echo -e "${GREEN}✓${NC} ${test_name}"
            passed=$((passed + 1))
        else
            echo -e "${RED}✗${NC} ${test_name}"
            failed=$((failed + 1))
        fi
        
        if [[ -n "${duration}" && "${duration}" != "0" ]]; then
            echo -e "  Duration: ${duration}ms"
            total_duration=$((total_duration + duration))
        fi
        echo ""
    done
    
    local total_time=$(( $(date +%s) - TEST_START_TIME ))
    echo "========================================"
    echo "Total Tests: ${total_tests}"
    echo -e "Passed: ${GREEN}${passed}${NC}"
    echo -e "Failed: ${RED}${failed}${NC}"
    echo "Total Test Time: ${total_time}s"
    if [[ ${total_duration} -gt 0 ]]; then
        echo "Total Operation Time: ${total_duration}ms"
    fi
    echo "========================================"
}
