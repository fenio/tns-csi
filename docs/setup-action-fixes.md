# Fixes Required for setup-* Actions

Based on testing in distro-compatibility workflow, here are the required fixes for each setup action:

## 1. setup-kubesolo (@v1)

**Issue**: Hostname validation fails due to uppercase letters in GitHub runner hostnames
- Runner hostnames: `garm-nPezRaiwISWe` (contains uppercase)
- KubeSolo error: `Hostname 'garm-nPezRaiwISWe' contains uppercase letters. RFC 1123 requires lowercase only.`

**Root Cause**: KubeSolo installer has strict RFC 1123 hostname validation

**Fix Option A** (Recommended): Add input parameter to disable hostname check

Add to `action.yml` inputs:
```yaml
inputs:
  skip-hostname-check:
    description: 'Skip RFC 1123 hostname validation (useful for CI environments)'
    required: false
    default: 'false'
```

Then pass to installer:
```bash
if [ "${{ inputs.skip-hostname-check }}" = "true" ]; then
  curl -sfL https://get.kubesolo.io | SKIP_HOSTNAME_CHECK=1 sudo sh -
else
  curl -sfL https://get.kubesolo.io | sudo sh -
fi
```

**Note**: This requires updating the KubeSolo installer script to support `SKIP_HOSTNAME_CHECK` environment variable.

**Fix Option B**: Normalize hostname before installation

Add this step before KubeSolo installation:

```yaml
- name: Set lowercase hostname for RFC 1123 compliance
  run: |
    CURRENT_HOSTNAME=$(hostname)
    LOWERCASE_HOSTNAME=$(echo "$CURRENT_HOSTNAME" | tr '[:upper:]' '[:lower:]')
    if [ "$CURRENT_HOSTNAME" != "$LOWERCASE_HOSTNAME" ]; then
      echo "Changing hostname from '$CURRENT_HOSTNAME' to '$LOWERCASE_HOSTNAME' for RFC 1123 compliance"
      sudo hostnamectl set-hostname "$LOWERCASE_HOSTNAME"
      # Update /etc/hosts
      sudo sed -i "s/$CURRENT_HOSTNAME/$LOWERCASE_HOSTNAME/g" /etc/hosts
      echo "✓ Hostname updated to $LOWERCASE_HOSTNAME"
    fi
```

**Recommended**: Use Option A if you control the KubeSolo installer, otherwise use Option B.

---

## 2. setup-k0s (@v1)

**Issue**: Missing `kubectl` binary when action tries to verify cluster readiness
```
Error: Unable to locate executable file: kubectl
```

**Root Cause**: The action's TypeScript code is trying to run `kubectl` commands but:
- kubectl is NOT pre-installed on the GitHub runner
- k0s itself bundles kubectl functionality but doesn't expose it as standalone binary by default
- The action needs to either use `k0s kubectl` or install kubectl separately

**Fix Required**: Use k0s's built-in kubectl functionality

The action's wait-for-ready logic should use `k0s kubectl` instead of `kubectl`:

**Option A** (Recommended): Use k0s kubectl wrapper

In the action's TypeScript code, change kubectl invocations to:
```typescript
// Instead of:
await exec.exec('kubectl', ['get', 'nodes'])

// Use:
await exec.exec('k0s', ['kubectl', 'get', 'nodes'])
```

**Option B**: Create kubectl symlink

Add this step in action.yml after k0s starts:
```yaml
- name: Setup kubectl symlink
  run: |
    echo "Creating kubectl symlink from k0s..."
    sudo ln -sf /usr/local/bin/k0s /usr/local/bin/kubectl
    echo "✓ kubectl available via k0s"
```

**Option C**: Add kubectl as standalone dependency

Add to the action's dependencies:
```yaml
- name: Install kubectl
  run: |
    K0S_VERSION=$(k0s version | cut -d'+' -f1)
    curl -LO "https://dl.k8s.io/release/${K0S_VERSION}/bin/linux/amd64/kubectl"
    sudo install kubectl /usr/local/bin/kubectl
    rm kubectl
    kubectl version --client
```

**Recommended**: Option A (use k0s kubectl) is cleanest and doesn't require external dependencies.

---

## 3. setup-minikube (@v1)

