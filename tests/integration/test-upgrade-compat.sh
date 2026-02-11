#!/bin/bash
# Upgrade Compatibility Test
# Verifies volumes created with the previous release still work after upgrading
# to the current build. Tests NFS and iSCSI protocols.
#
# Required environment variables:
#   TRUENAS_HOST       - TrueNAS server hostname/IP
#   TRUENAS_API_KEY    - TrueNAS API key
#   TRUENAS_POOL       - ZFS pool name
#   CSI_IMAGE_TAG      - Image tag for the current build
#   PREV_CHART_VERSION - Previous Helm chart version (e.g., "0.8.0")
#
# Optional environment variables:
#   PREV_VERSION         - Previous release tag for display (e.g., "v0.8.0")
#   CSI_IMAGE_REPOSITORY - Image repository (default: ghcr.io/fenio/tns-csi)
#   KUBELET_PATH         - Kubelet data directory (default: /var/lib/kubelet)
#   OCI_CHART_REPO       - OCI chart repository (default: oci://registry-1.docker.io/bfenski/tns-csi-driver)

set -euo pipefail

# ─────────────────────────────────────────────────
# Colors and output helpers
# ─────────────────────────────────────────────────
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${CYAN}ℹ${NC} $1"; }
pass()  { echo -e "${GREEN}✓${NC} $1"; }
fail()  { echo -e "${RED}✗${NC} $1"; }
warn()  { echo -e "${YELLOW}⚠${NC} $1"; }
phase() {
    echo ""
    echo -e "${BLUE}════════════════════════════════════════${NC}"
    echo -e "${BLUE}  $1${NC}"
    echo -e "${BLUE}════════════════════════════════════════${NC}"
    echo ""
}

# ─────────────────────────────────────────────────
# Configuration
# ─────────────────────────────────────────────────
NAMESPACE="upgrade-compat-$(date +%s)-${RANDOM}"
TRUENAS_URL="wss://${TRUENAS_HOST}/api/current"
OCI_CHART_REPO="${OCI_CHART_REPO:-oci://registry-1.docker.io/bfenski/tns-csi-driver}"
IMAGE_TAG="${CSI_IMAGE_TAG:-latest}"
IMAGE_REPO="${CSI_IMAGE_REPOSITORY:-ghcr.io/fenio/tns-csi}"
KUBELET_PATH="${KUBELET_PATH:-/var/lib/kubelet}"

# Validate required environment
for var in TRUENAS_HOST TRUENAS_API_KEY TRUENAS_POOL PREV_CHART_VERSION; do
    if [[ -z "${!var:-}" ]]; then
        fail "Required environment variable ${var} is not set"
        exit 1
    fi
done

# Track results
PHASE_RESULTS=()
FAILED=0

record_phase() {
    local name=$1 status=$2
    PHASE_RESULTS+=("${name}:${status}")
    if [[ "$status" == "FAIL" ]]; then
        FAILED=1
    fi
}

# ─────────────────────────────────────────────────
# Diagnostic dump (on failure)
# ─────────────────────────────────────────────────
dump_diagnostics() {
    echo ""
    echo "════════════════════════════════════════"
    echo "  DIAGNOSTIC INFORMATION"
    echo "════════════════════════════════════════"
    echo ""
    echo "=== CSI Driver Pods ==="
    kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver -o wide 2>/dev/null || true
    echo ""
    echo "=== Controller Logs (last 100 lines) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        -c tns-csi-plugin --tail=100 2>/dev/null || true
    echo ""
    echo "=== Node Logs (last 100 lines) ==="
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
        --tail=100 2>/dev/null || true
    echo ""
    echo "=== Test Namespace Events ==="
    kubectl get events -n "${NAMESPACE}" --sort-by='.lastTimestamp' 2>/dev/null || true
    echo ""
    echo "=== PVCs ==="
    kubectl get pvc -n "${NAMESPACE}" -o wide 2>/dev/null || true
    echo ""
    echo "=== PVs ==="
    kubectl get pv 2>/dev/null || true
    echo "════════════════════════════════════════"

    # Save to /tmp/test-logs/ for artifact upload
    mkdir -p /tmp/test-logs
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller \
        -c tns-csi-plugin --tail=500 > /tmp/test-logs/controller.log 2>&1 || true
    kubectl logs -n kube-system \
        -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node \
        --tail=500 > /tmp/test-logs/node.log 2>&1 || true
    kubectl get events -n "${NAMESPACE}" --sort-by='.lastTimestamp' > /tmp/test-logs/events.log 2>&1 || true
}

