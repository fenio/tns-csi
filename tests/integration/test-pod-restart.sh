#!/bin/bash
# Pod Restart/Rescheduling Test
# Verifies that volumes can be properly reattached after pod restarts
# Tests both graceful restarts and forced terminations

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="Pod Restart/Rescheduling"
PVC_NAME_NFS="restart-test-nfs"
PVC_NAME_NVMEOF="restart-test-nvmeof"
POD_NAME="restart-test-pod"
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

echo "================================================"
echo "TrueNAS CSI - Pod Restart/Rescheduling Test"
echo "================================================"
echo ""
echo "This test verifies:"
echo "  • Volumes reattach after graceful pod restart"
echo "  • Volumes reattach after forced termination"
echo "  • Data persists across pod restarts"
echo "  • Both NFS and NVMe-oF handle restarts correctly"
echo "================================================"

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME_NFS}"; cleanup_test "${POD_NAME}" "${PVC_NAME_NFS}"; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

# Run test steps
verify_cluster
deploy_driver "nvmeof"  # Deploy with both protocols
wait_for_driver

#######################################
# Test 1: Create NFS and NVMe-oF PVCs
#######################################
test_step 4 11 "Creating NFS and NVMe-oF PVCs"

# Create NFS PVC
cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME_NFS}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nfs
EOF

test_success "NFS PVC created: ${PVC_NAME_NFS}"

# Create NVMe-oF PVC
cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME_NVMEOF}
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nvmeof
EOF

test_success "NVMe-oF PVC created: ${PVC_NAME_NVMEOF}"

#######################################
# Test 2: Create pod with both volumes
#######################################
test_step 5 11 "Creating pod with both NFS and NVMe-oF volumes"

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
    volumeMounts:
    - name: nfs-volume
      mountPath: /nfs
    - name: nvmeof-volume
      mountPath: /nvmeof
  volumes:
  - name: nfs-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME_NFS}
  - name: nvmeof-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME_NVMEOF}
EOF

# Wait for PVCs to bind
kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME_NFS}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_PVC}"

kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME_NVMEOF}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_PVC}"

test_success "Both PVCs bound"

# Wait for pod
kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout=360s

NODE_NAME=$(kubectl get pod "${POD_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.nodeName}')
test_success "Pod running on node: ${NODE_NAME}"

#######################################
# Test 3: Write initial data
#######################################
test_step 6 11 "Writing initial test data"

echo ""
test_info "Writing data to NFS volume..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'NFS data v1' > /nfs/test.txt && \
           date > /nfs/timestamp.txt && \
           hostname > /nfs/hostname.txt"

test_success "Data written to NFS volume"

echo ""
test_info "Writing data to NVMe-oF volume..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'NVMe-oF data v1' > /nvmeof/test.txt && \
           date > /nvmeof/timestamp.txt && \
           hostname > /nvmeof/hostname.txt"

test_success "Data written to NVMe-oF volume"

# Store original data for verification
NFS_DATA_V1=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /nfs/test.txt)
NVMEOF_DATA_V1=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /nvmeof/test.txt)

#######################################
# Test 4: Graceful pod restart
#######################################
test_step 7 11 "Testing graceful pod restart"

echo ""
test_info "Deleting pod gracefully..."
kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --grace-period=30

test_info "Waiting for pod to terminate..."
kubectl wait --for=delete pod/"${POD_NAME}" -n "${TEST_NAMESPACE}" --timeout=60s || true

test_success "Pod terminated gracefully"

# Recreate pod
test_info "Recreating pod..."
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
    volumeMounts:
    - name: nfs-volume
      mountPath: /nfs
    - name: nvmeof-volume
      mountPath: /nvmeof
  volumes:
  - name: nfs-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME_NFS}
  - name: nvmeof-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME_NVMEOF}
EOF

kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout=360s

NEW_NODE_NAME=$(kubectl get pod "${POD_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.nodeName}')
test_success "Pod restarted on node: ${NEW_NODE_NAME}"

#######################################
# Test 5: Verify data after graceful restart
#######################################
test_step 8 11 "Verifying data after graceful restart"

echo ""
test_info "Checking NFS data..."
NFS_DATA_V2=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /nfs/test.txt)

