#!/bin/bash
# StatefulSet Test - NVMe-oF
# Verifies that StatefulSets work correctly with NVMe-oF block volumes
# Tests: ordering, scaling, rolling updates, volume persistence per replica

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib/common.sh"

PROTOCOL="NVMe-oF StatefulSet"
STS_NAME="web-nvmeof"
SERVICE_NAME="web-nvmeof-service"
REPLICAS=3
MANIFEST_DIR="${SCRIPT_DIR}/manifests"

echo "=========================================="
echo "TrueNAS CSI - NVMe-oF StatefulSet Test"
echo "=========================================="

# Trap errors and cleanup
trap 'show_diagnostic_logs "" ""; kubectl delete namespace "${TEST_NAMESPACE}" --ignore-not-found=true --timeout=120s || true; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

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
# Create headless service
#######################################
test_step "Creating headless service: ${SERVICE_NAME}"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: v1
kind: Service
metadata:
  name: ${SERVICE_NAME}
  labels:
    app: web-nvmeof
spec:
  ports:
  - port: 80
    name: web
  clusterIP: None
  selector:
    app: web-nvmeof
EOF

test_success "Headless service created"

#######################################
# Create StatefulSet with volumeClaimTemplates
#######################################
test_step "Creating StatefulSet: ${STS_NAME} (${REPLICAS} replicas)"

cat <<EOF | kubectl apply -n "${TEST_NAMESPACE}" -f -
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: ${STS_NAME}
spec:
  serviceName: ${SERVICE_NAME}
  replicas: ${REPLICAS}
  selector:
    matchLabels:
      app: web-nvmeof
  template:
    metadata:
      labels:
        app: web-nvmeof
    spec:
      containers:
      - name: web
        image: busybox:latest
        command:
          - sh
          - -c
          - |
            # Write pod identity to volume
            echo "Pod: \${HOSTNAME}" > /data/pod-identity.txt
            echo "Started at: \$(date)" >> /data/pod-identity.txt
            # Sync to ensure data is written
            sync
            # Keep running
            while true; do sleep 30; done
        volumeMounts:
        - name: data
          mountPath: /data
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: [ "ReadWriteOnce" ]
      storageClassName: tns-csi-nvmeof
      resources:
        requests:
          storage: 1Gi
EOF

test_success "StatefulSet created"

#######################################
# Wait for all pods to be ready
#######################################
test_step "Waiting for all ${REPLICAS} pods to be ready"

# StatefulSets create pods in order (0, 1, 2...)
# NVMe-oF uses WaitForFirstConsumer, so PVCs bind when pods are scheduled
for i in $(seq 0 $((REPLICAS - 1))); do
    POD_NAME="${STS_NAME}-${i}"
    test_info "Waiting for pod ${POD_NAME}..."
    
    # Extended timeout for NVMe-oF device attachment
    kubectl wait --for=condition=Ready pod/"${POD_NAME}" \
        -n "${TEST_NAMESPACE}" \
        --timeout=360s
    test_success "Pod ${POD_NAME} is ready"
done

#######################################
# Verify each pod has its own PVC/PV
#######################################
test_step "Verifying each pod has unique persistent volume"

echo ""
# Configure test with 10 total steps
set_test_steps 10
test_info "Checking PVCs created by volumeClaimTemplates..."
for i in $(seq 0 $((REPLICAS - 1))); do
    PVC_NAME="data-${STS_NAME}-${i}"
    POD_NAME="${STS_NAME}-${i}"
    
    # Check PVC exists and is bound
    PVC_STATUS=$(kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.status.phase}')
    if [[ "${PVC_STATUS}" == "Bound" ]]; then
        PV_NAME=$(kubectl get pvc "${PVC_NAME}" -n "${TEST_NAMESPACE}" -o jsonpath='{.spec.volumeName}')
        test_success "Pod ${POD_NAME} -> PVC ${PVC_NAME} -> PV ${PV_NAME}"
    else
        test_error "PVC ${PVC_NAME} is not bound (status: ${PVC_STATUS})"
        exit 1
    fi
done

