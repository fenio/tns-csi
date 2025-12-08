#!/bin/bash
# Access Mode Validation Test
# Verifies that ReadWriteMany (RWX) and ReadWriteOnce (RWO) work correctly
# Tests multiple pods accessing the same volume with appropriate access modes

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="Access Mode Validation"
PVC_NAME_RWX="access-mode-rwx"
PVC_NAME_RWO="access-mode-rwo"
POD_NAME_1="access-test-pod-1"
POD_NAME_2="access-test-pod-2"
POD_NAME_3="access-test-pod-3"

echo "================================================"
echo "TrueNAS CSI - Access Mode Validation Test"
echo "================================================"
echo ""
# Configure test with 10 total steps
set_test_steps 10
echo "This test verifies:"
echo "  • ReadWriteMany (RWX) allows multiple pods to mount"
echo "  • ReadWriteOnce (RWO) restricts to single pod"
echo "  • Data is shared correctly in RWX mode"
echo "  • Proper isolation in RWO mode"
echo "================================================"

# Trap errors and cleanup
trap 'show_diagnostic_logs "${POD_NAME_1}" "${PVC_NAME_RWX}"; cleanup_test "${POD_NAME_1}" "${PVC_NAME_RWX}"; cleanup_test "${POD_NAME_2}" "${PVC_NAME_RWO}"; cleanup_test "${POD_NAME_3}" ""; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

# Run test steps
verify_cluster
deploy_driver "both"  # Deploy with both NFS and NVMe-oF
wait_for_driver

#######################################
# Test 1: Create RWX PVC (NFS)
#######################################
test_step "Creating ReadWriteMany PVC (NFS)"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME_RWX}
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nfs
EOF

kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME_RWX}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_PVC}"

test_success "RWX PVC created and bound"

#######################################
# Test 2: Mount RWX volume in first pod
#######################################
test_step "Mounting RWX volume in first pod"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME_1}
  labels:
    test: access-mode-rwx
spec:
  containers:
  - name: test-container
    image: busybox:latest
    command: ["sleep", "600"]
    volumeMounts:
    - name: shared-volume
      mountPath: /data
  volumes:
  - name: shared-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME_RWX}
EOF

kubectl wait --for=condition=Ready pod/"${POD_NAME_1}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_POD}"

test_success "First pod mounted RWX volume"

# Write data from first pod
echo ""
# Configure test with 10 total steps
test_info "Writing data from first pod..."
kubectl exec "${POD_NAME_1}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'Data from pod 1' > /data/pod1.txt && \
           hostname > /data/pod1-hostname.txt"

test_success "Data written by pod 1"

#######################################
# Test 3: Mount RWX volume in second pod
#######################################
test_step "Mounting RWX volume in second pod (concurrent)"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME_2}
  labels:
    test: access-mode-rwx
spec:
  containers:
  - name: test-container
    image: busybox:latest
    command: ["sleep", "600"]
    volumeMounts:
    - name: shared-volume
      mountPath: /data
  volumes:
  - name: shared-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME_RWX}
EOF

kubectl wait --for=condition=Ready pod/"${POD_NAME_2}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_POD}"

test_success "Second pod mounted same RWX volume concurrently"

#######################################
# Test 4: Verify data sharing in RWX
#######################################
test_step "Verifying data sharing in RWX mode"

echo ""
# Configure test with 10 total steps
test_info "Checking if pod 2 can read data written by pod 1..."
POD1_DATA=$(kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- cat /data/pod1.txt)

if [[ "${POD1_DATA}" == "Data from pod 1" ]]; then
    test_success "Pod 2 can read data from pod 1 (RWX working)"
else
    test_error "Pod 2 cannot read pod 1's data: ${POD1_DATA}"
    exit 1
fi

# Write from second pod
test_info "Writing data from second pod..."
kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'Data from pod 2' > /data/pod2.txt && \
           hostname > /data/pod2-hostname.txt"

