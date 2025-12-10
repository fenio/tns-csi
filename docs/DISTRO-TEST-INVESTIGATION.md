# Distro Compatibility Test Results - Investigation Summary

**Test Run**: https://github.com/fenio/tns-csi/actions/runs/20107816004  
**Date**: 2025-12-10  
**Results**: 2/8 passed, 6/8 failed

---

## ✅ Working Tests (2/8)

### K3s
- ✅ **K3s + NFS**: Passed in 1m58s
- ✅ **K3s + NVMe-oF**: Passed in 2m11s

**Why it works**: K3s installer automatically creates kubectl symlink:
```
[INFO] Creating /usr/local/bin/kubectl symlink to k3s
```

---

## ❌ Failed Tests (6/8)

### 1. KubeSolo (both NFS and NVMe-oF)

**Error**:
```
❌ Error: Hostname 'garm-nPezRaiwISWe' contains uppercase letters. 
RFC 1123 requires lowercase only. Please change hostname to lowercase.
```

**Root Cause**: 
- GitHub self-hosted runners have hostnames with uppercase letters
- KubeSolo installer has strict RFC 1123 hostname validation
- Kubernetes node names must be lowercase

**Your Options**:

**Option A (Recommended)** - Add skip flag to KubeSolo installer:
```bash
# In kubesolo installer script, add:
if [ "${SKIP_HOSTNAME_CHECK}" = "1" ]; then
  echo "⚠️  Skipping hostname validation (SKIP_HOSTNAME_CHECK=1)"
else
  # existing hostname validation
fi
```

Then in setup-kubesolo action:
```yaml
inputs:
  skip-hostname-check:
    description: 'Skip RFC 1123 hostname validation (for CI)'
    required: false
    default: 'false'
```

**Option B** - Force lowercase hostname in action:
```bash
LOWERCASE=$(hostname | tr '[:upper:]' '[:lower:]')
sudo hostnamectl set-hostname "$LOWERCASE"
```

**Recommendation**: Option A is cleaner. Option B might have side effects.

---

### 2. K0s (both NFS and NVMe-oF)

**Error**:
```
❌ Failed waiting for cluster: Error: Unable to locate executable file: kubectl
```

**Root Cause**:
- setup-k0s action's TypeScript code tries to run `kubectl` commands
- kubectl is NOT pre-installed on GitHub runners
- k0s bundles kubectl but doesn't expose it as standalone binary

**Your Options**:

**Option A (Recommended)** - Use k0s kubectl in TypeScript:
```typescript
// In setup-k0s action src code:
// Instead of:
await exec.exec('kubectl', ['get', 'nodes'])

// Use:
await exec.exec('k0s', ['kubectl', 'get', 'nodes'])
```

**Option B** - Create kubectl symlink:
```bash
sudo ln -sf /usr/local/bin/k0s /usr/local/bin/kubectl
```

**Option C** - Install kubectl separately (heavy):
```bash
K0S_VERSION=$(k0s version | cut -d'+' -f1)
curl -LO "https://dl.k8s.io/release/${K0S_VERSION}/bin/linux/amd64/kubectl"
sudo install kubectl /usr/local/bin/kubectl
```

**Recommendation**: Option A - cleanest, no dependencies. K0s already has kubectl built-in.

---

### 3. Minikube (both NFS and NVMe-oF)

**Error**:
```
❌ Failed to install Minikube: Error: The process '/usr/bin/curl' failed with exit code 22
```

**Root Cause**:
- Curl exit code 22 = HTTP error (404, 403, or similar)
- Likely trying GitHub releases URL which may be unreliable or rate-limited
- Google Cloud Storage mirror is more reliable

**Your Fix** (you already identified this!):
```bash
# Use storage.googleapis.com as primary
if [ "$VERSION" = "latest" ] || [ "$VERSION" = "stable" ]; then
  DOWNLOAD_URL="https://storage.googleapis.com/minikube/releases/latest/minikube-linux-amd64"
else
  DOWNLOAD_URL="https://storage.googleapis.com/minikube/releases/${VERSION}/minikube-linux-amd64"
fi

# Fallback to GitHub if needed
if ! curl -fsSL "$DOWNLOAD_URL" -o /tmp/minikube; then
  DOWNLOAD_URL="https://github.com/kubernetes/minikube/releases/download/${VERSION}/minikube-linux-amd64"
  curl -fsSL "$DOWNLOAD_URL" -o /tmp/minikube
fi
```

**Recommendation**: Implement the storage.googleapis.com pattern you described.

---

## 🎯 Action Items by Repository

### 1. fenio/setup-kubesolo
- [ ] Add `skip-hostname-check` input to action.yml
- [ ] Update KubeSolo installer to support `SKIP_HOSTNAME_CHECK` env var
- **OR** add hostname lowercasing step in action

### 2. fenio/setup-k0s  
- [ ] Update TypeScript code to use `k0s kubectl` instead of `kubectl`
- **OR** create kubectl symlink before validation steps

### 3. fenio/setup-minikube
- [ ] Change primary download source to storage.googleapis.com
- [ ] Add GitHub releases as fallback
- [ ] Better error messages showing attempted URLs

### 4. fenio/setup-k3s
- [x] Already perfect! No changes needed.

---

## 🧪 Testing After Fixes

Once fixes are implemented, run:
```bash
gh workflow run distro-compatibility.yml \
  --ref main \
  -f distro=all \
  -f protocol=both
```

**Expected Result**: All 8 tests pass (4 distros × 2 protocols)

---

## 📝 Key Insights

1. **K3s works perfectly** because its installer handles everything automatically
2. **GitHub runners don't have kubectl** pre-installed - each action must provide it
3. **Hostname validation** is a real issue for CI environments with generated hostnames
4. **Google Cloud Storage** is more reliable than GitHub releases for binaries

---

## 💡 Recommendations Priority

### High Priority (blocks all tests)
1. **setup-k0s kubectl issue** - affects both NFS and NVMe-oF tests
2. **setup-minikube download** - affects both NFS and NVMe-oF tests

### Medium Priority (workaround available)
3. **setup-kubesolo hostname** - can manually set hostname but better to fix in installer

Fix these and you'll go from 2/8 passing to 8/8 passing! 🎉