# ─────────────────────────────────────────────────
# Cleanup
# ─────────────────────────────────────────────────
cleanup() {
    echo ""
    info "Cleaning up..."

    info "Deleting test namespace ${NAMESPACE}..."
    kubectl delete namespace "${NAMESPACE}" --ignore-not-found=true --timeout=120s 2>/dev/null || \
        kubectl delete namespace "${NAMESPACE}" --force --grace-period=0 --ignore-not-found=true 2>/dev/null || true

    info "Waiting for PV cleanup..."
    local timeout=60 elapsed=0
    while [[ $elapsed -lt $timeout ]]; do
        local remaining
        remaining=$(kubectl get pv -o json 2>/dev/null | jq -r ".items[] | select(.spec.claimRef.namespace==\"${NAMESPACE}\") | .metadata.name" 2>/dev/null || echo "")
        if [[ -z "$remaining" ]]; then
            break
        fi
        sleep 2
        elapsed=$((elapsed + 2))
    done

    info "Uninstalling Helm release..."
    helm uninstall tns-csi --namespace kube-system 2>/dev/null || true
    pass "Cleanup complete"
}

# Trap: dump diagnostics on failure, always clean up
on_exit() {
    local exit_code=$?
    if [[ $exit_code -ne 0 ]]; then
        dump_diagnostics
    fi
    cleanup
}
trap on_exit EXIT

# ─────────────────────────────────────────────────
# Header
# ─────────────────────────────────────────────────
echo "════════════════════════════════════════════════════"
echo "  TrueNAS CSI - Upgrade Compatibility Test"
echo "  Previous: ${PREV_VERSION:-v${PREV_CHART_VERSION}}"
echo "  Current:  ${IMAGE_REPO}:${IMAGE_TAG}"
echo "════════════════════════════════════════════════════"

# Create test namespace
info "Creating test namespace: ${NAMESPACE}"
kubectl create namespace "${NAMESPACE}"

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# Phase 1: Deploy previous release and create volumes
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
phase "Phase 1: Deploy previous release (${PREV_VERSION:-v${PREV_CHART_VERSION}})"

info "Installing previous release from OCI registry..."
helm install tns-csi "${OCI_CHART_REPO}" \
    --version "${PREV_CHART_VERSION}" \
    --namespace kube-system \
    --set truenas.url="${TRUENAS_URL}" \
    --set truenas.apiKey="${TRUENAS_API_KEY}" \
    --set truenas.skipTLSVerify=true \
    --set node.kubeletPath="${KUBELET_PATH}" \
    --set storageClasses.nfs.enabled=true \
    --set storageClasses.nfs.pool="${TRUENAS_POOL}" \
    --set storageClasses.nfs.server="${TRUENAS_HOST}" \
    --set storageClasses.nvmeof.enabled=false \
    --set storageClasses.iscsi.enabled=true \
    --set storageClasses.iscsi.pool="${TRUENAS_POOL}" \
    --set storageClasses.iscsi.server="${TRUENAS_HOST}" \
    --wait --timeout 5m

info "Waiting for driver pods to be ready..."
kubectl wait --for=condition=Ready pod \
    -l app.kubernetes.io/name=tns-csi-driver \
    -n kube-system --timeout=120s

OLD_IMAGE=$(kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver \
    -o jsonpath='{.items[0].spec.containers[?(@.name=="tns-csi-driver")].image}' 2>/dev/null || echo "unknown")
pass "Previous release deployed (image: ${OLD_IMAGE})"

# --- NFS volume with old driver ---
info "Creating NFS volume with old driver..."
kubectl apply -n "${NAMESPACE}" -f - <<'EOF'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: compat-nfs-pvc
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nfs
EOF

kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/compat-nfs-pvc -n "${NAMESPACE}" --timeout=120s
pass "NFS PVC bound"

