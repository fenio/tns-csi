# 🔒 Security Checklist for Making Repository Public

## ⚠️ CRITICAL: Self-Hosted Runner Security

**DANGER:** This repository uses self-hosted GitHub runners. Making the repo public creates a **CRITICAL SECURITY RISK**:

### The Problem
- Public repos allow anyone to fork and create pull requests
- By default, PR workflows run on your self-hosted infrastructure
- **Malicious actors can execute arbitrary code on your runner**
- They can access your network, TrueNAS server, and secrets

### Solutions (CHOOSE ONE BEFORE GOING PUBLIC)

#### Option 1: Require Manual Approval (RECOMMENDED)
Configure GitHub to require approval for all outside contributors:

1. Go to: **Settings → Actions → General**
2. Scroll to **Fork pull request workflows from outside collaborators**
3. Select: **"Require approval for first-time contributors who are new to GitHub"** or **"Require approval for all outside collaborators"**
4. ✅ This means you manually approve each PR before workflows run

**Pros:** Keeps existing workflow structure, you maintain control  
**Cons:** You must review every PR manually before CI runs

#### Option 2: Separate Public CI and Private Integration Tests
Create two workflow sets:

**Public CI (GitHub-hosted runners):**
- Lint
- Unit tests  
- Build verification
- Runs on all PRs safely

**Private Integration (self-hosted, main branch only):**
- Keep integration tests on self-hosted
- Only run on pushes to main (after PR merge)
- Never run on fork PRs

See `RUNNER_SECURITY_SETUP.md` for implementation details.

#### Option 3: Move to GitHub-Hosted Runners
Migrate all workflows to GitHub-hosted runners:
- Use `runs-on: ubuntu-latest` instead of `self-hosted`
- Skip TrueNAS integration tests in public CI
- Run full integration suite manually before releases

**Pros:** No security concerns  
**Cons:** Can't test against real TrueNAS in CI

---

## ✅ Pre-Public Checklist

### 1. Secrets and Credentials
- [x] No API keys or passwords in code
- [x] No secrets in git history
- [x] `.gitignore` properly configured
- [x] Example files use placeholders
- [x] GitHub Secrets configured for CI/CD
- [ ] Reviewed all values-*.yaml files for real credentials

### 2. GitHub Repository Settings

**Before making public, configure:**

#### General Settings
- [ ] Add repository description
- [ ] Add topics: `kubernetes`, `csi-driver`, `truenas`, `storage`, `nfs`, `nvmeof`
- [ ] Set default branch to `main`

#### Branch Protection (Settings → Branches → Add rule for `main`)
- [ ] Require pull request before merging
- [ ] Require at least 1 approval
- [ ] Require status checks to pass before merging:
  - [ ] `lint`
  - [ ] `test`
  - [ ] `sanity-test`
- [ ] Require conversation resolution before merging
- [ ] Require linear history (optional but recommended)
- [ ] Do not allow force pushes
- [ ] Do not allow deletions

#### Actions Settings (Settings → Actions → General)
- [ ] **CRITICAL:** Configure fork PR workflow approval (see above)
- [ ] Set workflow permissions to "Read repository contents" by default
- [ ] Require approval for workflow runs from outside collaborators

#### Security Settings
- [ ] Enable Dependabot security updates
- [ ] Enable Dependabot alerts  
- [ ] Enable GitHub security advisories
- [ ] Enable code scanning (CodeQL) if available
- [ ] Review "Secrets and variables" - ensure no sensitive data exposed

### 3. Documentation Review
- [x] README.md clearly states "early development, not production-ready"
- [x] SECURITY.md has correct contact information
- [x] SECURITY.md documents self-hosted runner risks
- [x] CONTRIBUTING.md has clear guidelines
- [x] LICENSE is appropriate (GPL-3.0)
- [ ] Update README with public repo URL (after going public)

### 4. Issue and PR Templates
- [x] Bug report template created
- [x] Feature request template created
- [x] Question template created
- [x] Pull request template created
- [x] CODEOWNERS file configured
- [x] Issue templates warn about not including secrets

### 5. CI/CD Configuration
- [ ] **CRITICAL:** Self-hosted runner security addressed (see above)
- [x] Dependabot configured
- [x] Workflows use GitHub Secrets (not hardcoded)
- [ ] Consider if any workflows should NOT run on forks
- [ ] Review workflow permissions (GITHUB_TOKEN)

### 6. Code Quality
- [ ] Run `make lint` - all checks pass
- [ ] Run `make test` - all tests pass
- [ ] Run security check: `./scripts/pre-public-check.sh`
- [ ] No obvious TODOs or FIXMEs that must be addressed first
- [ ] No embarrassing comments or debug code

### 7. Legal and Licensing
- [x] LICENSE file present and correct
- [x] Copyright notices where appropriate
- [ ] Review any dependencies for license compatibility
- [ ] Ensure no proprietary code included

### 8. Communication Channels
- [ ] Update SECURITY.md with real contact email for vulnerabilities
- [ ] Consider creating Discussions for Q&A
- [ ] Add communication preferences to README (Issues vs Discussions)

---

## 🚀 Going Public: Step-by-Step

When you're ready:

1. **Run final security check:**
   ```bash
   ./scripts/pre-public-check.sh
   ```

2. **Review recent commits** - make sure nothing sensitive was recently added

3. **Configure GitHub settings** (see checklist above)

4. **Make repository public:**
   - Go to: Settings → General → Danger Zone
   - Click "Change visibility"
   - Select "Make public"
   - Type repository name to confirm
   - Click "I understand, change repository visibility"

5. **Immediately verify:**
   - [ ] Repository is public
   - [ ] Actions tab is visible
   - [ ] Settings are correct (especially fork PR approvals)
   - [ ] No secrets visible in Actions logs

6. **Post-public tasks:**
   - [ ] Update README with correct GitHub URLs
   - [ ] Test forking workflow (fork from another account, open PR)
   - [ ] Verify PR approval workflow works
   - [ ] Enable GitHub Discussions if desired
   - [ ] Post announcement (Reddit, etc.)

---

## 🆘 If Something Goes Wrong

**If you accidentally expose secrets:**

1. **Immediately rotate all exposed credentials**
   - Change TrueNAS API keys
   - Rotate GitHub tokens
   - Update GitHub Secrets

2. **Make repository private again** (temporarily)

3. **Assess damage:**
   - Check access logs for unauthorized access
   - Review runner logs for suspicious activity
   - Check TrueNAS logs

4. **Clean git history if needed:**
   ```bash
   # Use git-filter-repo or BFG Repo-Cleaner
   # Consider starting fresh if needed
   ```

5. **Only go public again after all secrets rotated**

---

## 📚 Additional Resources

- [GitHub: Securing your repository](https://docs.github.com/en/code-security/getting-started/securing-your-repository)
- [GitHub: Self-hosted runner security](https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/about-self-hosted-runners#self-hosted-runner-security)
- [GitHub: Managing Actions permissions](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository)

---

## ✅ Sign-Off

Before making the repository public, check this box:

- [ ] I have read and completed this entire checklist
- [ ] I understand the self-hosted runner security implications
- [ ] I have configured fork PR approval requirements  
- [ ] All secrets and credentials have been verified
- [ ] Repository settings are properly configured

**Date:** _______________  
**Reviewed by:** _______________
