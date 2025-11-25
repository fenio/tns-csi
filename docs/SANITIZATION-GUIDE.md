# Security Sanitization Guide for Contributors

Quick reference for removing sensitive information before sharing logs, configs, or screenshots.

## What to Sanitize

### Always Remove

✅ **Credentials:**
- API keys
- Passwords
- Tokens
- SSH keys
- Certificates (private keys)

✅ **Network Information:**
- Internal IP addresses (192.168.x.x, 10.x.x.x, 172.16-31.x.x)
- Hostnames
- Domain names (if not public)
- MAC addresses

✅ **System Information:**
- Usernames (if not public)
- Email addresses
- Server names
- Full paths that reveal system structure

### What's OK to Share

❌ **Safe to include:**
- Public IP addresses (usually)
- Kubernetes service IPs (ClusterIP, they're internal)
- Log levels and timestamps
- Error messages (after sanitizing)
- Version numbers
- Generic paths (/usr/bin, /etc, etc.)

## Common Sanitization Patterns

### 1. Kubernetes Logs

**Before:**
```
I1125 10:30:15 Connecting to wss://truenas.home.local:443/api/current
I1125 10:30:15 Using API key: 1-6a8b9c1d2e3f4g5h6i7j8k9l0m1n2o3p4q5r6s7t8u9v0w1x2y3z4a5b6c7d8e9f0
```

**After:**
```
I1125 10:30:15 Connecting to wss://TRUENAS_HOST:443/api/current
I1125 10:30:15 Using API key: [REDACTED]
```

### 2. YAML Configuration Files

**Before:**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: tns-csi-secret
data:
  api-key: MS02YThiOWMxZDJlM2Y0ZzVoNmk3ajhrOWwwbTFuMm8zcDRxNXI2czd0OHU5djB3MXgyeTN6NGE1YjZjN2Q4ZTlmMA==
  url: d3NzOi8vdHJ1ZW5hcy5ob21lLmxvY2FsOjQ0My9hcGkvY3VycmVudA==
```

**After:**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: tns-csi-secret
data:
  api-key: W1JFREFDVEVEXQ==  # Base64("[REDACTED]")
  url: W1JFREFDVEVEXQ==
```

### 3. Shell Commands

**Before:**
```bash
$ kubectl create secret generic tns-csi-secret \
    --from-literal=api-key=1-abc123def456 \
    --from-literal=url=wss://10.0.0.50:443/api/current
```

**After:**
```bash
$ kubectl create secret generic tns-csi-secret \
    --from-literal=api-key=YOUR_API_KEY \
    --from-literal=url=wss://YOUR_TRUENAS_IP:443/api/current
```

## Automated Sanitization Script

```bash
#!/bin/bash
# Save as scripts/sanitize-log.sh

if [ -z "$1" ]; then
    echo "Usage: $0 <log-file>"
    exit 1
fi

sed -E \
    -e 's/[0-9]+-[a-zA-Z0-9]{64}/[API_KEY_REDACTED]/g' \
    -e 's/192\.168\.[0-9]+\.[0-9]+/[INTERNAL_IP]/g' \
    -e 's/10\.[0-9]+\.[0-9]+\.[0-9]+/[INTERNAL_IP]/g' \
    -e 's/172\.(1[6-9]|2[0-9]|3[0-1])\.[0-9]+\.[0-9]+/[INTERNAL_IP]/g' \
    -e 's/wss:\/\/[a-zA-Z0-9.-]+/wss:\/\/[TRUENAS_HOST]/g' \
    -e 's/\/mnt\/[^\/]+\/[^\ ]+/\/mnt\/[POOL]\/[PATH]/g' \
    "$1"
```

Usage:
```bash
# Sanitize controller logs before sharing
kubectl logs -n kube-system tns-csi-controller-xyz | ./scripts/sanitize-log.sh
```

## Quick Checklist

Before sharing any log, config, or screenshot:

- [ ] Replaced all API keys with `[REDACTED]`
- [ ] Replaced internal IPs with placeholders
- [ ] Replaced hostnames with generic names
- [ ] Removed or replaced usernames
- [ ] Removed email addresses
- [ ] Checked for passwords in plain text
- [ ] Reviewed file paths for sensitive info
- [ ] Checked URLs for sensitive parameters

## Remember

**When in doubt, redact it out!**

It's better to over-sanitize than to leak sensitive information.

---

For complete sanitization history of this repository, see `SECURITY-SANITIZATION.md`.
