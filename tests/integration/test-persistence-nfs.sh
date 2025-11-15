#!/bin/bash
# Data Persistence Test - NFS
# Verifies that data written to volumes survives pod restarts and failures
# This is critical for ensuring data durability

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NFS Data Persistence"
# Use unique PVC name with timestamp to prevent reuse of stale datasets if cleanup fails
TIMESTAMP=$(date +%s)
PVC_NAME="persistence-test-pvc-${TIMESTAMP}"
POD_NAME="persistence-test-pod-${TIMESTAMP}"
POD_NAME_2="persistence-test-pod-2-${TIMESTAMP}"
TEST_DATA="Persistence Test Data - ${TIMESTAMP}"
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
test_step "Creating PVC: ${PVC_NAME}"

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
test_step "Creating initial pod and writing test data"

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
# Configure test with 8 total steps
set_test_steps 8
test_info "Writing test data to volume..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo '${TEST_DATA}' > /data/test.txt"
test_success "Wrote test file: test.txt"

# Write a large file to test data integrity
echo ""
# Configure test with 8 total steps
test_info "Writing large file (${LARGE_FILE_SIZE_MB}MB) for integrity test..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    dd if=/dev/urandom of=/data/large-file.bin bs=1M count=${LARGE_FILE_SIZE_MB} 2>&1 | tail -3
test_success "Wrote large file: large-file.bin"

# Calculate checksum of the large file
echo ""
# Configure test with 8 total steps
test_info "Calculating checksum of large file..."
if ! ORIGINAL_CHECKSUM=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- md5sum /data/large-file.bin 2>&1 | awk '{print $1}'); then
    test_error "Failed to calculate checksum of large file!"
    test_error "Error: ${ORIGINAL_CHECKSUM}"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi
test_info "Original checksum: ${ORIGINAL_CHECKSUM}"

# Create a directory structure
echo ""
# Configure test with 8 total steps
test_info "Creating directory structure..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "mkdir -p /data/subdir1/subdir2 && echo 'nested data' > /data/subdir1/subdir2/nested.txt"
test_success "Created nested directories and files"

# List all files
echo ""
# Configure test with 8 total steps
test_info "Current file structure:"
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    find /data -type f -exec ls -lh {} \;

#######################################
# Test 1: Graceful pod restart
#######################################
test_step "Test 1: Graceful pod restart (delete and recreate)"

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
# Configure test with 8 total steps
test_info "Verifying test data persisted after graceful restart..."
test_info "Expected data: '${TEST_DATA}'"

if ! pod_file_exists "${POD_NAME}" "${TEST_NAMESPACE}" "/data/test.txt"; then
    test_error "CRITICAL: test.txt does not exist after restart!"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi

if ! RETRIEVED_DATA=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt 2>&1); then
    test_error "Failed to read test.txt after graceful restart!"
    test_error "Error: ${RETRIEVED_DATA}"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi

test_info "Retrieved data: '${RETRIEVED_DATA}'"
if [[ "${RETRIEVED_DATA}" == "${TEST_DATA}" ]]; then
    test_success "Test data intact: ${RETRIEVED_DATA}"
else
    test_error "Data mismatch! Expected: '${TEST_DATA}', Got: '${RETRIEVED_DATA}'"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi

# Verify large file checksum
echo ""
# Configure test with 8 total steps
test_info "Verifying large file integrity..."
if ! pod_file_exists "${POD_NAME}" "${TEST_NAMESPACE}" "/data/large-file.bin"; then
    test_error "CRITICAL: large-file.bin does not exist after restart!"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi

if ! NEW_CHECKSUM=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- sh -c "md5sum /data/large-file.bin | awk '{print \$1}'" 2>&1); then
    test_error "Failed to calculate checksum after restart!"
    test_error "Error: ${NEW_CHECKSUM}"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi

test_info "Original checksum: ${ORIGINAL_CHECKSUM}"
test_info "Current checksum:  ${NEW_CHECKSUM}"

if [[ "${NEW_CHECKSUM}" == "${ORIGINAL_CHECKSUM}" ]]; then
    test_success "Large file integrity verified (checksum matches)"
else
    test_error "Large file corrupted! Original: ${ORIGINAL_CHECKSUM}, New: ${NEW_CHECKSUM}"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi

# Verify nested file
echo ""
# Configure test with 8 total steps
test_info "Verifying nested directory structure..."
if ! pod_file_exists "${POD_NAME}" "${TEST_NAMESPACE}" "/data/subdir1/subdir2/nested.txt"; then
    test_error "CRITICAL: nested.txt does not exist after restart!"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi

