# Testing Guide - TrueNAS Scale CSI Driver

This guide walks you through deploying and testing the CSI driver in a real environment.

## Quick Start: Testing with Helm (Recommended)

The fastest way to test the CSI driver is using the Helm chart:

```bash
# Install from OCI registry
helm install tns-csi oci://registry-1.docker.io/bfenski/tns-csi-driver \
  --version 0.0.1 \
  --namespace kube-system \
  --create-namespace \
  --set truenas.url="wss://YOUR-TRUENAS-IP:443/api/current" \
  --set truenas.apiKey="YOUR-API-KEY" \
  --set storageClasses.nfs.enabled=true \
  --set storageClasses.nfs.pool="YOUR-POOL-NAME" \
  --set storageClasses.nfs.server="YOUR-TRUENAS-IP"

# Verify deployment
kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver

# Check logs
kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller -c tns-csi-plugin

# Create a test PVC
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: truenas-nfs
  resources:
    requests:
      storage: 1Gi
EOF

# Verify PVC is bound
kubectl get pvc test-pvc

# Create a test pod
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: test
    image: busybox
    command: ["sh", "-c", "echo 'Hello from TrueNAS!' > /data/test.txt && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-pvc
EOF

# Verify pod is running and data is written
kubectl wait --for=condition=Ready pod/test-pod --timeout=60s
kubectl exec test-pod -- cat /data/test.txt

# Cleanup
kubectl delete pod test-pod
kubectl delete pvc test-pvc
helm uninstall tns-csi -n kube-system
```

**Skip to "Test Volume Provisioning" section if using Helm for testing.**

---

## Advanced: Manual Testing and Development

The following sections are for advanced users who want to:
- Build custom images
- Test development changes
- Deploy manually without Helm

<details>
<summary>Manual Testing Prerequisites and Steps - Click to expand</summary>

## Prerequisites

### 1. TrueNAS Scale Setup

**Requirements:**
- TrueNAS Scale 25.10+
- At least one ZFS pool created
- API access enabled
- Network connectivity from Kubernetes cluster

**Steps:**

1. **Create an API Key:**
   ```
   TrueNAS UI → System Settings → API Keys → Add API Key
   - Name: kubernetes-csi-driver
   - Click "Add"
   - SAVE THE API KEY - you won't see it again!
   ```

2. **Verify NFS Service is Running:**
   ```
   TrueNAS UI → System Settings → Services
   - Ensure "NFS" service is running
   - If not, start it and set to "Start Automatically"
   ```

3. **Note Your Configuration:**
   - TrueNAS IP: `_______________`
   - ZFS Pool Name: `_______________`
   - API Key: `_______________`

### 2. Kubernetes Cluster Setup

**Requirements:**
- Kubernetes 1.20+
- kubectl configured with cluster-admin access
- Nodes must have NFS client utilities installed

**Install NFS Client on All Nodes:**

```bash
# Ubuntu/Debian
sudo apt-get update
sudo apt-get install -y nfs-common

# RHEL/CentOS/Fedora
sudo yum install -y nfs-utils

# Verify installation
which mount.nfs
```

### 3. Docker Registry Access

You need a place to host your Docker image. Options:

- **Docker Hub:** Free public or paid private repositories
- **GitHub Container Registry (ghcr.io):** Free for public repos
- **Private Registry:** Self-hosted or cloud-based
- **Local Registry:** For testing only (kind/minikube)

## Step 1: Build and Push Docker Image

### Option A: Public Docker Hub

```bash
cd /Users/bfenski/tns-csi

# Replace 'yourusername' with your Docker Hub username
export REGISTRY_USER="yourusername"
export IMAGE_TAG="v0.0.1"

# Build the image
docker build -t ${REGISTRY_USER}/tns-csi-driver:${IMAGE_TAG} .
docker tag ${REGISTRY_USER}/tns-csi-driver:${IMAGE_TAG} ${REGISTRY_USER}/tns-csi-driver:latest

# Login to Docker Hub
docker login

# Push the image
docker push ${REGISTRY_USER}/tns-csi-driver:${IMAGE_TAG}
docker push ${REGISTRY_USER}/tns-csi-driver:latest
```