**Issue**: Curl fails with exit code 22 (HTTP error)
```
Error: Failed to install Minikube: Error: The process '/usr/bin/curl' failed with exit code 22
```
- Exit code 22 = HTTP error (404, 403, etc.)

**Root Cause**: The action is likely using GitHub releases URL which may fail, but Google Cloud Storage mirror is more reliable

**Fix Required**: Use the pattern you identified - prioritize storage.googleapis.com

Update the download logic in action:

```yaml
- name: Download Minikube
  run: |
    VERSION="${{ inputs.version }}"
    PLATFORM="linux"
    BINARY_ARCH="amd64"  # or detect from arch
    
    # Construct download URL
    if [ "$VERSION" = "latest" ] || [ "$VERSION" = "stable" ]; then
      DOWNLOAD_URL="https://storage.googleapis.com/minikube/releases/latest/minikube-${PLATFORM}-${BINARY_ARCH}"
    else
      # For specific versions, use storage.googleapis.com first
      DOWNLOAD_URL="https://storage.googleapis.com/minikube/releases/${VERSION}/minikube-${PLATFORM}-${BINARY_ARCH}"
    fi
    
    echo "Downloading from: $DOWNLOAD_URL"
    
    # Try Google Cloud Storage first (more reliable)
    if ! curl -fsSL "$DOWNLOAD_URL" -o /tmp/minikube; then
      echo "Google Cloud Storage download failed, trying GitHub releases..."
      # Fallback to GitHub releases
      if [ "$VERSION" = "latest" ] || [ "$VERSION" = "stable" ]; then
        # Get latest version tag from GitHub API
        VERSION=$(curl -sL https://api.github.com/repos/kubernetes/minikube/releases/latest | grep '"tag_name"' | cut -d'"' -f4)
      fi
      DOWNLOAD_URL="https://github.com/kubernetes/minikube/releases/download/${VERSION}/minikube-${PLATFORM}-${BINARY_ARCH}"
      echo "Trying fallback URL: $DOWNLOAD_URL"
      curl -fsSL "$DOWNLOAD_URL" -o /tmp/minikube
    fi
    
    sudo install /tmp/minikube /usr/local/bin/minikube
    rm /tmp/minikube
    
    minikube version
    echo "✓ Minikube installed successfully"
```

**Key Changes**:
1. Use storage.googleapis.com as primary (more reliable)
2. Add fallback to GitHub releases
3. Better error messages showing which URL is being tried
4. Handle both `latest` and specific version patterns

---

## 4. setup-k3s (@v2)

**Status**: ✅ Working correctly - both NFS and NVMe-oF tests passed

No changes needed.

---

## Summary of Changes by Repository

### fenio/setup-kubesolo
**Issue**: Hostname validation rejects uppercase letters
**Options**:
- **A (Best)**: Add `skip-hostname-check` input parameter and support it in KubeSolo installer
- **B (Workaround)**: Force lowercase hostname in action before installation

**Recommendation**: Implement Option A if you control the KubeSolo installer script. This is cleaner than modifying hostnames which may have side effects.

### fenio/setup-k0s
**Issue**: Action tries to run `kubectl` which doesn't exist on runner
**Options**:
- **A (Best)**: Update TypeScript code to use `k0s kubectl` instead of `kubectl`
- **B (Quick)**: Create symlink: `ln -sf /usr/local/bin/k0s /usr/local/bin/kubectl`
- **C (Heavy)**: Download and install kubectl separately

**Recommendation**: Option A - use k0s's built-in kubectl. No external dependencies needed.

### fenio/setup-minikube
**Issue**: Download fails from GitHub releases (curl exit 22)
**Fix**: Use storage.googleapis.com as primary download source with GitHub releases as fallback

**Recommendation**: Follow the pattern you showed - prioritize Google Cloud Storage which is more reliable.

### fenio/setup-k3s
**Status**: ✅ Working perfectly - no changes needed

K3s setup action creates kubectl symlink automatically during installation.

---

## Testing After Fixes

Once these fixes are implemented, re-run the distro-compatibility workflow to verify:

```bash
gh workflow run distro-compatibility.yml --ref main -f distro=all -f protocol=both
```

Expected result: All 8 tests (4 distros × 2 protocols) should pass.
