# Security Incident Response Guide

## If Secrets Are Exposed

### Immediate Actions (Do These FIRST)

1. **Make repository private immediately:**
   - Go to Settings → General → Danger Zone
   - Click "Change repository visibility"
   - Select "Make private"

2. **Stop all runners:**
   ```bash
   # On runner machine(s)
   sudo systemctl stop actions.runner.*
   ```

3. **Rotate ALL potentially exposed credentials:**
   - TrueNAS API keys (revoke and create new)
   - GitHub personal access tokens
   - GitHub runner tokens
   - Any other secrets in GitHub Secrets

### Assessment

4. **Identify what was exposed:**
   ```bash
   # Check recent commits
   git log --all -p --since="7 days ago" | grep -i "password\|api.key\|secret\|token"
   
   # Check if it's in history
   git log --all --full-history -p -- path/to/file
   
   # Check all branches
   git branch -a
   ```

5. **Check for unauthorized access:**
   - Review TrueNAS access logs
   - Check GitHub Actions logs for suspicious activity
   - Review runner system logs
   - Check TrueNAS for unexpected datasets/changes

### Cleanup

6. **Remove secrets from git history:**

   **Option A: Using git-filter-repo (recommended)**
   ```bash
   # Install git-filter-repo
   pip3 install git-filter-repo
   
   # Remove a specific file
   git filter-repo --path secret-file.yaml --invert-paths
   
   # Remove secrets matching pattern
   git filter-repo --replace-text <(echo "regex:api-key.*===>[REDACTED]")
   ```

   **Option B: Using BFG Repo-Cleaner**
   ```bash
   # Download BFG
   wget https://repo1.maven.org/maven2/com/madgag/bfg/1.14.0/bfg-1.14.0.jar
   
   # Remove secrets
   java -jar bfg-1.14.0.jar --replace-text replacements.txt
   
   # replacements.txt:
   # api-key-value-here==>REDACTED
   ```

   **Option C: Start fresh** (nuclear option)
   ```bash
   # Create new repo without history
   git checkout --orphan fresh-start
   git add .
   git commit -m "Initial commit - fresh start"
   git branch -D main
   git branch -m main
   git push -f origin main
   ```

7. **Force push cleaned history:**
   ```bash
   git push --force --all origin
   git push --force --tags origin
   ```

8. **Update all clones:**
   ```bash
   # Notify all contributors to:
   git fetch origin
   git reset --hard origin/main
   ```

### Prevention

9. **Add pre-commit hooks:**
   ```bash
   # Already configured in .pre-commit-config.yaml
   pre-commit install
   ```

10. **Scan before going public again:**
    ```bash
    ./scripts/pre-public-check.sh
    ```

## If Self-Hosted Runner Is Compromised

### Immediate Actions

1. **Isolate the runner:**
   ```bash
   # Stop runner service
   sudo systemctl stop actions.runner.*
   
   # Disable networking (if possible)
   sudo iptables -A OUTPUT -j DROP
   
   # Or shut down VM/container
   sudo shutdown -h now
   ```

2. **Revoke runner access:**
   - GitHub: Settings → Actions → Runners
   - Click on compromised runner
   - Click "Remove" button

3. **Make repository private:**
   - Settings → General → Danger Zone
   - Change visibility to private

4. **Rotate all credentials:**
   - GitHub Secrets
   - TrueNAS API keys
   - Runner registration tokens

### Investigation

5. **Preserve evidence:**
   ```bash
   # If VM, take snapshot before investigation
   # If physical, create forensic image
   
   # Collect logs
   sudo journalctl -u actions.runner.* > runner-logs.txt
   sudo cp -r /var/log/syslog* /evidence/
   sudo cp -r /var/log/auth.log* /evidence/
   
   # Network connections
   sudo netstat -tunap > connections.txt
   sudo iptables -L -n -v > firewall-rules.txt
   
   # Process list
   ps auxf > processes.txt
   
   # File system changes (if you have baseline)
   sudo find /home/runner -type f -mtime -1 > recent-changes.txt
   ```

6. **Check for persistence mechanisms:**
   ```bash
   # Cron jobs
   sudo crontab -l -u runner
   
   # Systemd services
   sudo systemctl list-units --state=enabled
   
   # SSH keys
   cat ~/.ssh/authorized_keys
   
   # Startup scripts
   ls -la ~/.bashrc ~/.bash_profile /etc/rc.local
   ```

7. **Check TrueNAS for unauthorized changes:**
   - Review API audit logs
   - Check for unexpected datasets
   - Review NFS shares and permissions
   - Check for new API keys created