### Option B: GitHub Container Registry

```bash
cd /Users/bfenski/tns-csi

# Replace 'yourusername' with your GitHub username
export GITHUB_USER="yourusername"
export IMAGE_TAG="v0.0.1"

# Build the image
docker build -t ghcr.io/${GITHUB_USER}/tns-csi-driver:${IMAGE_TAG} .
docker tag ghcr.io/${GITHUB_USER}/tns-csi-driver:${IMAGE_TAG} ghcr.io/${GITHUB_USER}/tns-csi-driver:latest

# Login to GitHub Container Registry (requires personal access token)
echo $GITHUB_TOKEN | docker login ghcr.io -u ${GITHUB_USER} --password-stdin

# Push the image
docker push ghcr.io/${GITHUB_USER}/tns-csi-driver:${IMAGE_TAG}
docker push ghcr.io/${GITHUB_USER}/tns-csi-driver:latest
```

### Option C: Local Registry (for kind/minikube testing)

**For kind:**
```bash
cd /Users/bfenski/tns-csi

# Build the image
docker build -t tns-csi-driver:latest .

# Load directly into kind
kind load docker-image tns-csi-driver:latest
```

**For minikube:**
```bash
# Use minikube's Docker daemon
eval $(minikube docker-env)

cd /Users/bfenski/tns-csi

# Build the image
docker build -t tns-csi-driver:latest .
```

## Step 2: Update Deployment Manifests

After building your image, update the manifests to use your image:

```bash
cd /Users/bfenski/tns-csi/deploy

# For Docker Hub
export IMAGE_NAME="yourusername/tns-csi-driver:latest"

# For GitHub Container Registry
export IMAGE_NAME="ghcr.io/yourusername/tns-csi-driver:latest"

# For local testing
export IMAGE_NAME="tns-csi-driver:latest"

# Update the manifests
sed -i.bak "s|your-registry/tns-csi-driver:latest|${IMAGE_NAME}|g" controller.yaml node.yaml

# Verify the change
grep "image:" controller.yaml node.yaml
```

## Step 3: Configure TrueNAS Credentials

Edit the secret file with your TrueNAS details:

```bash
cd /Users/bfenski/tns-csi/deploy

# Edit secret.yaml
# Replace:
#   - YOUR_TRUENAS_IP with your TrueNAS server IP
#   - YOUR_API_KEY_HERE with your actual API key

# Example:
cat > secret.yaml << 'EOF'
---
apiVersion: v1
kind: Secret
metadata:
  name: tns-csi-secret
  namespace: kube-system
type: Opaque
stringData:
  url: "ws://192.168.1.100/websocket"
  api-key: "1-YourActualAPIKeyHere"
EOF
```

## Step 4: Configure Storage Class

Edit the storage class with your TrueNAS pool and server details:

```bash
cd /Users/bfenski/tns-csi/deploy

# Edit storageclass.yaml
# Update the following parameters:
#   - pool: your ZFS pool name (e.g., "tank", "pool1")
#   - server: your TrueNAS IP address
#   - parentDataset: (optional) parent dataset path

# Example:
cat > storageclass-nfs.yaml << 'EOF'
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: tns-nfs
provisioner: tns.csi.io
parameters:
  protocol: "nfs"
  pool: "tank"
  parentDataset: "tank/kubernetes"
  server: "192.168.1.100"
allowVolumeExpansion: false  # Not yet implemented
reclaimPolicy: Delete
volumeBindingMode: Immediate
EOF
```

## Step 5: Deploy the CSI Driver

Deploy in the correct order:

```bash
cd /Users/bfenski/tns-csi/deploy

# 1. Create RBAC resources
kubectl apply -f rbac.yaml

# 2. Create the secret
kubectl apply -f secret.yaml

# 3. Register the CSI driver
kubectl apply -f csidriver.yaml

# 4. Deploy the controller
kubectl apply -f controller.yaml

# 5. Deploy the node plugin
kubectl apply -f node.yaml

# 6. Create the storage class
kubectl apply -f storageclass-nfs.yaml
```

## Step 6: Verify Deployment

