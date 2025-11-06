# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| 0.0.x   | :white_check_mark: |

## Reporting a Vulnerability

If you discover a security vulnerability in the TNS CSI Driver, please report it responsibly:

1. **DO NOT** open a public GitHub issue
2. Email the maintainer directly with details
3. Include:
   - Description of the vulnerability
   - Steps to reproduce
   - Potential impact
   - Suggested fix (if available)

We will acknowledge your report within 48 hours and provide a timeline for a fix.

## Security Considerations

### Credential Management

**API Keys and Secrets:**
- TrueNAS API keys are stored in Kubernetes Secrets
- Secrets should use RBAC to restrict access
- Never commit credentials to git
- Use `.local.yaml` files for local development (automatically ignored by git)

**Best Practices:**
- Use dedicated API keys with minimal required permissions
- Rotate API keys regularly
- Enable TrueNAS API audit logging
- Monitor API key usage

### Network Security

**TLS/SSL:**
- Always use `wss://` (WebSocket Secure) for TrueNAS API connections
- Verify TLS certificates in production
- For self-signed certificates, understand the security implications

**Network Isolation:**
- Restrict network access to TrueNAS management interface
- Use firewalls to limit access to storage ports:
  - NFS: TCP 2049
  - NVMe-oF: TCP 4420
- Consider VPN or private networks for production deployments

**Self-Hosted Runners:**
- Use private GitHub repositories to prevent malicious PR execution
- Isolate runners on dedicated network segments
- Use Wireguard VPN for secure communication with TrueNAS
- Regularly update runner systems and dependencies

### Kubernetes Security

**RBAC:**
- The CSI driver requires cluster-wide permissions
- Review RBAC manifests in `deploy/rbac.yaml`
- Limit access to CSI driver namespace

**Pod Security:**
- Driver pods run as privileged (required for mounting)
- Node pods need access to host filesystem
- Review security contexts in deployment manifests

**Secrets:**
```bash
# Create secret with proper permissions
kubectl create secret generic truenas-csi-secret \
  --from-literal=api-key=YOUR_API_KEY \
  --from-literal=api-url=wss://YOUR-TRUENAS-IP:443/api/current \
  --namespace kube-system

# Restrict access
kubectl create role secret-reader \
  --verb=get \
  --resource=secrets \
  --resource-name=truenas-csi-secret \
  --namespace kube-system
```

### Data Security

**Volume Data:**
- Data in volumes is subject to TrueNAS permissions and encryption
- Use TrueNAS dataset encryption for sensitive data
- Implement backup strategies
- Consider volume encryption at application level for additional security

**Access Modes:**
- Use ReadWriteOnce (RWO) when possible to limit access
- ReadWriteMany (RWX) should be used only when necessary
- Review application requirements for appropriate access modes

### Supply Chain Security

**Container Images:**
- Images are built from this source code
- Verify image signatures before deployment
- Use specific version tags, not `latest`
- Scan images for vulnerabilities

**Dependencies:**
- Review `go.mod` for dependency versions
- Run `go mod tidy` and `go mod verify`
- Update dependencies regularly
- Monitor for security advisories

### Audit and Monitoring

**Logging:**
- Enable detailed logging in development/staging
- Monitor CSI driver logs for unusual activity:
  ```bash
  kubectl logs -n kube-system -l app.kubernetes.io/name=tns-csi-driver
  ```

**TrueNAS Audit:**
- Enable TrueNAS API audit logging
- Review logs for unauthorized access attempts
- Monitor dataset access patterns

**Kubernetes Events:**
```bash
# Watch CSI events
kubectl get events -n kube-system --field-selector involvedObject.kind=Pod

# Check PVC/PV events
kubectl describe pvc <pvc-name>
```

## Known Security Limitations

### Current Limitations

1. **API Key Storage**: API keys are stored in Kubernetes Secrets (base64 encoded, not encrypted by default)
   - **Mitigation**: Enable Kubernetes encryption at rest
   - **Alternative**: Use external secret management (Vault, etc.)

2. **Privileged Pods**: Node pods run with elevated privileges
   - **Reason**: Required for volume mounting operations
   - **Mitigation**: Carefully review RBAC and pod security policies

3. **TLS Certificate Verification**: May be disabled for self-signed certificates
   - **Risk**: Potential for man-in-the-middle attacks
   - **Mitigation**: Use proper CA-signed certificates in production

### Future Improvements

- Support for external secret managers (Vault, etc.)
- Mutual TLS authentication
- Enhanced audit logging
- CSI driver pod security improvements

## Security Updates

Security updates will be released as soon as possible after verification. Subscribe to repository releases to be notified of security patches.

## Compliance

This driver does not currently undergo formal security audits or compliance certifications. Use in production environments should include your own security assessment.

## Additional Resources

- [Kubernetes Secrets Management](https://kubernetes.io/docs/concepts/configuration/secret/)
- [CSI Driver Security Considerations](https://kubernetes-csi.github.io/docs/)
- [TrueNAS Security Best Practices](https://www.truenas.com/docs/)
