#!/bin/bash
# Data Persistence Test - NFS
# Verifies that data written to volumes survives pod restarts and failures
# This is critical for ensuring data durability

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NFS Data Persistence"
PVC_NAME="persistence-test-pvc"
POD_NAME="persistence-test-pod"
POD_NAME_2="persistence-test-pod-2"
TEST_DATA="Persistence Test Data - $(date +%s)"
LARGE_FILE_SIZE_MB=50
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

echo "========================================"
echo "TrueNAS CSI - NFS Data Persistence Test"
echo "========================================"

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_test "${POD_NAME}" "${PVC_NAME}"; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

# Run test steps
verify_cluster
deploy_driver "nfs"
wait_for_driver

#######################################
# Create PVC
#######################################
test_step 4 9 "Creating PVC: ${PVC_NAME}"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 2Gi
  storageClassName: tns-csi-nfs
EOF

kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_PVC}"

test_success "PVC is bound"

PV_NAME=$(kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
test_info "Created PV: ${PV_NAME}"

#######################################
# Create initial pod and write data
#######################################
test_step 5 9 "Creating initial pod and writing test data"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
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
      claimName: ${PVC_NAME}
EOF

kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_POD}"

test_success "Pod is ready"

# Write test data
echo ""
test_info "Writing test data to volume..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo '${TEST_DATA}' > /data/test.txt"
test_success "Wrote test file: test.txt"

# Write a large file to test data integrity
echo ""
test_info "Writing large file (${LARGE_FILE_SIZE_MB}MB) for integrity test..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    dd if=/dev/urandom of=/data/large-file.bin bs=1M count=${LARGE_FILE_SIZE_MB} 2>&1 | tail -3
test_success "Wrote large file: large-file.bin"

# Calculate checksum of the large file
echo ""
test_info "Calculating checksum of large file..."
ORIGINAL_CHECKSUM=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    md5sum /data/large-file.bin | awk '{print $1}')
test_info "Original checksum: ${ORIGINAL_CHECKSUM}"

# Create a directory structure
echo ""
test_info "Creating directory structure..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "mkdir -p /data/subdir1/subdir2 && echo 'nested data' > /data/subdir1/subdir2/nested.txt"
test_success "Created nested directories and files"

# List all files
echo ""
test_info "Current file structure:"
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    find /data -type f -exec ls -lh {} \;

#######################################
# Test 1: Graceful pod restart
#######################################
test_step 6 9 "Test 1: Graceful pod restart (delete and recreate)"

test_info "Deleting pod gracefully..."
kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --timeout=60s

test_info "Waiting for pod to be fully deleted..."
kubectl wait --for=delete pod/"${POD_NAME}" -n "${TEST_NAMESPACE}" --timeout=60s || true
sleep 5

test_success "Pod deleted"

# Recreate the same pod
test_info "Recreating pod with same PVC..."
cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
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
      claimName: ${PVC_NAME}
EOF

kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_POD}"

test_success "Pod recreated and ready"

# Verify data is intact
echo ""
test_info "Verifying test data persisted after graceful restart..."
RETRIEVED_DATA=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt)
if [[ "${RETRIEVED_DATA}" == "${TEST_DATA}" ]]; then
    test_success "Test data intact: ${RETRIEVED_DATA}"
else
    test_error "Data mismatch! Expected: '${TEST_DATA}', Got: '${RETRIEVED_DATA}'"
    exit 1
fi

# Verify large file checksum
echo ""
test_info "Verifying large file integrity..."
NEW_CHECKSUM=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    md5sum /data/large-file.bin | awk '{print $1}')
if [[ "${NEW_CHECKSUM}" == "${ORIGINAL_CHECKSUM}" ]]; then
    test_success "Large file integrity verified (checksum matches)"
else
    test_error "Large file corrupted! Original: ${ORIGINAL_CHECKSUM}, New: ${NEW_CHECKSUM}"
    exit 1
fi

# Verify nested file
echo ""
test_info "Verifying nested directory structure..."
NESTED_DATA=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/subdir1/subdir2/nested.txt)
if [[ "${NESTED_DATA}" == "nested data" ]]; then
    test_success "Nested directory structure intact"
else
    test_error "Nested data mismatch!"
    exit 1
fi

#######################################
# Test 2: Force delete (simulated crash)
#######################################
test_step 7 9 "Test 2: Force delete (simulated pod crash)"

test_info "Force deleting pod (simulating crash)..."
kubectl delete pod "${POD_NAME}" -n "${TEST_NAMESPACE}" --force --grace-period=0

test_info "Waiting for pod to be deleted..."
sleep 10

test_success "Pod force deleted"

# Create a different pod name to simulate new workload
test_info "Creating new pod with different name..."
cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME_2}
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
      claimName: ${PVC_NAME}
EOF

kubectl wait --for=condition=Ready pod/"${POD_NAME_2}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_POD}"

test_success "New pod ready"

# Verify all data is still intact after force delete
echo ""
test_info "Verifying all data persisted after force delete..."

RETRIEVED_DATA=$(kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt)
if [[ "${RETRIEVED_DATA}" == "${TEST_DATA}" ]]; then
    test_success "Test data intact after force delete"
else
    test_error "Data lost after force delete!"
    exit 1
fi

NEW_CHECKSUM=$(kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- \
    md5sum /data/large-file.bin | awk '{print $1}')
if [[ "${NEW_CHECKSUM}" == "${ORIGINAL_CHECKSUM}" ]]; then
    test_success "Large file integrity maintained after force delete"
else
    test_error "Large file corrupted after force delete!"
    exit 1
fi

NESTED_DATA=$(kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- cat /data/subdir1/subdir2/nested.txt)
if [[ "${NESTED_DATA}" == "nested data" ]]; then
    test_success "All data structures intact after force delete"
else
    test_error "Nested data lost after force delete!"
    exit 1
fi

#######################################
# Test 3: Write new data and verify persistence
#######################################
echo ""
test_info "Writing additional data from new pod..."
kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'Data from second pod' > /data/second-pod.txt"

# Delete second pod and create third to verify new data
kubectl delete pod "${POD_NAME_2}" -n "${TEST_NAMESPACE}" --timeout=60s

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME}
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
      claimName: ${PVC_NAME}
EOF

kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_POD}"

echo ""
test_info "Verifying data from second pod persisted..."
SECOND_POD_DATA=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/second-pod.txt)
if [[ "${SECOND_POD_DATA}" == "Data from second pod" ]]; then
    test_success "Data from second pod persisted correctly"
else
    test_error "Data from second pod was lost!"
    exit 1
fi

# Final file listing
echo ""
test_info "Final file structure:"
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    find /data -type f -exec ls -lh {} \;

# Verify metrics
test_step 8 9 "Verifying metrics collection"
verify_metrics

# Cleanup
cleanup_test "${POD_NAME}" "${PVC_NAME}"

# Success
test_summary "${PROTOCOL}" "PASSED"