```bash
# Check controller pod
kubectl get pods -n kube-system -l app=tns-csi-controller
# Should show: 1/1 Running

# Check node pods (one per node)
kubectl get pods -n kube-system -l app=tns-csi-node
# Should show: 1/1 Running for each node

# Check CSI driver registration
kubectl get csidrivers
# Should show: tns.csi.io

# Check storage class
kubectl get storageclass tns-nfs
# Should show: tns-nfs with PROVISIONER tns.csi.io

# Check controller logs
kubectl logs -n kube-system -l app=tns-csi-controller -c tns-csi-plugin --tail=50

# Check node logs
kubectl logs -n kube-system -l app=tns-csi-node -c tns-csi-plugin --tail=50
```

## Step 7: Test Volume Provisioning

### Test 1: Create a PVC

```bash
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: tns-nfs
  resources:
    requests:
      storage: 1Gi
EOF
```

**Verify:**
```bash
# Check PVC status
kubectl get pvc test-pvc
# Should show: Bound

# Check PV was created
kubectl get pv
# Should show a PV bound to test-pvc

# Check TrueNAS
# In TrueNAS UI → Storage → Datasets
# You should see a new dataset under your pool/parentDataset

# In TrueNAS UI → Shares → NFS
# You should see a new NFS share
```

### Test 2: Use the Volume in a Pod

```bash
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: test
    image: busybox
    command: ["sh", "-c", "echo 'Hello from TrueNAS!' > /data/test.txt && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-pvc
EOF
```

**Verify:**
```bash
# Wait for pod to be running
kubectl wait --for=condition=Ready pod/test-pod --timeout=60s

# Check if file was written
kubectl exec test-pod -- cat /data/test.txt
# Should output: Hello from TrueNAS!

# Check mount on the node
NODE=$(kubectl get pod test-pod -o jsonpath='{.spec.nodeName}')
echo "Pod is running on node: $NODE"

# If you have SSH access to the node:
# ssh $NODE
# mount | grep truenas
# Should show the NFS mount
```

### Test 3: Test Data Persistence

```bash
# Write more data
kubectl exec test-pod -- sh -c "echo 'Persistent data test' > /data/persistent.txt"
kubectl exec test-pod -- ls -la /data/

# Delete the pod
kubectl delete pod test-pod

# Recreate it
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: test-pod-2
spec:
  containers:
  - name: test
    image: busybox
    command: ["sleep", "3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-pvc
EOF

# Wait and verify data persisted
kubectl wait --for=condition=Ready pod/test-pod-2 --timeout=60s
kubectl exec test-pod-2 -- cat /data/test.txt
kubectl exec test-pod-2 -- cat /data/persistent.txt
# Both files should exist!
```

### Test 4: Test Volume Deletion

```bash
# Delete the pod
kubectl delete pod test-pod-2

# Delete the PVC
kubectl delete pvc test-pvc

# Check that PV was deleted
kubectl get pv
# Should not show the test PV

# Check TrueNAS
# For NFS: dataset and NFS share should be deleted
# For NVMe-oF: namespace, subsystem, and ZVOL should be deleted
# (Use UI: Storage → Datasets; Shares → NFS; Services → NVMe-oF if available)
```

## Step 8: Test Multiple Volumes

```bash
# Create multiple PVCs
for i in {1..3}; do
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: multi-pvc-${i}
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: tns-nfs
  resources:
    requests:
      storage: 500Mi
EOF
done

# Verify all are bound
kubectl get pvc
# Should show 3 PVCs all Bound

# Check TrueNAS - should see 3 new datasets and shares

# Clean up
kubectl delete pvc multi-pvc-1 multi-pvc-2 multi-pvc-3
```

## Troubleshooting

### PVC Stuck in Pending

```bash
# Check events
kubectl describe pvc <pvc-name>

# Check controller logs (Helm)
kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller -c tns-csi-plugin --tail=100

# Check controller logs (Manual)
kubectl logs -n kube-system -l app=tns-csi-controller -c tns-csi-plugin --tail=100

# Common issues:
# - Wrong TrueNAS credentials (check secret)
# - Network connectivity (can cluster reach TrueNAS?)
# - Pool doesn't exist (check pool parameter in storage class)
# - API key lacks permissions
```