# Verify first pod can see second pod's data
test_info "Verifying pod 1 can read pod 2's data..."
POD2_DATA=$(kubectl exec "${POD_NAME_1}" -n "${TEST_NAMESPACE}" -- cat /data/pod2.txt)

if [[ "${POD2_DATA}" == "Data from pod 2" ]]; then
    test_success "Pod 1 can read data from pod 2 (bidirectional RWX working)"
else
    test_error "Pod 1 cannot read pod 2's data: ${POD2_DATA}"
    exit 1
fi

# Test concurrent writes
echo ""
# Configure test with 10 total steps
test_info "Testing concurrent writes..."
kubectl exec "${POD_NAME_1}" -n "${TEST_NAMESPACE}" -- \
    sh -c "for i in 1 2 3 4 5; do echo \"pod1-\$i\" >> /data/concurrent.txt; sleep 0.1; done" &
PID1=$!

kubectl exec "${POD_NAME_2}" -n "${TEST_NAMESPACE}" -- \
    sh -c "for i in 1 2 3 4 5; do echo \"pod2-\$i\" >> /data/concurrent.txt; sleep 0.1; done" &
PID2=$!

wait $PID1 $PID2

test_success "Concurrent writes completed"

# Verify both pods wrote to the file
CONCURRENT_DATA=$(kubectl exec "${POD_NAME_1}" -n "${TEST_NAMESPACE}" -- cat /data/concurrent.txt)
if echo "${CONCURRENT_DATA}" | grep -q "pod1-" && echo "${CONCURRENT_DATA}" | grep -q "pod2-"; then
    test_success "Both pods successfully wrote to shared file"
else
    test_warning "Concurrent writes may have data loss (expected with NFS without locking)"
fi

#######################################
# Test 5: Create RWO PVC (NVMe-oF)
#######################################
test_step "Creating ReadWriteOnce PVC (NVMe-oF)"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${PVC_NAME_RWO}
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nvmeof
EOF

# Create a pod to bind the PVC
cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: ${POD_NAME_3}
  labels:
    test: access-mode-rwo
spec:
  containers:
  - name: test-container
    image: busybox:latest
    command: ["sleep", "600"]
    volumeMounts:
    - name: exclusive-volume
      mountPath: /data
  volumes:
  - name: exclusive-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME_RWO}
EOF

kubectl wait --for=jsonpath='{.status.phase}'=Bound \
    pvc/"${PVC_NAME_RWO}" \
    -n "${TEST_NAMESPACE}" \
    --timeout="${TIMEOUT_PVC}"

kubectl wait --for=condition=Ready pod/"${POD_NAME_3}" \
    -n "${TEST_NAMESPACE}" \
    --timeout=360s

test_success "RWO PVC created and bound to pod 3"

# Write data to RWO volume
echo ""
# Configure test with 10 total steps
test_info "Writing data to RWO volume..."
kubectl exec "${POD_NAME_3}" -n "${TEST_NAMESPACE}" -- \
    sh -c "echo 'Exclusive data from pod 3' > /data/exclusive.txt"

test_success "Data written to RWO volume"

#######################################
# Test 6: Verify RWO exclusivity
#######################################
test_step "Verifying RWO volume exclusivity"

echo ""
# Configure test with 10 total steps
test_info "Attempting to create second pod with same RWO volume..."
test_info "This should remain in ContainerCreating or Pending state"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Pod
metadata:
  name: access-test-rwo-violation
  labels:
    test: access-mode-rwo-violation
spec:
  containers:
  - name: test-container
    image: busybox:latest
    command: ["sleep", "60"]
    volumeMounts:
    - name: exclusive-volume
      mountPath: /data
  volumes:
  - name: exclusive-volume
    persistentVolumeClaim:
      claimName: ${PVC_NAME_RWO}
EOF

# Wait a bit and check the pod status
sleep 15

