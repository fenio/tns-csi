# Testing with Kind (Kubernetes in Docker)

This guide shows how to test the TrueNAS CSI driver in a local Kind cluster with NFS support.

## Prerequisites

1. **Docker**: Running and accessible
2. **Kind**: Install from https://kind.sigs.k8s.io/docs/user/quick-start/
3. **kubectl**: Kubernetes CLI tool
4. **TrueNAS**: Accessible TrueNAS server with API access

## Quick Start with Helm (Recommended)

### 1. Create Kind Cluster

```bash
kind create cluster --config kind-config.yaml --name truenas-csi-test
```

### 2. Install NFS Support

Install `nfs-common` package on all Kind nodes, which is required for NFS mounts:

```bash
# For each Kind node (control-plane and workers)
docker exec truenas-csi-test-control-plane apt-get update
docker exec truenas-csi-test-control-plane apt-get install -y nfs-common

# If you have worker nodes
docker exec truenas-csi-test-worker apt-get update
docker exec truenas-csi-test-worker apt-get install -y nfs-common
```

### 3. Install CSI Driver via Helm

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
```

### 4. Test the Driver

Create a test PVC and pod:

```bash
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
---
apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: test
    image: busybox
    command: ["sh", "-c", "echo 'Hello from Kind!' > /data/test.txt && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-pvc
EOF

# Check status
kubectl get pvc test-pvc
kubectl get pod test-pod
kubectl exec test-pod -- cat /data/test.txt
```

---

## Manual Setup (Step-by-Step)

<details>
<summary>Manual deployment for development/testing - Click to expand</summary>

If you prefer to set up manually or understand each step:

### 1. Create Kind Cluster

```bash
kind create cluster --config kind-config.yaml
```

### 2. Install NFS Support

Install `nfs-common` package on all Kind nodes:

```bash
# For each Kind node (control-plane and workers)
docker exec truenas-csi-test-control-plane apt-get update
docker exec truenas-csi-test-control-plane apt-get install -y nfs-common

# If you have worker nodes
docker exec truenas-csi-test-worker apt-get update
docker exec truenas-csi-test-worker apt-get install -y nfs-common
```

### 3. Build and Load Image

```bash
# Build
docker build -t bfenski/tns-csi:v0.0.1 .

# Load into Kind
kind load docker-image bfenski/tns-csi:v0.0.1 --name truenas-csi-test
```

### 4. Create Kubernetes Secret

```bash
# Load credentials
source .tns-credentials

# Create secret
kubectl create secret generic tns-csi-secret \
  --namespace=kube-system \
  --from-literal=url="$TRUENAS_URL" \
  --from-literal=api-key="$TRUENAS_API_KEY"
```

### 5. Deploy CSI Driver

```bash
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/csidriver.yaml
kubectl apply -f deploy/controller.yaml
kubectl apply -f deploy/node.yaml
kubectl apply -f deploy/storageclass.yaml
```

### 6. Verify Deployment

```bash
# Check pods
kubectl get pods -n kube-system -l 'app in (tns-csi-controller,tns-csi-node)'

# Check logs
kubectl logs -n kube-system -l app=tns-csi-controller -c tns-csi-plugin
kubectl logs -n kube-system -l app=tns-csi-node -c tns-csi-plugin
```

</details>

---

## Troubleshooting

### NFS Mount Issues

If pods fail to mount NFS volumes:

1. **Check NFS client installation:**
   ```bash
   docker exec truenas-csi-test-control-plane which mount.nfs
   docker exec truenas-csi-test-worker which mount.nfs
   ```

2. **Verify network connectivity:**
   ```bash
   # From a pod in the cluster
   kubectl run -it --rm debug --image=alpine --restart=Never -- sh
   apk add nfs-utils
   showmount -e YOUR-TRUENAS-IP  # Replace with your TrueNAS IP
   ```

3. **Check TrueNAS NFS service:**
   - Ensure NFS service is running in TrueNAS
   - Verify NFS shares exist
   - Check firewall allows NFS (port 2049)

### Pod Stuck in ContainerCreating

Check node plugin logs:

For Helm deployments:
```bash
kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node -c tns-csi-plugin --tail=100
```

For manual/script deployments:
```bash
kubectl logs -n kube-system -l app=tns-csi-node -c tns-csi-plugin --tail=100
```

Common issues:
- NFS client not installed (install nfs-common in Kind nodes)
- Cannot reach TrueNAS server (check network/firewall)
- Invalid NFS export path

### PVC Stuck in Pending

Check controller logs:

For Helm deployments:
```bash
kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller -c tns-csi-plugin --tail=100
```

For manual/script deployments:
```bash
kubectl logs -n kube-system -l app=tns-csi-controller -c tns-csi-plugin --tail=100
```

Common issues:
- Invalid TrueNAS credentials
- Pool doesn't exist
- Network connectivity issues

### Debug with Verbose Logging

Edit the deployment files to increase log verbosity:

```yaml
# In deploy/controller.yaml and deploy/node.yaml
args:
  - "--v=10"  # Increase from 5 to 10
```

Then restart:

For Helm deployments:
```bash
kubectl rollout restart statefulset -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=controller
kubectl rollout restart daemonset -n kube-system -l app.kubernetes.io/name=tns-csi-driver,app.kubernetes.io/component=node
```

For manual/script deployments:
```bash
kubectl rollout restart statefulset -n kube-system tns-csi-controller
kubectl rollout restart daemonset -n kube-system tns-csi-node
```

## Testing Scenarios

### Test 1: Basic Volume Creation

```bash
kubectl apply -f test-pvc.yaml
kubectl get pvc test-pvc-nfs -w
```

### Test 2: Data Persistence

```bash
# Write data
kubectl exec test-nfs-pod -- sh -c "echo 'persistent data' > /data/persistent.txt"

# Delete pod
kubectl delete pod test-nfs-pod

# Recreate pod
kubectl apply -f test-pvc.yaml

# Verify data
kubectl exec test-nfs-pod -- cat /data/persistent.txt
```

### Test 3: Multiple Volumes

```bash
for i in {1..3}; do
  kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc-$i
spec:
  accessModes:
    - ReadWriteMany
  storageClassName: tns-nfs
  resources:
    requests:
      storage: 500Mi
EOF
done

kubectl get pvc
```

## Cleanup

### Delete test resources:

```bash
kubectl delete pvc test-pvc
kubectl delete pod test-pod
```

### Uninstall CSI driver:

For Helm installations:
```bash
helm uninstall tns-csi -n kube-system
```

For manual/script deployments:
```bash
kubectl delete -f deploy/storageclass.yaml
kubectl delete -f deploy/node.yaml
kubectl delete -f deploy/controller.yaml
kubectl delete -f deploy/csidriver.yaml
kubectl delete -f deploy/rbac.yaml
kubectl delete secret tns-csi-secret -n kube-system
```

### Delete Kind cluster:

```bash
kind delete cluster --name truenas-csi-test
```

## Network Considerations for Kind

### Accessing TrueNAS from Kind

Kind containers run in Docker's network. Ensure Kind can reach your TrueNAS server:

1. **If TrueNAS is on your local network:**
   - Use the actual IP address (e.g., YOUR-TRUENAS-IP)
   - Docker should be able to route to it

2. **If TrueNAS is on localhost:**
   - Use host.docker.internal instead of 127.0.0.1
   - Or use the host's network IP

3. **Test connectivity from Kind:**
   ```bash
   kubectl run -it --rm test --image=alpine --restart=Never -- sh
   # Inside the pod:
   ping -c 3 YOUR-TRUENAS-IP
   nc -zv YOUR-TRUENAS-IP 2049  # Test NFS port
   ```

## Performance Notes

Kind is suitable for:
- Development and testing
- CI/CD pipelines
- Feature validation
- Bug reproduction

Kind is NOT suitable for:
- Production workloads
- Performance benchmarking
- Load testing

For production testing, use a real Kubernetes cluster (EKS, GKE, AKS, or on-premises).

## Next Steps

After successful Kind testing:

1. Test on a real Kubernetes cluster
2. Implement volume snapshots
3. Add volume expansion support
4. Test with StatefulSets
5. Load testing with multiple concurrent volumes
