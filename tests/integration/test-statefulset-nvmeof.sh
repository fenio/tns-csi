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
test_step 4 10 "Creating headless service: ${SERVICE_NAME}"

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
test_step 5 10 "Creating StatefulSet: ${STS_NAME} (${REPLICAS} replicas)"

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
test_step 6 10 "Waiting for all ${REPLICAS} pods to be ready"

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
test_step 7 10 "Verifying each pod has unique persistent volume"

echo ""
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
test_step 8 10 "Verifying data isolation between replicas"

echo ""
test_info "Checking each pod wrote to its own volume..."
for i in $(seq 0 $((REPLICAS - 1))); do
    POD_NAME="${STS_NAME}-${i}"
    IDENTITY=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/pod-identity.txt | head -1)
    EXPECTED="Pod: ${POD_NAME}"
    
    if [[ "${IDENTITY}" == "${EXPECTED}" ]]; then
        test_success "${POD_NAME}: Correct identity stored"
    else
        test_error "${POD_NAME}: Identity mismatch! Expected '${EXPECTED}', got '${IDENTITY}'"
        exit 1
    fi
done

# Write unique data to each pod's volume
echo ""
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
test_step 9 10 "Testing scale down operation"

NEW_REPLICAS=2
test_info "Scaling StatefulSet from ${REPLICAS} to ${NEW_REPLICAS} replicas..."
kubectl scale statefulset "${STS_NAME}" -n "${TEST_NAMESPACE}" --replicas=${NEW_REPLICAS}

# Wait for scale down (last pod should be deleted)
DELETED_POD="${STS_NAME}-$((REPLICAS - 1))"
test_info "Waiting for pod ${DELETED_POD} to be deleted..."
kubectl wait --for=delete pod/"${DELETED_POD}" -n "${TEST_NAMESPACE}" --timeout=120s || true

test_success "Scaled down to ${NEW_REPLICAS} replicas"

# Verify remaining pods still have their data
echo ""
test_info "Verifying remaining pods retained their data..."
for i in $(seq 0 $((NEW_REPLICAS - 1))); do
    POD_NAME="${STS_NAME}-${i}"
    REPLICA_DATA=$(kubectl exec "${POD_NAME}" -n "${TEST_NAMESPACE}" -- cat /data/replica-data.txt)
    EXPECTED="Unique data for replica ${i}"
    
    if [[ "${REPLICA_DATA}" == "${EXPECTED}" ]]; then
        test_success "${POD_NAME}: Data intact after scale down"
    else
        test_error "${POD_NAME}: Data corrupted after scale down!"
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
test_info "Verifying scaled-up pod reattached to original volume..."
IDENTITY=$(kubectl exec "${SCALED_UP_POD}" -n "${TEST_NAMESPACE}" -- cat /data/pod-identity.txt | head -1)
EXPECTED="Pod: ${SCALED_UP_POD}"

if [[ "${IDENTITY}" == "${EXPECTED}" ]]; then
    test_success "${SCALED_UP_POD}: Reattached to original volume with preserved data"
else
    test_error "${SCALED_UP_POD}: Did not reattach to original volume!"
    exit 1
fi

#######################################
# Test: Rolling update (delete pod and let it recreate)
#######################################
test_step 10 10 "Testing rolling update (pod recreation)"

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
test_info "Verifying recreated pod has original data..."
REPLICA_DATA=$(kubectl exec "${TEST_POD}" -n "${TEST_NAMESPACE}" -- cat /data/replica-data.txt)
EXPECTED="Unique data for replica 1"

if [[ "${REPLICA_DATA}" == "${EXPECTED}" ]]; then
    test_success "${TEST_POD}: Data persisted through rolling update"
else
    test_error "${TEST_POD}: Data lost during rolling update!"
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
test_info "Waiting for TrueNAS backend cleanup (30 seconds)..."
sleep 30
test_success "Cleanup complete"

# Success
test_summary "${PROTOCOL}" "PASSED"