### Pod Stuck in ContainerCreating

```bash
# Check events
kubectl describe pod <pod-name>

# Check node logs (Helm)
kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node -c tns-csi-plugin --tail=100

# Check node logs (Manual)
kubectl logs -n kube-system -l app=tns-csi-node -c tns-csi-plugin --tail=100

# Common issues:
# - NFS client not installed on nodes
# - Network connectivity from node to TrueNAS
# - NFS service not running on TrueNAS
# - Firewall blocking NFS ports (2049)
```

### Volume Not Deleting

```bash
# Check finalizers
kubectl get pvc <pvc-name> -o yaml | grep finalizers -A 5

# Check controller logs (Helm)
kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller -c tns-csi-plugin --tail=100

# Check controller logs (Manual)
kubectl logs -n kube-system -l app=tns-csi-controller -c tns-csi-plugin --tail=100

# If stuck, you may need to manually remove finalizers:
kubectl patch pvc <pvc-name> -p '{"metadata":{"finalizers":null}}'

# Then manually clean up in TrueNAS UI
```

### Check CSI Driver Health

```bash
# Test from controller pod
kubectl exec -n kube-system -it <controller-pod-name> -c tns-csi-plugin -- sh

# Inside the pod, check if binary exists
/usr/local/bin/tns-csi-driver --version

# Check socket
ls -la /var/lib/csi/sockets/pluginproxy/
```

## Performance Testing

### Basic Write Performance

```bash
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Pod
metadata:
  name: perf-test
spec:
  containers:
  - name: test
    image: ubuntu
    command: ["bash", "-c", "apt-get update && apt-get install -y fio && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-pvc
EOF

# Wait for pod
kubectl wait --for=condition=Ready pod/perf-test --timeout=120s

# Run fio test
kubectl exec perf-test -- fio --name=write-test --ioengine=libaio --iodepth=1 --rw=write --bs=4k --size=1G --numjobs=1 --runtime=60 --time_based --directory=/data
```

## Test Checklist

- [ ] Controller pod running
- [ ] Node pods running on all nodes
- [ ] CSI driver registered
- [ ] Storage class created
- [ ] PVC provisions successfully
- [ ] Pod can mount PVC
- [ ] Can write data to volume
- [ ] Data persists across pod restarts
- [ ] PVC deletion removes PV
- [ ] TrueNAS dataset and share are created
- [ ] TrueNAS dataset and share are deleted on PVC deletion
- [ ] NVMe-oF subsystem, namespace, and ZVOL are created for NVMe-oF volumes
- [ ] NVMe-oF subsystem, namespace, and ZVOL are deleted on PVC deletion (NVMe-oF)
- [ ] Multiple PVCs can coexist
- [ ] Logs show no errors

## Next Steps

Once basic testing is successful:

1. **Test edge cases:**
   - Network failures during operations
   - TrueNAS API unavailability
   - Node failures
   - Concurrent volume operations

2. **Production hardening:**
   - Set proper resource limits
   - Configure monitoring/alerting
   - Set up log aggregation
   - Document operational procedures

3. **Advanced features:**
   - Implement volume expansion
   - Implement snapshots
   - Add metrics/monitoring
   - Performance tuning

## Clean Up

To remove the CSI driver completely:

### Helm Installation

```bash
# Delete all PVCs using the storage class first
kubectl delete pvc --all

# Uninstall Helm release
helm uninstall tns-csi -n kube-system

# Verify everything is gone
kubectl get pods -n kube-system -l app.kubernetes.io/name=tns-csi-driver
kubectl get pv
```

### Manual Installation

```bash
# Delete all PVCs using the storage class first
kubectl delete pvc --all

# Delete the driver components
kubectl delete -f deploy/node.yaml
kubectl delete -f deploy/controller.yaml
kubectl delete -f deploy/storageclass-nfs.yaml
kubectl delete -f deploy/csidriver.yaml
kubectl delete -f deploy/secret.yaml
kubectl delete -f deploy/rbac.yaml

# Verify everything is gone
kubectl get pods -n kube-system | grep truenas
kubectl get pv
```

Manually clean up any remaining datasets/shares in TrueNAS UI if needed.

</details>
