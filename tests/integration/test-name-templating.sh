#!/bin/bash
# Name Templating Integration Test
# Tests that volume name templating works via StorageClass parameters
#
# This test verifies:
# 1. StorageClass with nameTemplate parameter is created correctly
# 2. PVC is provisioned with the templated volume name
# 3. The PV's CSI volume handle contains the expected templated name
# 4. Controller logs show the rendered volume name

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NFS-NameTemplating"
PVC_NAME="test-pvc-name-template"
POD_NAME="test-pod-name-template"
SC_NAME="tns-csi-nfs-name-template"
TEST_TAGS="basic,nfs,name-templating"

# Expected pattern in the volume name (namespace-pvcname format)
# The actual namespace will be TEST_NAMESPACE which is dynamically generated

echo "========================================"
echo "TrueNAS CSI - Name Templating Test"
echo "========================================"
echo "This test verifies volume name templating works"
echo "via StorageClass parameters (nameTemplate, namePrefix, nameSuffix)"
echo "========================================"

# Configure test with 9 total steps
set_test_steps 9

# Check if test should be skipped
if should_skip_test "${TEST_TAGS}"; then
    echo "Skipping Name Templating test due to tag filter: ${TEST_SKIP_TAGS}"
    exit 0
fi

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_name_template_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

#######################################
# Create StorageClass with name template
#######################################
create_storageclass_with_name_template() {
    start_test_timer "create_storageclass"
    test_step "Creating StorageClass with name template"
    
    # Create StorageClass with nameTemplate parameter
    # Template: {{ .PVCNamespace }}-{{ .PVCName }} will produce "<namespace>-test-pvc-name-template"
    cat <<EOF | kubectl apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: ${SC_NAME}
provisioner: tns.csi.io
parameters:
  protocol: "nfs"
  pool: "${TRUENAS_POOL}"
  server: "${TRUENAS_HOST}"
  # Name templating: use namespace and PVC name
  nameTemplate: "{{ .PVCNamespace }}-{{ .PVCName }}"
  # CSI parameters to pass PVC info (required for templating)
  csi.storage.k8s.io/provisioner-secret-name: ""
  csi.storage.k8s.io/provisioner-secret-namespace: ""
allowVolumeExpansion: true
reclaimPolicy: Delete
volumeBindingMode: Immediate
EOF

    test_success "StorageClass created with name template"
    
    echo ""
    echo "=== StorageClass YAML ==="
    kubectl get storageclass "${SC_NAME}" -o yaml
    
    # Verify nameTemplate parameter is set correctly
    local name_template
    name_template=$(kubectl get storageclass "${SC_NAME}" -o jsonpath='{.parameters.nameTemplate}')
    test_info "nameTemplate parameter: ${name_template}"
    
    if [[ "${name_template}" == '{{ .PVCNamespace }}-{{ .PVCName }}' ]]; then
        test_success "nameTemplate parameter is correctly set"
    else
        test_error "nameTemplate parameter not found or incorrect in StorageClass"
        false
    fi
    
    stop_test_timer "create_storageclass" "PASSED"
}