kubectl apply -n "${NAMESPACE}" -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: compat-nfs-writer
spec:
  containers:
  - name: writer
    image: public.ecr.aws/docker/library/busybox:latest
    imagePullPolicy: Always
    command: ["sh", "-c", "echo 'compat-nfs-data' > /data/test.txt && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: compat-nfs-pvc
EOF

kubectl wait --for=condition=Ready pod/compat-nfs-writer -n "${NAMESPACE}" --timeout=120s

NFS_DATA=$(kubectl exec compat-nfs-writer -n "${NAMESPACE}" -- cat /data/test.txt)
if [[ "$NFS_DATA" != "compat-nfs-data" ]]; then
    fail "NFS write verification failed: got '${NFS_DATA}'"
    record_phase "Create volumes (old driver)" "FAIL"
    exit 1
fi
pass "NFS sentinel data written"

kubectl delete pod compat-nfs-writer -n "${NAMESPACE}" --timeout=60s
pass "NFS writer pod deleted (PVC retained)"

# --- iSCSI volume with old driver (if StorageClass exists) ---
ISCSI_OLD_AVAILABLE=false
if kubectl get storageclass tns-csi-iscsi &>/dev/null; then
    info "Creating iSCSI volume with old driver..."
    kubectl apply -n "${NAMESPACE}" -f - <<'EOF'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: compat-iscsi-pvc
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-iscsi
EOF

    kubectl apply -n "${NAMESPACE}" -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: compat-iscsi-writer
spec:
  containers:
  - name: writer
    image: public.ecr.aws/docker/library/busybox:latest
    imagePullPolicy: Always
    command: ["sh", "-c", "echo 'compat-iscsi-data' > /data/test.txt && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: compat-iscsi-pvc
EOF

    kubectl wait --for=condition=Ready pod/compat-iscsi-writer -n "${NAMESPACE}" --timeout=180s

    ISCSI_DATA=$(kubectl exec compat-iscsi-writer -n "${NAMESPACE}" -- cat /data/test.txt)
    if [[ "$ISCSI_DATA" != "compat-iscsi-data" ]]; then
        fail "iSCSI write verification failed: got '${ISCSI_DATA}'"
        record_phase "Create volumes (old driver)" "FAIL"
        exit 1
    fi
    pass "iSCSI sentinel data written"

    kubectl delete pod compat-iscsi-writer -n "${NAMESPACE}" --timeout=60s
    pass "iSCSI writer pod deleted (PVC retained)"
    ISCSI_OLD_AVAILABLE=true
else
    warn "iSCSI StorageClass not available in previous release, skipping"
fi

record_phase "Create volumes (old driver)" "PASS"

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# Phase 2: Upgrade driver to current build
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
phase "Phase 2: Upgrade to current build (${IMAGE_REPO}:${IMAGE_TAG})"

helm upgrade tns-csi ./charts/tns-csi-driver \
    --namespace kube-system \
    --reuse-values \
    --set image.repository="${IMAGE_REPO}" \
    --set image.tag="${IMAGE_TAG}" \
    --set image.pullPolicy=Always \
    --wait --timeout 5m

info "Waiting for controller rollout..."
kubectl rollout status deployment/tns-csi-controller -n kube-system --timeout=120s

info "Waiting for node daemonset rollout..."
kubectl rollout status daemonset/tns-csi-node -n kube-system --timeout=120s

NEW_IMAGE=$(kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver \
    -o jsonpath='{.items[0].spec.containers[?(@.name=="tns-csi-driver")].image}' 2>/dev/null || echo "unknown")
pass "Driver upgraded (image: ${NEW_IMAGE})"

record_phase "Upgrade driver" "PASS"

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# Phase 3: Verify old volumes survive upgrade
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
phase "Phase 3: Verify old volumes after upgrade"

# --- Verify NFS volume ---
info "Mounting old NFS volume with new driver..."
kubectl apply -n "${NAMESPACE}" -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: compat-nfs-reader
spec:
  containers:
  - name: reader
    image: public.ecr.aws/docker/library/busybox:latest
    imagePullPolicy: Always
    command: ["sh", "-c", "cat /data/test.txt && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: compat-nfs-pvc
EOF

kubectl wait --for=condition=Ready pod/compat-nfs-reader -n "${NAMESPACE}" --timeout=120s

NFS_VERIFY=$(kubectl exec compat-nfs-reader -n "${NAMESPACE}" -- cat /data/test.txt)
if [[ "$NFS_VERIFY" == "compat-nfs-data" ]]; then
    pass "NFS data intact after upgrade"
else
    fail "NFS data mismatch: expected 'compat-nfs-data', got '${NFS_VERIFY}'"
    record_phase "Verify old volumes" "FAIL"
    exit 1
fi

kubectl delete pod compat-nfs-reader -n "${NAMESPACE}" --timeout=60s

# --- Verify iSCSI volume ---
if [[ "$ISCSI_OLD_AVAILABLE" == "true" ]]; then
    info "Mounting old iSCSI volume with new driver..."
    kubectl apply -n "${NAMESPACE}" -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: compat-iscsi-reader
spec:
  containers:
  - name: reader
    image: public.ecr.aws/docker/library/busybox:latest
    imagePullPolicy: Always
    command: ["sh", "-c", "cat /data/test.txt && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: compat-iscsi-pvc
EOF

    kubectl wait --for=condition=Ready pod/compat-iscsi-reader -n "${NAMESPACE}" --timeout=180s

    ISCSI_VERIFY=$(kubectl exec compat-iscsi-reader -n "${NAMESPACE}" -- cat /data/test.txt)
    if [[ "$ISCSI_VERIFY" == "compat-iscsi-data" ]]; then
        pass "iSCSI data intact after upgrade"
    else
        fail "iSCSI data mismatch: expected 'compat-iscsi-data', got '${ISCSI_VERIFY}'"
        record_phase "Verify old volumes" "FAIL"
        exit 1
    fi

    kubectl delete pod compat-iscsi-reader -n "${NAMESPACE}" --timeout=60s
fi

record_phase "Verify old volumes" "PASS"

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# Phase 4: Verify new volumes work with upgraded driver
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
phase "Phase 4: Verify new volume creation"

# --- New NFS volume ---
info "Creating new NFS volume with upgraded driver..."
kubectl apply -n "${NAMESPACE}" -f - <<'EOF'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: new-nfs-pvc
spec:
  accessModes:
    - ReadWriteMany
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-nfs
EOF

kubectl wait --for=jsonpath='{.status.phase}'=Bound pvc/new-nfs-pvc -n "${NAMESPACE}" --timeout=120s
pass "New NFS PVC bound"

kubectl apply -n "${NAMESPACE}" -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: new-nfs-pod
spec:
  containers:
  - name: test
    image: public.ecr.aws/docker/library/busybox:latest
    imagePullPolicy: Always
    command: ["sh", "-c", "echo 'new-nfs-data' > /data/test.txt && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: new-nfs-pvc
EOF

kubectl wait --for=condition=Ready pod/new-nfs-pod -n "${NAMESPACE}" --timeout=120s

NEW_NFS_DATA=$(kubectl exec new-nfs-pod -n "${NAMESPACE}" -- cat /data/test.txt)
if [[ "$NEW_NFS_DATA" == "new-nfs-data" ]]; then
    pass "New NFS volume works"
else
    fail "New NFS volume data mismatch: expected 'new-nfs-data', got '${NEW_NFS_DATA}'"
    record_phase "New volumes" "FAIL"
    exit 1
fi

# --- New iSCSI volume ---
if kubectl get storageclass tns-csi-iscsi &>/dev/null; then
    info "Creating new iSCSI volume with upgraded driver..."
    kubectl apply -n "${NAMESPACE}" -f - <<'EOF'
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: new-iscsi-pvc
spec:
  accessModes:
    - ReadWriteOnce
  volumeMode: Filesystem
  resources:
    requests:
      storage: 1Gi
  storageClassName: tns-csi-iscsi
EOF

    kubectl apply -n "${NAMESPACE}" -f - <<'EOF'
apiVersion: v1
kind: Pod
metadata:
  name: new-iscsi-pod
spec:
  containers:
  - name: test
    image: public.ecr.aws/docker/library/busybox:latest
    imagePullPolicy: Always
    command: ["sh", "-c", "echo 'new-iscsi-data' > /data/test.txt && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: new-iscsi-pvc
EOF

    kubectl wait --for=condition=Ready pod/new-iscsi-pod -n "${NAMESPACE}" --timeout=180s

    NEW_ISCSI_DATA=$(kubectl exec new-iscsi-pod -n "${NAMESPACE}" -- cat /data/test.txt)
    if [[ "$NEW_ISCSI_DATA" == "new-iscsi-data" ]]; then
        pass "New iSCSI volume works"
    else
        fail "New iSCSI volume data mismatch: expected 'new-iscsi-data', got '${NEW_ISCSI_DATA}'"
        record_phase "New volumes" "FAIL"
        exit 1
    fi
fi

record_phase "New volumes" "PASS"

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# Summary
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
echo ""
echo "════════════════════════════════════════"
echo "  UPGRADE COMPATIBILITY RESULTS"
echo "════════════════════════════════════════"
for result in "${PHASE_RESULTS[@]}"; do
    IFS=':' read -r name status <<< "$result"
    if [[ "$status" == "PASS" ]]; then
        echo -e "  ${GREEN}✓${NC} ${name}"
    else
        echo -e "  ${RED}✗${NC} ${name}"
    fi
done
echo "════════════════════════════════════════"

if [[ "$FAILED" -eq 0 ]]; then
    echo -e "${GREEN}Upgrade compatibility test: PASSED${NC}"
else
    echo -e "${RED}Upgrade compatibility test: FAILED${NC}"
    exit 1
fi