#######################################
# Verify data isolation between replicas
#######################################
test_step "Verifying data isolation between replicas"

echo ""
# Configure test with 10 total steps
test_info "Checking each pod wrote to its own volume..."
for i in $(seq 0 $((REPLICAS - 1))); do
    POD_NAME="${STS_NAME}-${i}"
    
    if ! IDENTITY=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/pod-identity.txt 2>&1 | head -1); then
        test_error "${POD_NAME}: Failed to read pod identity file!"
        test_error "Error: ${IDENTITY}"
        show_diagnostic_logs "${POD_NAME}" ""
        exit 1
    fi
    
    EXPECTED="Pod: ${POD_NAME}"
    
    if [[ "${IDENTITY}" == "${EXPECTED}" ]]; then
        test_success "${POD_NAME}: Correct identity stored"
    else
        test_error "${POD_NAME}: Identity mismatch! Expected '${EXPECTED}', got '${IDENTITY}'"
        show_diagnostic_logs "${POD_NAME}" ""
        exit 1
    fi
done

# Write unique data to each pod's volume
echo ""
# Configure test with 10 total steps
test_info "Writing unique test data to each replica's volume..."
for i in $(seq 0 $((REPLICAS - 1))); do
    POD_NAME="${STS_NAME}-${i}"
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- \
        sh -c "echo 'Unique data for replica ${i}' > /data/replica-data.txt && sync"
    test_success "Wrote data to ${POD_NAME}"
done

#######################################
# Test: Scale down and verify remaining data
#######################################
test_step "Testing scale down operation"

# DIAGNOSTIC: Show state BEFORE scaling down
test_info "BEFORE SCALE DOWN: Capturing baseline state..."
test_info "Node NVMe devices before scale down:"
kubectl exec -n kube-system $(kubectl get pods -n kube-system -l app=tns-csi-node -o jsonpath='{.items[0].metadata.name}') -c tns-csi-plugin -- sh -c 'nvme list 2>&1 || echo "nvme list failed"; echo "=== Block devices ==="; lsblk | grep nvme || echo "No nvme in lsblk"' 2>&1 | head -30 || true

# Check that all pods can still read their data BEFORE scaling
test_info "Verifying all pods can read data BEFORE scale down..."
for i in $(seq 0 $((REPLICAS - 1))); do
    POD_NAME="${STS_NAME}-${i}"
    if REPLICA_DATA=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/replica-data.txt 2>&1); then
        test_success "${POD_NAME}: Can read data before scale down: ${REPLICA_DATA}"
    else
        test_error "${POD_NAME}: Cannot read data BEFORE scale down - test environment is broken!"
        exit 1
    fi
done

NEW_REPLICAS=2
test_info "Scaling StatefulSet from ${REPLICAS} to ${NEW_REPLICAS} replicas..."
kubectl scale statefulset "${STS_NAME}" -n "${TEST_NAMESPACE}" --replicas=${NEW_REPLICAS}

# Wait for scale down (last pod should be deleted)
DELETED_POD="${STS_NAME}-$((REPLICAS - 1))"
test_info "Waiting for pod ${DELETED_POD} to be deleted..."
kubectl wait --for=delete pod/"${DELETED_POD}" -n "${TEST_NAMESPACE}" --timeout=120s || true

test_success "Scaled down to ${NEW_REPLICAS} replicas"

# DIAGNOSTIC: Give system time to settle after delete and show state
test_info "Waiting 5 seconds for system to settle after pod deletion..."
sleep 5
test_info "AFTER SCALE DOWN: Checking node state..."
kubectl exec -n kube-system $(kubectl get pods -n kube-system -l app=tns-csi-node -o jsonpath='{.items[0].metadata.name}') -c tns-csi-plugin -- sh -c 'nvme list 2>&1 || echo "nvme list failed"; echo "=== Block devices ==="; lsblk | grep nvme || echo "No nvme in lsblk"' 2>&1 | head -30 || true

# Verify remaining pods still have their data
echo ""
# Configure test with 10 total steps
test_info "Verifying remaining pods retained their data..."