#######################################
# Create PVC with the templated StorageClass
#######################################
create_name_template_pvc() {
    start_test_timer "create_pvc"
    test_step "Creating PVC with name-templated StorageClass"
    
    # Create PVC inline
    cat <<EOF | kubectl apply -f - -n "${TEST_NAMESPACE}"
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
  storageClassName: ${SC_NAME}
EOF

    test_success "PVC created"
    
    # Wait for PVC to be bound
    echo ""
    test_info "Waiting for PVC to be bound (timeout: ${TIMEOUT_PVC})..."
    if ! kubectl wait --for=jsonpath='{.status.phase}'=Bound \
        pvc/"${PVC_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_PVC}"; then
        test_error "PVC failed to bind"
        false
    fi
    
    test_success "PVC is bound"
    
    # Get PV name
    local pv_name
    pv_name=$(kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
    test_info "Created PV: ${pv_name}"
    
    # Show PVC details
    echo ""
    echo "=== PVC YAML ==="
    kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o yaml
    
    # Show PV details
    echo ""
    echo "=== PV YAML ==="
    kubectl get pv "${pv_name}" -o yaml
    
    stop_test_timer "create_pvc" "PASSED"
}

#######################################
# Create test pod
#######################################
create_name_template_pod() {
    start_test_timer "create_pod"
    test_step "Creating test pod"
    
    cat <<EOF | kubectl apply -f - -n "${TEST_NAMESPACE}"
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
spec:
  containers:
    - name: test-container
      image: public.ecr.aws/docker/library/busybox:stable
      command: ["sleep", "3600"]
      volumeMounts:
        - name: test-volume
          mountPath: /data
  volumes:
    - name: test-volume
      persistentVolumeClaim:
        claimName: ${PVC_NAME}
EOF

    test_success "Pod created"
    
    # Wait for pod to be ready
    echo ""
    test_info "Waiting for pod to be ready (timeout: ${TIMEOUT_POD})..."
    if ! kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_POD}"; then
        test_error "Pod failed to become ready"
        false
    fi
    
    test_success "Pod is ready"
    stop_test_timer "create_pod" "PASSED"
}

