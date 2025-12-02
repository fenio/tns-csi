#!/bin/bash
# Btrfs Filesystem Integration Test
# Tests NVMe-oF volumes with btrfs filesystem
#
# This test validates that:
# 1. NVMe-oF volumes can be formatted with btrfs
# 2. Btrfs volumes can be mounted and used
# 3. Volume expansion works with btrfs
# 4. Basic I/O operations work correctly

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="Btrfs Filesystem"
STORAGE_CLASS_NAME="tns-csi-nvmeof-btrfs"
PVC_NAME="test-pvc-btrfs"
POD_NAME="test-pod-btrfs"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

# Test steps: cluster, deploy, check nvmeof, driver ready, create SC, 
# create PVC, create pod, verify btrfs, test I/O, test expansion, cleanup
set_test_steps 11

echo "========================================"
echo "TrueNAS CSI - Btrfs Filesystem Test"
echo "========================================"
echo ""
echo "This test validates btrfs filesystem support for NVMe-oF volumes."
echo ""

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_btrfs_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Create StorageClass with btrfs filesystem
#######################################
create_btrfs_storage_class() {
    test_step "Creating StorageClass with btrfs filesystem"
    
    # NVMe-oF subsystem NQN - use env var or default
    local subsystem_nqn="${NVMEOF_SUBSYSTEM_NQN:-nqn.2005-03.org.truenas:csi-test}"
    
    cat <<EOF | kubectl apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: ${STORAGE_CLASS_NAME}
provisioner: tns.csi.io
parameters:
  protocol: nvmeof
  pool: "${TRUENAS_POOL}"
  server: "${TRUENAS_HOST}"
  transport: tcp
  port: "4420"
  subsystemNQN: "${subsystem_nqn}"
  fsType: btrfs
reclaimPolicy: Delete
volumeBindingMode: Immediate
allowVolumeExpansion: true
EOF
    
    test_success "StorageClass created with fsType=btrfs"
    
    echo ""
    echo "=== StorageClass Details ==="
    kubectl get storageclass "${STORAGE_CLASS_NAME}" -o yaml
}

#######################################
# Create PVC with btrfs storage class
#######################################
create_btrfs_pvc() {
    test_step "Creating PVC with btrfs StorageClass"
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME}
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: 2Gi
  storageClassName: ${STORAGE_CLASS_NAME}
EOF
    
    # Wait for PVC to be bound
    echo ""
    test_info "Waiting for PVC to be bound (timeout: ${TIMEOUT_PVC})..."
    kubectl wait --for=jsonpath='{.status.phase}'=Bound \
        pvc/"${PVC_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_PVC}"
    
    test_success "PVC is bound"
    
    echo ""
    echo "=== PVC Details ==="
    kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o yaml
}

#######################################
# Create test pod
#######################################
create_btrfs_test_pod() {
    test_step "Creating test pod to mount btrfs volume"
    
    cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
spec:
  containers:
  - name: test-container
    image: busybox:latest
    command: ["sleep", "3600"]
    securityContext:
      privileged: true
    volumeMounts:
    - name: test-volume
      mountPath: /data
  volumes:
  - name: test-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME}
EOF
    
    # Wait for pod to be ready
    echo ""
    test_info "Waiting for pod to be ready (timeout: ${TIMEOUT_POD})..."
    kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"
    
    test_success "Pod is ready"
}

#######################################
# Verify btrfs filesystem
#######################################
verify_btrfs_filesystem() {
    test_step "Verifying btrfs filesystem"
    
    echo ""
    echo "=== Node Driver Logs (format/mount) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
        --tail=50 | grep -iE "btrfs|format|mkfs|mount" || true
    
    # Check filesystem type
    echo ""
    test_info "Checking filesystem type..."
    
    # Get mount info
    local mount_info
    mount_info=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /proc/mounts | grep "/data" || echo "")
    
    if [[ -z "${mount_info}" ]]; then
        test_error "Could not find mount point /data in /proc/mounts"
        return 1
    fi
    
    test_info "Mount info: ${mount_info}"
    
    # Check for btrfs
    if echo "${mount_info}" | grep -q "btrfs"; then
        test_success "Volume is mounted with btrfs filesystem"
    else
        test_error "Volume is NOT mounted with btrfs"
        echo ""
        echo "=== All mounts ==="
        kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /proc/mounts
        return 1
    fi
    
    # Show filesystem details
    echo ""
    echo "=== Filesystem details ==="
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- df -Th /data
    
    # Try to show btrfs-specific info (may fail if btrfs tools not in busybox)
    echo ""
    test_info "Attempting to show btrfs filesystem info..."
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        sh -c "btrfs filesystem show /data 2>/dev/null || echo 'btrfs tools not available in container'" || true
}

