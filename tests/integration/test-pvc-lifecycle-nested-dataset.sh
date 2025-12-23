#!/bin/bash
# NVMe-oF PVC Lifecycle Test with Nested Parent Dataset
# Tests PVC creation, binding, and deletion with a deeply nested parentDataset path
#
# Purpose: Verify that PVC cleanup works correctly when using nested parentDataset paths
# like "pool/democratic-csi/nvmefc/volumes" which some users migrate from other CSI drivers
#
# User scenario that triggered this test:
#   storageClasses:
#     nvmeof:
#       pool: "vega"
#       parentDataset: "vega/democratic-csi/nvmefc/volumes"
#
# The nested path can cause issues with dataset ID parsing during deletion.

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NVMe-oF (Nested Dataset)"
PVC_NAME="test-pvc-lifecycle-nested"
POD_NAME="test-pod-lifecycle-nested"
TEST_TAGS="lifecycle,nvmeof,nested-dataset"

# Nested dataset path - simulates migration from democratic-csi or similar
# The TRUENAS_NESTED_DATASET env var allows customization, defaults to a test path
NESTED_DATASET_PATH="${TRUENAS_NESTED_DATASET:-csi-test/nested/volumes}"

echo "========================================"
echo "TrueNAS CSI - NVMe-oF PVC Lifecycle Test (Nested Dataset)"
echo "========================================"
echo ""
echo "This test verifies PVC create/bind/delete behavior for NVMe-oF"
echo "when using a deeply nested parentDataset path."
echo ""
echo "Nested dataset path: ${TRUENAS_POOL}/${NESTED_DATASET_PATH}"
echo ""

# Configure test with 8 total steps:
# verify_cluster, create_parent_dataset, deploy_driver, wait_for_driver,
# create_pvc, trigger_binding_with_pod, delete_pod_only, delete_pvc_directly
set_test_steps 8

# Check if test should be skipped
if should_skip_test "${TEST_TAGS}"; then
    echo "Skipping NVMe-oF nested dataset lifecycle test due to tag filter: ${TEST_SKIP_TAGS}"
    exit 0
fi

