---
name: Bug Report
about: Report a bug or issue with the CSI driver
title: '[BUG] '
labels: bug
assignees: ''
---

## Bug Description
A clear and concise description of what the bug is.

## Environment
**Kubernetes:**
- Distribution: [e.g., k3s, K8s, OpenShift]
- Version: [e.g., 1.28.0]

**TrueNAS:**
- Version: [e.g., TrueNAS SCALE 24.04]
- Storage pool type: [e.g., mirror, raidz2]

**CSI Driver:**
- Version: [e.g., v0.0.5]
- Protocol: [NFS or NVMe-oF]
- Deployment method: [Helm or kubectl]

## Steps to Reproduce
1. Create StorageClass with '...'
2. Create PVC with '...'
3. Create Pod mounting PVC
4. See error

## Expected Behavior
What you expected to happen.

## Actual Behavior
What actually happened.

## Logs

**Controller logs:**
```
kubectl logs -n kube-system -l app=tns-csi-controller
(paste logs here)
```

**Node logs:**
```
kubectl logs -n kube-system -l app=tns-csi-node
(paste logs here)
```

**PVC/PV status:**
```
kubectl describe pvc <pvc-name>
(paste output here)
```

## Configuration

**StorageClass:**
```yaml
(paste your StorageClass definition - REMOVE API keys!)
```

**PVC:**
```yaml
(paste your PVC definition)
```

## Additional Context
Any other context about the problem.

## ⚠️ Security Reminder
**DO NOT include API keys, passwords, or other secrets in this issue!**
Use placeholders like "REDACTED" for sensitive information.