### Recovery

8. **Rebuild runner from scratch:**
   ```bash
   # DO NOT reuse old runner
   # Build fresh from base image
   # Apply security hardening
   # Install minimal packages
   ```

9. **Re-register runner:**
   ```bash
   # Generate new registration token in GitHub
   # Settings → Actions → Runners → Add runner
   
   ./config.sh --url https://github.com/yourorg/repo --token NEW_TOKEN
   ```

10. **Implement additional security controls:**
    - Network segmentation
    - Stricter firewall rules
    - Intrusion detection (fail2ban, OSSEC)
    - Log aggregation and monitoring

## If Malicious PR Was Merged

### Immediate Actions

1. **Revert the merge:**
   ```bash
   git revert -m 1 <merge-commit-hash>
   git push origin main
   ```

2. **Assess damage:**
   - Review what the malicious code did
   - Check if secrets were accessed
   - Review TrueNAS state
   - Check for backdoors in codebase

3. **Scan for additional malicious changes:**
   ```bash
   # Review all files in the PR
   git diff <merge-commit>^ <merge-commit>
   
   # Check for obfuscated code
   grep -r "eval\|exec\|base64" .
   
   # Look for network calls to suspicious domains
   grep -r "curl\|wget\|nc\|netcat" .
   ```

### Recovery

4. **Notify users if driver was released:**
   ```markdown
   # Create GitHub Security Advisory
   # Settings → Security → Advisories → New draft
   
   Title: Malicious code in version X.X.X
   Severity: Critical
   Description: Version X.X.X contained malicious code...
   Affected versions: vX.X.X
   Patched versions: vX.X.Y (after fix)
   ```

5. **Release patched version:**
   ```bash
   # Remove malicious code
   # Bump version
   # Create new release
   git tag -a vX.X.Y -m "Security fix: Remove malicious code"
   git push origin vX.X.Y
   ```

## If Repository Settings Were Changed

GitHub Actions can modify repository settings if permissions are too permissive.

### Check What Changed

1. **Review audit log:**
   - Settings → Logs → Audit log
   - Filter by Actions
   - Look for unexpected changes

2. **Verify critical settings:**
   - Branch protection rules
   - Deploy keys
   - Webhooks
   - Secrets

### Restore Correct Settings

3. **Re-apply branch protection:**
   - Settings → Branches
   - Add rule for `main`
   - Require PR reviews
   - Require status checks

4. **Review and remove unauthorized items:**
   - Deploy keys
   - Webhooks  
   - Actions secrets
   - Collaborators

## General Incident Response Checklist

- [ ] Contain the incident (stop runners, make repo private)
- [ ] Preserve evidence (logs, snapshots, file listings)
- [ ] Identify scope (what was exposed/compromised)
- [ ] Rotate ALL potentially affected credentials
- [ ] Investigate root cause
- [ ] Implement fixes
- [ ] Test thoroughly before going public again
- [ ] Document what happened and lessons learned
- [ ] Update security controls to prevent recurrence

## Post-Incident

### Documentation

Create incident report documenting:
- Timeline of events
- What was compromised
- Actions taken
- Root cause analysis
- Preventive measures implemented

### Lessons Learned

- What went wrong?
- What went right in the response?
- What should be changed?
- What additional monitoring is needed?

### Communication

If users were affected:
- Create GitHub Security Advisory
- Post to Discussions/Issues
- Update documentation
- Notify via any other channels (Reddit, Discord, etc.)

## Prevention is Better Than Cure

**Before going public:**
- [ ] Complete PRE-PUBLIC-CHECKLIST.md
- [ ] Configure fork PR approvals
- [ ] Test security controls
- [ ] Run `./scripts/pre-public-check.sh`
- [ ] Have incident response plan ready

**After going public:**
- [ ] Monitor workflow runs daily
- [ ] Review all PRs carefully before approval
- [ ] Keep runner system updated
- [ ] Regularly rotate secrets
- [ ] Test incident response procedures

## Emergency Contacts

Update these with your information:

- **Repository Owner:** [Your email]
- **Security Contact:** [Your security email]
- **TrueNAS Admin:** [TrueNAS admin contact]
- **Network Admin:** [Network admin contact]

## References

- [GitHub Security Best Practices](https://docs.github.com/en/code-security)
- [Self-Hosted Runner Security](https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/about-self-hosted-runners#self-hosted-runner-security)
- [git-filter-repo Documentation](https://github.com/newren/git-filter-repo)