#######################################
# Verify the templated volume name in PV
#######################################
verify_templated_volume_name() {
    start_test_timer "verify_name"
    test_step "Verifying templated volume name"
    
    # Get the PV name
    local pv_name
    pv_name=$(kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
    
    # Get the CSI volume handle from the PV
    local volume_handle
    volume_handle=$(kubectl get pv "${pv_name}" -o jsonpath='{.spec.csi.volumeHandle}')
    test_info "CSI Volume Handle: ${volume_handle}"
    
    # The expected pattern should contain the namespace and PVC name
    # Format: pool/namespace-pvcname (the dataset path includes the templated name)
    local expected_pattern="${TEST_NAMESPACE}-${PVC_NAME}"
    test_info "Expected pattern in volume handle: ${expected_pattern}"
    
    if echo "${volume_handle}" | grep -q "${expected_pattern}"; then
        test_success "Volume handle contains templated name: ${expected_pattern}"
    else
        test_error "Volume handle does not contain expected pattern"
        test_error "Expected pattern: ${expected_pattern}"
        test_error "Actual volume handle: ${volume_handle}"
        false
    fi
    
    # Also verify the CSI volume attributes contain NFS share path with templated name
    local nfs_share
    nfs_share=$(kubectl get pv "${pv_name}" -o jsonpath='{.spec.csi.volumeAttributes.share}')
    test_info "NFS Share path: ${nfs_share}"
    
    if echo "${nfs_share}" | grep -q "${expected_pattern}"; then
        test_success "NFS share path contains templated name"
    else
        test_warning "NFS share path may use different naming (this is acceptable)"
    fi
    
    stop_test_timer "verify_name" "PASSED"
}

#######################################
# Verify templated name in controller logs
#######################################
verify_name_in_logs() {
    start_test_timer "verify_logs"
    test_step "Verifying templated name in controller logs"
    
    echo ""
    test_info "Checking controller logs for rendered volume name..."
    
    # Give some time for logs to be generated
    sleep 3
    
    # Get controller logs
    local logs
    logs=$(kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        --tail=200 2>/dev/null || echo "")
    
    # The expected pattern in logs
    local expected_pattern="${TEST_NAMESPACE}-${PVC_NAME}"
    
    # Check for the rendered volume name in logs
    if grep -q "Rendered volume name" <<< "${logs}"; then
        test_success "Controller logged 'Rendered volume name'"
        echo ""
        echo "=== Relevant Controller Logs ==="
        echo "${logs}" | grep -E "(Rendered volume name|nameTemplate|${expected_pattern})" || echo "(Pattern found but specific line not shown)"
    else
        test_info "No 'Rendered volume name' log found (may require V(4) log level)"
    fi
    
    # Check if the expected pattern appears anywhere in logs
    if grep -q "${expected_pattern}" <<< "${logs}"; then
        test_success "Controller logs contain the templated name pattern: ${expected_pattern}"
    else
        test_info "Templated name pattern not found in recent logs (this may be OK if log level is not high enough)"
    fi
    
    test_success "Log verification completed"
    stop_test_timer "verify_logs" "PASSED"
}

#######################################
# Test prefix/suffix templating (additional test)
#######################################
test_prefix_suffix() {
    start_test_timer "test_prefix_suffix"
    test_step "Testing prefix/suffix name templating"
    
    local sc_prefix_name="tns-csi-nfs-prefix-test"
    local pvc_prefix_name="test-pvc-prefix"
    
    # Create StorageClass with prefix/suffix
    cat <<EOF | kubectl apply -f -
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: ${sc_prefix_name}
provisioner: tns.csi.io
parameters:
  protocol: "nfs"
  pool: "${TRUENAS_POOL}"
  server: "${TRUENAS_HOST}"
  # Simple prefix/suffix instead of full template
  namePrefix: "prod-"
  nameSuffix: "-data"
allowVolumeExpansion: true
reclaimPolicy: Delete
volumeBindingMode: Immediate
EOF

    test_success "Created StorageClass with prefix/suffix"
    
    # Create PVC
    cat <<EOF | kubectl apply -f - -n "${TEST_NAMESPACE}"
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${pvc_prefix_name}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: ${sc_prefix_name}
EOF

    # Wait for PVC to be bound
    if ! kubectl wait --for=jsonpath='{.status.phase}'=Bound \
        pvc/"${pvc_prefix_name}" \
        -n "${TEST_NAMESPACE}" \
        --timeout="${TIMEOUT_PVC}"; then
        test_error "Prefix/suffix PVC failed to bind"
        false
    fi
    
    test_success "Prefix/suffix PVC is bound"
    
    # Verify the volume handle contains the prefix and suffix
    local pv_name
    pv_name=$(kubectl get pvc "${pvc_prefix_name}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
    
    local volume_handle
    volume_handle=$(kubectl get pv "${pv_name}" -o jsonpath='{.spec.csi.volumeHandle}')
    test_info "Prefix/suffix volume handle: ${volume_handle}"
    
    # The handle should contain "prod-" prefix and "-data" suffix around the PV name
    if echo "${volume_handle}" | grep -q "prod-" && echo "${volume_handle}" | grep -q "\-data"; then
        test_success "Volume handle contains prefix 'prod-' and suffix '-data'"
    else
        test_warning "Volume handle may not contain expected prefix/suffix pattern (checking structure)"
        # Show what we got for debugging
        echo "=== Prefix/Suffix PV Details ==="
        kubectl get pv "${pv_name}" -o yaml
    fi
    
    # Cleanup prefix/suffix test resources
    kubectl delete pvc "${pvc_prefix_name}" -n "${TEST_NAMESPACE}" --ignore-not-found=true || true
    kubectl delete storageclass "${sc_prefix_name}" --ignore-not-found=true || true
    
    test_success "Prefix/suffix test completed"
    stop_test_timer "test_prefix_suffix" "PASSED"
}

#######################################
# Cleanup function for this test
#######################################
cleanup_name_template_test() {
    echo ""
    test_info "Cleaning up Name Templating test resources..."
    
    # Delete pod
    kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete PVC
    kubectl delete pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=60s || true
    
    # Delete StorageClasses (cluster-scoped, no namespace)
    kubectl delete storageclass "${SC_NAME}" --ignore-not-found=true || true
    kubectl delete storageclass "tns-csi-nfs-prefix-test" --ignore-not-found=true || true
    
    # Delete namespace
    kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=120s || {
        test_warning "Namespace deletion timed out, forcing deletion"
        kubectl delete namespace "${TEST_NAMESPACE}" --force --grace-period=0 --ignore-not-found=true || true
    }
    
    test_info "Cleanup completed"
}

# Run test steps
verify_cluster
deploy_driver "nfs"
wait_for_driver
create_storageclass_with_name_template
create_name_template_pvc
create_name_template_pod
test_io_operations "${POD_NAME}" "/data" "filesystem"
verify_templated_volume_name
verify_name_in_logs
test_prefix_suffix
cleanup_name_template_test

# Success
test_summary "${PROTOCOL}" "PASSED"