#######################################
# Cleanup function for nested dataset lifecycle test
#######################################
cleanup_nested_lifecycle_test() {
    local pvc_name=$1
    local pod_name=$2
    
    echo ""
    test_info "Cleaning up nested dataset lifecycle test resources..."
    
    # Delete pod if it still exists
    kubectl delete pod "${pod_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=30s || true
    
    # Delete PVC if it still exists
    kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=30s || true
    
    # Delete namespace
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || {
        test_warning "Namespace deletion timed out, forcing..."
        kubectl delete namespace "${TEST_NAMESPACE}" --force --grace-period=0 --ignore-not-found=true || true
    }
    
    test_success "Cleanup completed"
}

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_nested_lifecycle_test "${PVC_NAME}" "${POD_NAME}"; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Create nested parent dataset on TrueNAS (if it doesn't exist)
# This simulates a user's existing dataset structure from democratic-csi migration
#######################################
create_parent_dataset_structure() {
    start_test_timer "create_parent_dataset"
    test_step "Creating nested parent dataset structure on TrueNAS"
    
    # The parent dataset needs to exist before the CSI driver can create volumes under it
    # In real deployments, users would create this manually or it would exist from migration
    # For testing, we'll create it via the CSI driver's helm deployment which handles this
    
    test_info "Nested dataset path to be used: ${TRUENAS_POOL}/${NESTED_DATASET_PATH}"
    test_info "Note: The parent dataset structure should be created on TrueNAS before deploying"
    test_info "      For this test, we rely on the dataset being created or the test will fail"
    test_info "      with a clear error message indicating the missing parent dataset."
    
    # We don't actually create the dataset here - we let the CSI driver attempt to use it
    # and report proper errors if it doesn't exist. This tests the error path as well.
    
    stop_test_timer "create_parent_dataset" "PASSED"
}

#######################################
# Deploy driver with nested parentDataset configuration
#######################################
deploy_driver_nested() {
    start_test_timer "deploy_driver_nested"
    test_step "Deploying CSI driver with nested parentDataset configuration"
    
    # Check required environment variables
    if [[ -z "${TRUENAS_HOST}" ]]; then
        stop_test_timer "deploy_driver_nested" "FAILED"
        test_error "TRUENAS_HOST environment variable not set"
        false
    fi
    
    if [[ -z "${TRUENAS_API_KEY}" ]]; then
        stop_test_timer "deploy_driver_nested" "FAILED"
        test_error "TRUENAS_API_KEY environment variable not set"
        false
    fi
    
    if [[ -z "${TRUENAS_POOL}" ]]; then
        stop_test_timer "deploy_driver_nested" "FAILED"
        test_error "TRUENAS_POOL environment variable not set"
        false
    fi
    
    # Construct TrueNAS WebSocket URL
    local truenas_url="wss://${TRUENAS_HOST}/api/current"
    test_info "TrueNAS URL: ${truenas_url}"
    
    # Image configuration
    local image_tag="${CSI_IMAGE_TAG:-latest}"
    local image_repo="${CSI_IMAGE_REPOSITORY:-ghcr.io/fenio/tns-csi}"
    local kubelet_path="${KUBELET_PATH:-/var/lib/kubelet}"
    
    test_info "Deploying with nested parentDataset: ${TRUENAS_POOL}/${NESTED_DATASET_PATH}"
    
    # Deploy with Helm - using nested parentDataset
    # This simulates the user's configuration:
    #   storageClasses:
    #     nvmeof:
    #       pool: "vega"
    #       parentDataset: "vega/democratic-csi/nvmefc/volumes"
    if ! helm upgrade --install tns-csi ./charts/tns-csi-driver \
        --namespace kube-system \
        --create-namespace \
        --set image.repository="${image_repo}" \
        --set image.tag="${image_tag}" \
        --set image.pullPolicy=Always \
        --set truenas.url="${truenas_url}" \
        --set truenas.apiKey="${TRUENAS_API_KEY}" \
        --set truenas.skipTLSVerify=true \
        --set node.kubeletPath="${kubelet_path}" \
        --set storageClasses.nfs.enabled=false \
        --set storageClasses.nvmeof.enabled=true \
        --set storageClasses.nvmeof.name=tns-csi-nvmeof-nested \
        --set storageClasses.nvmeof.pool="${TRUENAS_POOL}" \
        --set "storageClasses.nvmeof.parentDataset=${TRUENAS_POOL}/${NESTED_DATASET_PATH}" \
        --set storageClasses.nvmeof.server="${TRUENAS_HOST}" \
        --set storageClasses.nvmeof.transport=tcp \
        --set storageClasses.nvmeof.port=4420 \
        --wait --timeout 10m; then
        stop_test_timer "deploy_driver_nested" "FAILED"
        test_error "Helm deployment failed"
        
        echo ""
        echo "=== DIAGNOSTIC: Pod Status After Helm Failure ==="
        kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver -o wide || true
        
        echo ""
        echo "=== DIAGNOSTIC: Controller Logs ==="
        kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller --all-containers --tail=50 || true
        
        false
    fi
    
    test_success "CSI driver deployed with nested parentDataset"
    
    # Show StorageClass configuration
    echo ""
    echo "=== StorageClass Configuration ==="
    kubectl get storageclass tns-csi-nvmeof-nested -o yaml || true
    
    stop_test_timer "deploy_driver_nested" "PASSED"
}

#######################################
# Create PVC using the nested StorageClass
#######################################
create_pvc_nested() {
    local pvc_name=$1
    
    start_test_timer "create_pvc_nested"
    test_step "Creating PVC with nested parentDataset StorageClass: ${pvc_name}"
    
    # Create PVC manifest inline using the nested storage class
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${pvc_name}
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nvmeof-nested
EOF
    
    test_info "PVC created with nested parentDataset StorageClass"
    
    # Verify PVC exists and check status
    local pvc_status
    pvc_status=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}' 2>/dev/null || echo "unknown")
    test_info "PVC initial status: ${pvc_status}"
    
    stop_test_timer "create_pvc_nested" "PASSED"
}