#######################################
# Test I/O operations on btrfs
#######################################
test_btrfs_io() {
    test_step "Testing I/O operations on btrfs volume"
    
    # Write test data
    test_info "Writing test data..."
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Btrfs Test Data' > /data/test.txt"
    
    local content
    content=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt)
    
    if [[ "${content}" == "Btrfs Test Data" ]]; then
        test_success "Write/read operation successful: ${content}"
    else
        test_error "Data mismatch: ${content}"
        return 1
    fi
    
    # Write larger file
    test_info "Writing larger file..."
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        dd if=/dev/zero of=/data/largefile.bin bs=1M count=100 2>&1 | tail -3
    
    test_success "Large file write successful"
    
    # Verify file
    echo ""
    echo "=== Volume contents ==="
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- ls -lh /data/
    
    echo ""
    echo "=== Disk usage ==="
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- df -h /data
    
    test_success "I/O operations completed successfully on btrfs"
}

#######################################
# Test volume expansion with btrfs
#######################################
test_btrfs_expansion() {
    test_step "Testing volume expansion with btrfs"
    
    local new_size="4Gi"
    
    # Get current size
    local current_size
    current_size=$(kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.resources.requests.storage}')
    test_info "Current PVC size: ${current_size}"
    
    echo ""
    echo "=== Current filesystem usage ==="
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- df -h /data
    
    # Expand PVC
    test_info "Expanding PVC from ${current_size} to ${new_size}..."
    kubectl patch pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" \
        -p "{\"spec\":{\"resources\":{\"requests\":{\"storage\":\"${new_size}\"}}}}"
    
    test_success "PVC expansion request submitted"
    
    # Wait for expansion to complete
    echo ""
    test_info "Waiting for volume expansion to complete..."
    
    local retries=0
    local max_retries=60
    while [[ $retries -lt $max_retries ]]; do
        local status_capacity
        status_capacity=$(kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.capacity.storage}' 2>/dev/null || echo "")
        
        if [[ "${status_capacity}" == "${new_size}" ]]; then
            test_success "Volume expanded to ${new_size}"
            break
        fi
        
        sleep 2
        retries=$((retries + 1))
    done
    
    if [[ $retries -eq $max_retries ]]; then
        test_error "Timeout waiting for volume expansion"
        kubectl describe pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}"
        return 1
    fi
    
    # Show node logs for btrfs resize
    echo ""
    echo "=== Node Driver Logs (expansion) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
        --tail=30 | grep -iE "btrfs|resize|expand" || true
    
    # Verify filesystem sees new size
    echo ""
    echo "=== Filesystem after expansion ==="
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- df -h /data
    
    # Test I/O after expansion
    test_info "Testing I/O after expansion..."
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Post-expansion test' > /data/expansion-test.txt"
    
    local new_content
    new_content=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/expansion-test.txt)
    
    if [[ "${new_content}" == "Post-expansion test" ]]; then
        test_success "I/O works after btrfs expansion"
    else
        test_error "I/O failed after expansion"
        return 1
    fi
    
    test_success "Btrfs volume expansion completed successfully"
}

#######################################
# Cleanup test resources
#######################################
cleanup_btrfs_test() {
    test_step "Cleaning up btrfs test resources"
    
    # Delete pod
    test_info "Deleting pod..."
    kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete PVC
    test_info "Deleting PVC..."
    kubectl delete pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete StorageClass
    test_info "Deleting StorageClass..."
    kubectl delete storageclass "${STORAGE_CLASS_NAME}" --ignore-not-found=true || true
    
    # Delete namespace
    test_info "Deleting test namespace: ${TEST_NAMESPACE}"
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=120s || {
        test_warning "Namespace deletion timed out, forcing deletion"
        kubectl delete namespace "${TEST_NAMESPACE}" --force --grace-period=0 --ignore-not-found=true || true
    }
    
    # Wait for cleanup
    test_info "Waiting for TrueNAS backend cleanup..."
    sleep 15
    
    test_success "Cleanup complete"
}

# Run test steps
verify_cluster
deploy_driver "nvmeof"
wait_for_driver

# Check if NVMe-oF is configured before proceeding
# Create a temporary PVC to test if NVMe-oF ports are configured
test_step "Checking NVMe-oF configuration"
if ! check_nvmeof_configured "${MANIFEST_DIR}/pvc-nvmeof.yaml" "test-pvc-nvmeof" "Btrfs/NVMe-oF"; then
    exit 0  # Skip test gracefully
fi

create_btrfs_storage_class
create_btrfs_pvc
create_btrfs_test_pod
verify_btrfs_filesystem
test_btrfs_io
test_btrfs_expansion

# Success
cleanup_btrfs_test
test_summary "${PROTOCOL}" "PASSED"
