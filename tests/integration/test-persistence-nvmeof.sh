#!/bin/bash
# Data Persistence Test - NVMe-oF
# Verifies that data written to block volumes survives pod restarts and failures
# This is critical for ensuring data durability in block storage

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NVMe-oF Data Persistence"
# Use unique PVC name with timestamp to prevent reuse of stale ZVOLs if cleanup fails
TIMESTAMP=$(date +%s)
PVC_NAME="persistence-test-pvc-nvmeof-${TIMESTAMP}"
POD_NAME="persistence-test-pod-nvmeof-${TIMESTAMP}"
POD_NAME_2="persistence-test-pod-nvmeof-2-${TIMESTAMP}"
TEST_DATA="Persistence Test Data - ${TIMESTAMP}"
LARGE_FILE_SIZE_MB=50
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

echo "=========================================="
echo "TrueNAS CSI - NVMe-oF Data Persistence Test"
echo "=========================================="
echo ""
echo "Test Configuration:"
echo "  Timestamp: ${TIMESTAMP}"
echo "  PVC Name: ${PVC_NAME}"
echo "  Pod Name: ${POD_NAME}"
echo "  Test Data: '${TEST_DATA}'"
echo ""

# Configure test with 7 total steps
set_test_steps 7

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"; cleanup_test "${POD_NAME}" "${PVC_NAME}"; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

# Run test steps
verify_cluster
deploy_driver "nvmeof"
wait_for_driver

# Check if NVMe-oF is configured on TrueNAS
MANIFEST_DIR="${SCRIPT_DIR}/manifests"
if ! check_nvmeof_configured "${MANIFEST_DIR}/pvc-nvmeof.yaml" "test-pvc-nvmeof" "${PROTOCOL}"; then
    exit 0  # Gracefully skip test if not configured
fi

#######################################
# Create PVC (WaitForFirstConsumer)
#######################################
test_step "Creating PVC: ${PVC_NAME}"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME}
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 2Gi
  storageClassName: tns-csi-nvmeof
EOF

test_success "PVC created (will bind when pod is created)"

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

# Wait for PVC to bind (triggered by pod creation)
test_info "Waiting for PVC to bind (WaitForFirstConsumer)..."
kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_PVC}"

test_success "PVC is bound"

PV_NAME=$(kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
test_info "Created PV: ${PV_NAME}"

# Get detailed PV information
echo ""
test_info "=== PV Details ==="
kubectl get pv "${PV_NAME}" -o yaml | grep -E "volumeHandle:|csi:|volumeAttributes:" -A 10
echo ""

# Wait for pod to be ready
test_info "Waiting for pod ${POD_NAME} to be ready (using PVC: ${PVC_NAME}, PV: ${PV_NAME})..."
kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_POD}"

test_success "Pod is ready"

# Check initial volume state
echo ""
test_info "=== Initial Volume State ==="
test_info "Checking what exists on the volume before writing..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- ls -la /data/ || echo "(Volume is empty or not yet mounted)"
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- df -h /data || true
echo ""

# Write test data
# NOTE: CSI driver handles formatting during NodeStageVolume - no need to format here
echo ""
test_info "Writing test data to volume..."
test_info "Test data content: '${TEST_DATA}'"
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo '${TEST_DATA}' > /data/test.txt"
test_success "Wrote test file: test.txt"

# Verify what was actually written
if ! WRITTEN_DATA=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/test.txt 2>&1); then
    test_error "CRITICAL: Failed to read test.txt immediately after write!"
    test_error "Error: ${WRITTEN_DATA}"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi
test_info "Verified written data: '${WRITTEN_DATA}'"

if [[ "${WRITTEN_DATA}" != "${TEST_DATA}" ]]; then
    test_error "CRITICAL: Data mismatch immediately after write! Expected: '${TEST_DATA}', Got: '${WRITTEN_DATA}'"
    show_diagnostic_logs "${POD_NAME}" "${PVC_NAME}"
    exit 1
fi

# Write a large file to test data integrity
echo ""
test_info "Writing large file (${LARGE_FILE_SIZE_MB}MB) for integrity test..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    dd if=/dev/urandom of=/data/large-file.bin bs=1M count=${LARGE_FILE_SIZE_MB} 2>&1 | tail -3
test_success "Wrote large file: large-file.bin"

# Calculate checksum of the large file
echo ""
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
test_info "Creating directory structure..."
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    sh -c "mkdir -p /data/subdir1/subdir2 && echo 'nested data' > /data/subdir1/subdir2/nested.txt"
test_success "Created nested directories and files"