if ! NESTED_DATA=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/subdir1/subdir2/nested.txt 2>&1); then
    test_error "Failed to read nested.txt after restart!"
    test_error "Error: ${NESTED_DATA}"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi

test_info "Nested data: '${NESTED_DATA}'"

if [[ "${NESTED_DATA}" == "nested data" ]]; then
    test_success "Nested directory structure intact"
else
    test_error "Nested data mismatch! Expected: 'nested data', Got: '${NESTED_DATA}'"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi

#######################################
# Test 2: Force delete (simulated crash)
#######################################
test_step "Test 2: Force delete (simulated pod crash)"

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
# Configure test with 8 total steps
test_info "Verifying all data persisted after force delete..."
test_info "Expected data: '${TEST_DATA}'"

if ! pod_file_exists "${POD_NAME_2}" "${TEST_NAMESPACE}" "/data/test.txt"; then
    test_error "CRITICAL: test.txt does not exist after force delete!"
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi

if ! RETRIEVED_DATA=$(kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt 2>&1); then
    test_error "Failed to read test.txt after force delete!"
    test_error "Error: ${RETRIEVED_DATA}"
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi

test_info "Retrieved data: '${RETRIEVED_DATA}'"
if [[ "${RETRIEVED_DATA}" == "${TEST_DATA}" ]]; then
    test_success "Test data intact after force delete"
else
    test_error "Data lost after force delete! Expected: '${TEST_DATA}', Got: '${RETRIEVED_DATA}'"
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi

if ! pod_file_exists "${POD_NAME_2}" "${TEST_NAMESPACE}" "/data/large-file.bin"; then
    test_error "CRITICAL: large-file.bin does not exist after force delete!"
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi

if ! NEW_CHECKSUM=$(kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- sh -c "md5sum /data/large-file.bin | awk '{print \$1}'" 2>&1); then
    test_error "Failed to calculate checksum after force delete!"
    test_error "Error: ${NEW_CHECKSUM}"
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi

test_info "Original checksum: ${ORIGINAL_CHECKSUM}"
test_info "Current checksum:  ${NEW_CHECKSUM}"

if [[ "${NEW_CHECKSUM}" == "${ORIGINAL_CHECKSUM}" ]]; then
    test_success "Large file integrity maintained after force delete"
else
    test_error "Large file corrupted after force delete!"
    test_error "  Original: ${ORIGINAL_CHECKSUM}"
    test_error "  Current:  ${NEW_CHECKSUM}"
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi

if ! pod_file_exists "${POD_NAME_2}" "${TEST_NAMESPACE}" "/data/subdir1/subdir2/nested.txt"; then
    test_error "CRITICAL: nested.txt does not exist after force delete!"
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi

if ! NESTED_DATA=$(kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- cat /data/subdir1/subdir2/nested.txt 2>&1); then
    test_error "Failed to read nested.txt after force delete!"
    test_error "Error: ${NESTED_DATA}"
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi

test_info "Nested data: '${NESTED_DATA}'"

if [[ "${NESTED_DATA}" == "nested data" ]]; then
    test_success "All data structures intact after force delete"
else
    test_error "Nested data lost after force delete! Expected: 'nested data', Got: '${NESTED_DATA}'"
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi

#######################################
# Test 3: Write new data and verify persistence
#######################################
echo ""
# Configure test with 8 total steps
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
# Configure test with 8 total steps
test_info "Verifying data from second pod persisted..."
if ! pod_file_exists "${POD_NAME}" "${TEST_NAMESPACE}" "/data/second-pod.txt"; then
    test_error "CRITICAL: second-pod.txt does not exist!"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi

if ! SECOND_POD_DATA=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/second-pod.txt 2>&1); then
    test_error "Failed to read second-pod.txt!"
    test_error "Error: ${SECOND_POD_DATA}"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi

test_info "Retrieved data: '${SECOND_POD_DATA}'"

if [[ "${SECOND_POD_DATA}" == "Data from second pod" ]]; then
    test_success "Data from second pod persisted correctly"
else
    test_error "Data from second pod was lost! Expected: 'Data from second pod', Got: '${SECOND_POD_DATA}'"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi

# Final file listing
echo ""
# Configure test with 8 total steps
test_info "Final file structure:"
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    find /data -type f -exec ls -lh {} \;

# Verify metrics
test_step "Verifying metrics collection"
verify_metrics

# Cleanup
cleanup_test "${POD_NAME}" "${PVC_NAME}"

# Success
test_summary "${PROTOCOL}" "PASSED"