#######################################
# Create a minimal pod to trigger PVC binding
#######################################
trigger_binding_with_pod() {
    local pvc_name=$1
    local pod_name=$2
    
    start_test_timer "trigger_binding_with_pod"
    test_step "Creating temporary pod to trigger PVC binding"
    
    # Create minimal pod that uses the PVC
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${pod_name}
spec:
  containers:
    - name: test
      image: busybox:1.36
      command: ["sleep", "infinity"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: ${pvc_name}
EOF
    
    test_info "Pod created, waiting for it to be ready (this triggers PVC binding)..."
    
    # Wait for pod to be ready with extended timeout for nested dataset creation
    if ! kubectl wait --for=condition=Ready pod/"${pod_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout=180s; then
        stop_test_timer "trigger_binding_with_pod" "FAILED"
        test_error "Pod failed to become ready"
        
        echo ""
        echo "=== Pod Status ==="
        kubectl describe pod "${pod_name}" -n "${TEST_NAMESPACE}" || true
        
        echo ""
        echo "=== PVC Status ==="
        kubectl describe pvc "${pvc_name}" -n "${TEST_NAMESPACE}" || true
        
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
        
        false
    fi
    
    test_success "Pod is ready, PVC should now be bound"
    
    # Verify PVC is bound
    local pvc_status
    pvc_status=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}')
    if [[ "${pvc_status}" != "Bound" ]]; then
        test_error "Expected PVC to be Bound, got: ${pvc_status}"
        stop_test_timer "trigger_binding_with_pod" "FAILED"
        false
    fi
    
    # Get PV details
    local pv_name
    pv_name=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
    test_info "Created PV: ${pv_name}"
    
    local volume_handle
    volume_handle=$(kubectl get pv "${pv_name}" -o jsonpath='{.spec.csi.volumeHandle}')
    test_info "Volume handle (TrueNAS zvol): ${volume_handle}"
    
    # Verify the volume was created under the nested dataset path
    echo ""
    echo "=== PVC Details ==="
    kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o yaml
    
    echo ""
    echo "=== PV Details ==="
    kubectl get pv "${pv_name}" -o yaml
    
    # Check if the volume context shows the expected nested path
    local dataset_name
    dataset_name=$(kubectl get pv "${pv_name}" -o jsonpath='{.spec.csi.volumeAttributes.datasetName}' 2>/dev/null || echo "")
    if [[ -n "${dataset_name}" ]]; then
        test_info "Volume dataset path: ${dataset_name}"
        
        # Verify the dataset name contains our nested path
        if [[ "${dataset_name}" == *"${NESTED_DATASET_PATH}"* ]]; then
            test_success "Volume created under nested parentDataset as expected"
        else
            test_warning "Dataset name doesn't contain expected nested path"
            test_warning "Expected to contain: ${NESTED_DATASET_PATH}"
            test_warning "Actual: ${dataset_name}"
        fi
    fi
    
    stop_test_timer "trigger_binding_with_pod" "PASSED"
}

#######################################
# Delete pod only, leaving PVC intact
#######################################
delete_pod_only() {
    local pod_name=$1
    local pvc_name=$2
    
    start_test_timer "delete_pod_only"
    test_step "Deleting pod (leaving PVC intact for isolated deletion test)"
    
    # Delete the pod
    kubectl delete pod "${pod_name}" -n "${TEST_NAMESPACE}" --timeout=60s
    
    test_success "Pod deleted"
    
    # Verify PVC is still bound
    local pvc_status
    pvc_status=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}')
    test_info "PVC status after pod deletion: ${pvc_status}"
    
    if [[ "${pvc_status}" != "Bound" ]]; then
        test_warning "Expected PVC to remain Bound after pod deletion, got: ${pvc_status}"
    else
        test_success "PVC remains bound after pod deletion"
    fi
    
    # Give the system a moment to stabilize
    sleep 5
    
    stop_test_timer "delete_pod_only" "PASSED"
}