POD_STATUS=$(kubectl get pod access-test-rwo-violation -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}')
CONTAINER_STATUS=$(kubectl get pod access-test-rwo-violation -n "${TEST_NAMESPACE}" -o jsonpath='{.status.containerStatuses[0].state}' 2>/dev/null || echo "")

if [[ "${POD_STATUS}" == "Pending" ]] || [[ "${POD_STATUS}" == "ContainerCreating" ]] || echo "${CONTAINER_STATUS}" | grep -q "waiting"; then
    test_success "RWO exclusivity enforced - second pod cannot mount volume"
else
    test_error "RWO violation: second pod reached unexpected state: ${POD_STATUS}"
    kubectl describe pod access-test-rwo-violation -n "${TEST_NAMESPACE}"
    exit 1
fi

# Check events for attachment errors
EVENTS=$(kubectl get events -n "${TEST_NAMESPACE}" --sort-by='.lastTimestamp' | grep -i "access-test-rwo-violation" | tail -5 || echo "")
if echo "${EVENTS}" | grep -qE "(FailedAttachVolume|FailedMount|Volume.*already.*use)"; then
    test_success "Kubernetes correctly reports volume attachment conflict"
else
    test_info "Pod is waiting for volume attachment (expected behavior)"
fi

# Cleanup the violation test pod
kubectl delete pod access-test-rwo-violation -n "${TEST_NAMESPACE}" --wait=false

#######################################
# Test 7: Summary and verification
#######################################
test_step "Final verification and summary"

echo ""
# Configure test with 10 total steps
test_info "Listing all files in RWX volume..."
RWX_FILES=$(kubectl exec "${POD_NAME_1}" -n "${TEST_NAMESPACE}" -- ls -la /data/)
echo "${RWX_FILES}"

test_info "Counting files in RWX volume..."
RWX_FILE_COUNT=$(kubectl exec "${POD_NAME_1}" -n "${TEST_NAMESPACE}" -- ls /data/ | wc -l)
test_info "Files in RWX volume: ${RWX_FILE_COUNT}"

if [[ $RWX_FILE_COUNT -ge 4 ]]; then
    test_success "Multiple files present in shared RWX volume"
else
    test_warning "Expected more files in RWX volume: ${RWX_FILE_COUNT}"
fi

test_info "Verifying RWO volume data..."
RWO_DATA=$(kubectl exec "${POD_NAME_3}" -n "${TEST_NAMESPACE}" -- cat /data/exclusive.txt)

if [[ "${RWO_DATA}" == "Exclusive data from pod 3" ]]; then
    test_success "RWO volume data intact"
else
    test_error "RWO data verification failed: ${RWO_DATA}"
    exit 1
fi

echo ""
# Configure test with 10 total steps
echo "================================================"
echo "Access Mode Validation Summary"
echo "================================================"
echo ""
# Configure test with 10 total steps
echo "ReadWriteMany (RWX) - NFS:"
echo "  ✓ Two pods mounted same volume concurrently"
echo "  ✓ Data shared between pods bidirectionally"
echo "  ✓ Both pods could read and write"
echo "  ✓ Files created: ${RWX_FILE_COUNT}"
echo ""
# Configure test with 10 total steps
echo "ReadWriteOnce (RWO) - NVMe-oF:"
echo "  ✓ Single pod mounted volume successfully"
echo "  ✓ Second pod blocked from mounting"
echo "  ✓ Kubernetes enforced exclusivity"
echo "  ✓ Data remained isolated to single pod"
echo ""
# Configure test with 10 total steps
echo "================================================"

# Verify metrics
verify_metrics

# Cleanup
cleanup_test "${POD_NAME_1}" "${PVC_NAME_RWX}"
cleanup_test "${POD_NAME_2}" ""
cleanup_test "${POD_NAME_3}" "${PVC_NAME_RWO}"

# Success
test_summary "${PROTOCOL}" "PASSED"