# First, show node state AFTER scaling down to understand the environment
test_info "Checking NVMe device state on node after scale down..."
kubectl exec -n kube-system $(kubectl get pods -n kube-system -l app=tns-csi-node -o jsonpath='{.items[0].metadata.name}') -c tns-csi-plugin -- sh -c 'echo "=== NVMe devices ==="; ls -la /dev/nvme* 2>&1 || echo "No NVMe devices"; echo "=== NVMe list ==="; nvme list 2>&1 || echo "nvme list failed"' || true

for i in $(seq 0 $((NEW_REPLICAS - 1))); do
    POD_NAME="${STS_NAME}-${i}"
    
    test_info "Checking pod ${POD_NAME} volume state..."
    
    # Show mount information inside the pod
    test_info "${POD_NAME}: Mount information:"
    kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- sh -c 'echo "=== Mounts ==="; mount | grep /data; echo "=== /data contents ==="; ls -la /data; echo "=== Filesystem on /data ==="; df -h /data' 2>&1 | head -20 || true
    
    # Check if file is accessible first
    test_info "${POD_NAME}: Testing if /data/replica-data.txt exists..."
    if ! kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- test -f /data/replica-data.txt 2>&1; then
        test_error "${POD_NAME}: File /data/replica-data.txt does not exist or is not accessible!"
        test_error "${POD_NAME}: Directory listing:"
        kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- ls -la /data 2>&1 || true
        show_diagnostic_logs "${POD_NAME}" ""
        exit 1
    fi
    
    # Read the data - explicitly check for errors
    test_info "${POD_NAME}: Reading /data/replica-data.txt..."
    if ! REPLICA_DATA=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/replica-data.txt 2>&1); then
        test_error "${POD_NAME}: Failed to read /data/replica-data.txt - I/O error!"
        test_error "Error output: ${REPLICA_DATA}"
        
        # Additional debugging: check dmesg for I/O errors
        test_error "${POD_NAME}: Checking for kernel I/O errors..."
        kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- dmesg | tail -50 2>&1 || true
        
        # Check mount status
        test_error "${POD_NAME}: Mount status:"
        kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- mount | grep /data 2>&1 || true
        
        # Check if device is still connected
        test_error "${POD_NAME}: Checking NVMe devices in container:"
        kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- ls -la /dev/nvme* 2>&1 || echo "No NVMe devices visible"
        
        show_diagnostic_logs "${POD_NAME}" ""
        exit 1
    fi
    
    EXPECTED="Unique data for replica ${i}"
    
    if [[ "${REPLICA_DATA}" == "${EXPECTED}" ]]; then
        test_success "${POD_NAME}: Data intact after scale down"
    else
        test_error "${POD_NAME}: Data corrupted after scale down!"
        test_error "Expected: '${EXPECTED}'"
        test_error "Got: '${REPLICA_DATA}'"
        show_diagnostic_logs "${POD_NAME}" ""
        exit 1
    fi
done

# Important: PVC for scaled-down pod should still exist (StatefulSet behavior)
SCALED_DOWN_PVC="data-${STS_NAME}-$((REPLICAS - 1))"
if kubectl get pvc "${SCALED_DOWN_PVC}" -n "${TEST_NAMESPACE}" &>/dev/null; then
    test_success "PVC ${SCALED_DOWN_PVC} retained after scale down (StatefulSet behavior)"
else
    test_error "PVC ${SCALED_DOWN_PVC} was deleted (unexpected!)"
    exit 1
fi

#######################################
# Test: Scale back up and verify volume reattachment
#######################################
test_info "Scaling back up to ${REPLICAS} replicas..."
kubectl scale statefulset "${STS_NAME}" -n "${TEST_NAMESPACE}" --replicas=${REPLICAS}

# Wait for scaled-up pod to be ready
SCALED_UP_POD="${STS_NAME}-$((REPLICAS - 1))"
test_info "Waiting for pod ${SCALED_UP_POD} to be ready..."
kubectl wait --for=condition=Ready pod/"${SCALED_UP_POD}" \
    -n "${TEST_NAMESPACE}" \
    --timeout=360s