# Sync filesystem to ensure all data is written
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- sync

# List all files
echo ""
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
test_info "Recreating pod with same PVC: ${PVC_NAME} (backed by PV: ${PV_NAME})..."
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

test_info "Waiting for pod to reconnect to volume..."
kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_POD}"

test_success "Pod recreated and ready"

# Show what files exist on the volume after restart
echo ""
test_info "Files on volume after restart:"
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- ls -la /data/

# Verify data is intact
echo ""
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

# Show immediate volume state after pod creation
echo ""
test_info "=== Immediate Volume State After Force Delete Recovery ==="
test_info "Checking what the new pod sees on the volume..."
kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- df -h /data || echo "Cannot get filesystem info"
kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- ls -la /data/ || echo "Cannot list /data"
echo ""

# Verify all data is still intact after force delete
echo ""
test_info "=== Post-Force-Delete Volume State ==="
test_info "Checking volume contents after force delete and pod recreation..."

# Verify we're still using the same PV
CURRENT_PV=$(kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
test_info "Current PV name: ${CURRENT_PV}"
if [[ "${CURRENT_PV}" != "${PV_NAME}" ]]; then
    test_error "CRITICAL: PV changed! Original: ${PV_NAME}, Current: ${CURRENT_PV}"
    test_error "This indicates the PVC was rebound to a different volume!"
    exit 1
fi
test_success "PVC is still bound to the same PV: ${PV_NAME}"
echo ""

# First, check what files exist on the volume
test_info "Files on volume after force delete:"
kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- ls -la /data/ || {
    test_error "CRITICAL: Cannot list /data directory!"
    test_info "Checking if volume is mounted:"
    kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- df -h /data || echo "Volume not mounted!"
    test_info "Checking mount points:"
    kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- mount | grep /data || echo "No /data mount found!"
    exit 1
}
echo ""

# Check filesystem type and UUID
test_info "Filesystem information after force delete:"
kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- df -h /data || echo "Cannot get filesystem info"
echo ""

# Try to read the test file with detailed error handling
test_info "Attempting to read test.txt..."
test_info "Expected data: '${TEST_DATA}'"
if ! pod_file_exists "${POD_NAME_2}" "${TEST_NAMESPACE}" "/data/test.txt"; then
    test_error "CRITICAL: test.txt file does not exist after force delete!"
    test_error "This indicates the volume was reformatted or wrong volume was attached!"
    test_info "Complete directory listing:"
    kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- find /data -ls || true
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi

test_success "test.txt file exists"

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
    test_error "Data mismatch after force delete!"
    test_error "  Expected: '${TEST_DATA}'"
    test_error "  Got:      '${RETRIEVED_DATA}'"
    test_info "File details:"
    kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- ls -lh /data/test.txt
    kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- stat /data/test.txt || true
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi
echo ""

# Verify large file integrity
test_info "Verifying large file integrity..."
if ! pod_file_exists "${POD_NAME_2}" "${TEST_NAMESPACE}" "/data/large-file.bin"; then
    test_error "CRITICAL: large-file.bin does not exist after force delete!"
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi

test_success "large-file.bin exists"

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
echo ""

# Verify nested directory structure
test_info "Verifying nested directory structure..."
if ! pod_file_exists "${POD_NAME_2}" "${TEST_NAMESPACE}" "/data/subdir1/subdir2/nested.txt"; then
    test_error "CRITICAL: nested.txt does not exist after force delete!"
    test_info "Checking if subdir1/subdir2 exists:"
    kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- ls -la /data/subdir1/subdir2/ || echo "Directory doesn't exist"
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi

test_success "nested.txt exists in correct location"

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
    test_error "Nested data mismatch!"
    test_error "  Expected: 'nested data'"
    test_error "  Got:      '${NESTED_DATA}'"
    show_diagnostic_logs "${POD_NAME_2}" "${PVC_NAME}"
    exit 1
fi

#######################################
# Test 3: Write new data and verify persistence
#######################################
echo ""
test_info "Writing additional data from new pod..."
kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'Data from second pod' > /data/second-pod.txt"

# Sync to ensure data is written
kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- sync

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
test_info "Final file structure:"
kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
    find /data -type f -exec ls -lh {} \;

# Verify metrics
verify_metrics

# Cleanup
cleanup_test "${POD_NAME}" "${PVC_NAME}"

# Success
test_summary "${PROTOCOL}" "PASSED"