if [[ "${NFS_DATA_V2}" == "${NFS_DATA_V1}" ]]; then
    test_success "NFS data intact after restart"
else
    test_error "NFS data mismatch: expected '${NFS_DATA_V1}', got '${NFS_DATA_V2}'"
    exit 1
fi

test_info "Checking NVMe-oF data..."
NVMEOF_DATA_V2=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /nvmeof/test.txt)

if [[ "${NVMEOF_DATA_V2}" == "${NVMEOF_DATA_V1}" ]]; then
    test_success "NVMe-oF data intact after restart"
else
    test_error "NVMe-oF data mismatch: expected '${NVMEOF_DATA_V1}', got '${NVMEOF_DATA_V2}'"
    exit 1
fi

#######################################
# Test 6: Write new data after restart
#######################################
test_step 9 11 "Writing new data after restart"

echo ""
test_info "Appending data to volumes..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'NFS data v2 - after restart' >> /nfs/test.txt"

kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'NVMe-oF data v2 - after restart' >> /nvmeof/test.txt"

test_success "New data written successfully"

#######################################
# Test 7: Forced termination test
#######################################
test_step 10 11 "Testing forced pod termination"

echo ""
test_info "Force deleting pod (grace-period=0)..."
kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --force --grace-period=0

test_info "Waiting for pod to be removed..."
sleep 10  # Give system time to process forced deletion

test_success "Pod force terminated"

# Recreate pod again
test_info "Recreating pod after forced termination..."
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
    volumeMounts:
    - name: nfs-volume
      mountPath: /nfs
    - name: nvmeof-volume
      mountPath: /nvmeof
  volumes:
  - name: nfs-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME_NFS}
  - name: nvmeof-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME_NVMEOF}
EOF

kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout=360s

test_success "Pod restarted after forced termination"

#######################################
# Test 8: Final data verification
#######################################
test_step 11 11 "Final data verification after forced restart"

echo ""
test_info "Verifying all data is intact..."

# Check NFS data includes both v1 and v2
NFS_FINAL=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /nfs/test.txt)
if echo "${NFS_FINAL}" | grep -q "NFS data v1" && echo "${NFS_FINAL}" | grep -q "after restart"; then
    test_success "NFS data complete (both v1 and v2 present)"
else
    test_error "NFS data incomplete: ${NFS_FINAL}"
    exit 1
fi

# Check NVMe-oF data includes both v1 and v2
NVMEOF_FINAL=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /nvmeof/test.txt)
if echo "${NVMEOF_FINAL}" | grep -q "NVMe-oF data v1" && echo "${NVMEOF_FINAL}" | grep -q "after restart"; then
    test_success "NVMe-oF data complete (both v1 and v2 present)"
else
    test_error "NVMe-oF data incomplete: ${NVMEOF_FINAL}"
    exit 1
fi

# Write final verification data
echo ""
test_info "Writing final verification data..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'Final write after forced restart' > /nfs/final.txt && \
           echo 'Final write after forced restart' > /nvmeof/final.txt"

test_success "Final data written successfully"

echo ""
echo "================================================"
echo "Pod Restart/Rescheduling Summary"
echo "================================================"
echo ""
echo "✓ Created volumes with initial data"
echo "✓ Performed graceful pod restart"
echo "✓ Verified data integrity after graceful restart"
echo "✓ Wrote additional data after restart"
echo "✓ Performed forced pod termination"
echo "✓ Verified data integrity after forced restart"
echo "✓ Both NFS and NVMe-oF volumes reattached correctly"
echo ""
echo "Node changes:"
echo "  Initial node: ${NODE_NAME}"
echo "  After graceful restart: ${NEW_NODE_NAME}"
echo ""
echo "================================================"

# Verify metrics
verify_metrics

# Cleanup
cleanup_test "${POD_NAME}" "${PVC_NAME_NFS}"
kubectl delete pvc "${PVC_NAME_NVMEOF}" -n "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=120s

# Success
test_summary "${PROTOCOL}" "PASSED"