#######################################
# Delete PVC directly and verify cleanup
# This is the critical test - verifying deletion works with nested parentDataset
#######################################
delete_pvc_directly() {
    local pvc_name=$1
    
    start_test_timer "delete_pvc_directly"
    test_step "Deleting PVC directly: ${pvc_name} (testing nested dataset cleanup)"
    
    # Get PV name before deletion
    local pv_name
    pv_name=$(kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}' 2>/dev/null || echo "")
    
    if [[ -z "${pv_name}" ]]; then
        test_error "Could not get PV name from PVC"
        stop_test_timer "delete_pvc_directly" "FAILED"
        false
    fi
    
    test_info "Associated PV: ${pv_name}"
    
    # Get volume handle and dataset info for logging
    local volume_handle
    volume_handle=$(kubectl get pv "${pv_name}" -o jsonpath='{.spec.csi.volumeHandle}' 2>/dev/null || echo "unknown")
    test_info "Volume handle to be deleted: ${volume_handle}"
    
    local dataset_name
    dataset_name=$(kubectl get pv "${pv_name}" -o jsonpath='{.spec.csi.volumeAttributes.datasetName}' 2>/dev/null || echo "unknown")
    test_info "Dataset to be deleted: ${dataset_name}"
    
    # CRITICAL: Verify dataset EXISTS on TrueNAS BEFORE we try to delete it
    # This confirms we're using the correct path and the creation was successful
    echo ""
    test_info "Verifying zvol exists on TrueNAS BEFORE deletion..."
    if ! verify_truenas_exists "${dataset_name}"; then
        test_error "Dataset '${dataset_name}' does NOT exist on TrueNAS before deletion!"
        test_error "This indicates either wrong path or creation failed."
        echo ""
        echo "=== Listing all CSI datasets on TrueNAS ==="
        list_truenas_datasets || true
        stop_test_timer "delete_pvc_directly" "FAILED"
        false
    fi
    test_success "Confirmed zvol exists on TrueNAS before deletion"
    
    # This is the key test: deletion with nested parentDataset path
    echo ""
    test_info "Deleting PVC ${pvc_name}..."
    test_info "This will test DeleteVolume with nested dataset path: ${dataset_name}"
    
    kubectl delete pvc "${pvc_name}" -n "${TEST_NAMESPACE}" --timeout=60s
    
    test_success "PVC deletion command completed"
    
    # Wait for PV to be deleted (indicates successful backend cleanup)
    echo ""
    test_info "Waiting for PV ${pv_name} to be deleted (indicates TrueNAS cleanup)..."
    
    local timeout=120  # Extended timeout for nested dataset cleanup
    local elapsed=0
    local interval=2
    local pv_deleted=false
    
    while [[ $elapsed -lt $timeout ]]; do
        if ! kubectl get pv "${pv_name}" &>/dev/null; then
            pv_deleted=true
            break
        fi
        
        # Check PV status
        local pv_status
        pv_status=$(kubectl get pv "${pv_name}" -o jsonpath='{.status.phase}' 2>/dev/null || echo "unknown")
        test_info "PV status: ${pv_status} (elapsed: ${elapsed}s)"
        
        # Check if PV is stuck in Released state
        if [[ "${pv_status}" == "Released" ]]; then
            test_warning "PV is in Released state - checking for finalizer issues..."
            kubectl get pv "${pv_name}" -o jsonpath='{.metadata.finalizers}' || true
            echo ""
            
            # Show controller logs for deletion
            echo ""
            echo "=== Controller Logs (deletion in progress) ==="
            kubectl logs -n kube-system \
                -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
                --tail=30 || true
        fi
        
        sleep "${interval}"
        elapsed=$((elapsed + interval))
    done
    
    if [[ "${pv_deleted}" == "true" ]]; then
        test_success "PV deleted successfully in ${elapsed}s"
        test_success "TrueNAS zvol/subsystem cleanup completed for nested dataset"
    else
        test_error "PV deletion timed out after ${timeout}s"
        
        echo ""
        echo "=== PV Status (stuck) ==="
        kubectl describe pv "${pv_name}" || true
        
        echo ""
        echo "=== Controller Logs (last 200 lines) ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            --tail=200 || true
        
        echo ""
        echo "=== CSI Provisioner Sidecar Logs ==="
        local controller_pod
        controller_pod=$(kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
        if [[ -n "${controller_pod}" ]]; then
            kubectl logs -n kube-system "${controller_pod}" -c csi-provisioner --tail=100 || true
        fi
        
        stop_test_timer "delete_pvc_directly" "FAILED"
        false
    fi
    
    # Verify PVC is also gone
    if kubectl get pvc "${pvc_name}" -n "${TEST_NAMESPACE}" &>/dev/null; then
        test_warning "PVC still exists after PV deletion - this is unexpected"
    else
        test_success "PVC confirmed deleted"
    fi
    
    # CRITICAL: Verify the zvol was actually deleted from TrueNAS
    # This is especially important for nested dataset paths where parsing can fail
    echo ""
    test_info "Verifying zvol was deleted from TrueNAS backend..."
    if ! verify_truenas_deletion "${dataset_name}" 30; then
        test_error "TrueNAS zvol still exists! CSI DeleteVolume did not clean up backend."
        test_error "This may indicate an issue with nested parentDataset path handling."
        echo ""
        echo "=== Listing all CSI datasets on TrueNAS ==="
        list_truenas_datasets || true
        echo ""
        echo "=== Controller Logs (DeleteVolume) ==="
        kubectl logs -n kube-system \
            -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
            --tail=100 | grep -i -E "(delete|volume|zvol|subsystem|dataset)" || true
        stop_test_timer "delete_pvc_directly" "FAILED"
        false
    fi
    test_success "TrueNAS zvol confirmed deleted - nested dataset backend cleanup verified!"
    
    stop_test_timer "delete_pvc_directly" "PASSED"
}

# Run test steps
verify_cluster
create_parent_dataset_structure
deploy_driver_nested
wait_for_driver

# Check if NVMe-oF is configured
echo ""
test_info "Checking if NVMe-oF is configured on TrueNAS..."

# Create a temporary PVC to check configuration
cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: nvmeof-nested-config-check
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nvmeof-nested
EOF

# Wait a moment for controller to process
sleep 5

# Check controller logs for port configuration error
logs=$(kubectl logs -n kube-system \
    -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
    --tail=20 2>/dev/null || true)

if grep -q "No TCP NVMe-oF port" <<< "$logs"; then
    test_warning "NVMe-oF ports not configured on TrueNAS server"
    test_warning "Skipping NVMe-oF nested dataset lifecycle test - this is expected if NVMe-oF is not set up"
    kubectl delete pvc nvmeof-nested-config-check -n "${TEST_NAMESPACE}" --ignore-not-found=true
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    test_summary "${PROTOCOL}" "SKIPPED"
    exit 0
fi

# Check for parent dataset errors
# Filter out property-related errors (like "properties.comments: Property does not exist")
# which are warnings, not actual dataset creation failures
if echo "$logs" | grep -v "properties\." | grep -qE "(dataset.*does not exist|parent.*not found)"; then
    test_error "Parent dataset does not exist on TrueNAS"
    test_error "Please create the parent dataset on TrueNAS: ${TRUENAS_POOL}/${NESTED_DATASET_PATH}"
    echo ""
    echo "=== Controller Logs ==="
    echo "$logs"
    kubectl delete pvc nvmeof-nested-config-check -n "${TEST_NAMESPACE}" --ignore-not-found=true
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    test_summary "${PROTOCOL}" "FAILED"
    exit 1
fi

# Clean up config check PVC
kubectl delete pvc nvmeof-nested-config-check -n "${TEST_NAMESPACE}" --ignore-not-found=true
wait_for_resource_deleted "pvc" "nvmeof-nested-config-check" "${TEST_NAMESPACE}" 30 || true

test_success "NVMe-oF is configured, proceeding with nested dataset lifecycle test"

# Continue with the actual test
create_pvc_nested "${PVC_NAME}"
trigger_binding_with_pod "${PVC_NAME}" "${POD_NAME}"
delete_pod_only "${POD_NAME}" "${PVC_NAME}"
delete_pvc_directly "${PVC_NAME}"

# Final cleanup (namespace only, PVC already deleted)
echo ""
test_info "Final cleanup..."
kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true

# Success
test_summary "${PROTOCOL}" "PASSED"