test_success "Scaled back up to ${REPLICAS} replicas"

# Verify the scaled-up pod has the same PVC and data
echo ""
# Configure test with 10 total steps
test_info "Verifying scaled-up pod reattached to original volume..."

if ! IDENTITY=$(kubectl exec "${SCALED_UP_POD}" -n "${TEST_NAMESPACE}" -- cat /data/pod-identity.txt 2>&1 | head -1); then
    test_error "${SCALED_UP_POD}: Failed to read pod identity after scale-up!"
    test_error "Error: ${IDENTITY}"
    show_diagnostic_logs "${SCALED_UP_POD}" ""
    exit 1
fi

EXPECTED="Pod: ${SCALED_UP_POD}"

if [[ "${IDENTITY}" == "${EXPECTED}" ]]; then
    test_success "${SCALED_UP_POD}: Reattached to original volume with preserved data"
else
    test_error "${SCALED_UP_POD}: Did not reattach to original volume!"
    test_error "Expected: '${EXPECTED}', Got: '${IDENTITY}'"
    show_diagnostic_logs "${SCALED_UP_POD}" ""
    exit 1
fi

#######################################
# Test: Rolling update (delete pod and let it recreate)
#######################################
test_step "Testing rolling update (pod recreation)"

TEST_POD="${STS_NAME}-1"
test_info "Deleting pod ${TEST_POD} to simulate rolling update..."
kubectl delete pod "${TEST_POD}" -n "${TEST_NAMESPACE}" --timeout=60s

test_info "Waiting for StatefulSet controller to recreate pod..."
kubectl wait --for=condition=Ready pod/"${TEST_POD}" \
    -n "${TEST_NAMESPACE}" \
    --timeout=360s

test_success "Pod ${TEST_POD} recreated"

# Verify recreated pod has the same data
echo ""
# Configure test with 10 total steps
test_info "Verifying recreated pod has original data..."

if ! REPLICA_DATA=$(kubectl exec "${TEST_POD}" -n "${TEST_NAMESPACE}" -- cat /data/replica-data.txt 2>&1); then
    test_error "${TEST_POD}: Failed to read data after rolling update!"
    test_error "Error: ${REPLICA_DATA}"
    show_diagnostic_logs "${TEST_POD}" ""
    exit 1
fi

EXPECTED="Unique data for replica 1"

if [[ "${REPLICA_DATA}" == "${EXPECTED}" ]]; then
    test_success "${TEST_POD}: Data persisted through rolling update"
else
    test_error "${TEST_POD}: Data lost during rolling update!"
    test_error "Expected: '${EXPECTED}', Got: '${REPLICA_DATA}'"
    show_diagnostic_logs "${TEST_POD}" ""
    exit 1
fi

# Verify metrics
verify_metrics

# Cleanup by deleting namespace (also tests proper cleanup of StatefulSet volumes)
test_info "Cleaning up test namespace (includes StatefulSet, PVCs, and TrueNAS resources)..."
kubectl delete namespace "${TEST_NAMESPACE}" --timeout=120s || {
    test_warning "Namespace deletion timed out, forcing deletion"
    kubectl delete namespace "${TEST_NAMESPACE}" --force --grace-period=0 || true
}

# Wait for PVs to be deleted (StatefulSet creates 3 PVCs -> 3 PVs)
test_info "Waiting for PVs to be deleted..."
for i in {1..60}; do
    REMAINING_PVS=$(kubectl get pv --no-headers 2>/dev/null | grep -c "${TEST_NAMESPACE}" || echo "0")
    if [[ "${REMAINING_PVS}" == "0" ]]; then
        test_success "All PVs deleted successfully"
        break
    fi
    if [[ $i == 60 ]]; then
        test_warning "Some PVs still exist after 60 seconds"
        kubectl get pv | grep "${TEST_NAMESPACE}" || true
    fi
    sleep 1
done

# Additional wait for TrueNAS backend cleanup
test_info "Waiting for TrueNAS backend cleanup (15 seconds)..."
sleep 15
test_success "Cleanup complete"

# Success
test_summary "${PROTOCOL}" "PASSED"
