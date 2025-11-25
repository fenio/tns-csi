#!/bin/bash
# Pre-public security check script
# Run this before making the repository public

set -e

echo "🔒 Running security checks before going public..."

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

FAILED=0

# Check 1: Look for potential secrets in files
echo ""
echo "1️⃣  Checking for potential secrets in tracked files..."
if git ls-files | xargs grep -i -E "(password|secret|token).*=.*['\"][^'\"]{20,}['\"]" 2>/dev/null; then
    echo -e "${RED}❌ FAILED: Potential hardcoded secrets found!${NC}"
    FAILED=1
else
    echo -e "${GREEN}✅ PASSED: No obvious hardcoded secrets found${NC}"
fi

# Check 2: Verify .gitignore exists and has essential entries
echo ""
echo "2️⃣  Checking .gitignore..."
if [ ! -f .gitignore ]; then
    echo -e "${RED}❌ FAILED: .gitignore not found!${NC}"
    FAILED=1
else
    required_patterns=("*.key" "*.pem" "secret.local" "*.local.yaml")
    missing=()
    for pattern in "${required_patterns[@]}"; do
        if ! grep -q "$pattern" .gitignore; then
            missing+=("$pattern")
        fi
    done
    
    if [ ${#missing[@]} -gt 0 ]; then
        echo -e "${YELLOW}⚠️  WARNING: .gitignore missing patterns: ${missing[*]}${NC}"
    else
        echo -e "${GREEN}✅ PASSED: .gitignore looks good${NC}"
    fi
fi

# Check 3: Check for API keys in git history
echo ""
echo "3️⃣  Checking git history for API keys..."
if git log --all --full-history -p | grep -i -E "api-key.*[0-9]+-[a-zA-Z0-9]{64}" 2>/dev/null | head -1; then
    echo -e "${RED}❌ FAILED: API key pattern found in git history!${NC}"
    echo "   You may need to rewrite git history or start fresh."
    FAILED=1
else
    echo -e "${GREEN}✅ PASSED: No API keys in git history${NC}"
fi

# Check 4: Verify example files use placeholders
echo ""
echo "4️⃣  Checking example files for placeholders..."
example_files=("deploy/secret.yaml" "charts/tns-csi-driver/values.yaml")
for file in "${example_files[@]}"; do
    if [ -f "$file" ]; then
        if grep -q "YOUR-API-KEY-HERE\|your-api-key-here" "$file"; then
            echo -e "${GREEN}✅ $file uses placeholder${NC}"
        else
            echo -e "${YELLOW}⚠️  WARNING: $file may not use placeholder${NC}"
        fi
    fi
done

# Check 5: Verify required documentation exists
echo ""
echo "5️⃣  Checking required documentation..."
required_docs=("README.md" "SECURITY.md" "CONTRIBUTING.md" "LICENSE")
for doc in "${required_docs[@]}"; do
    if [ -f "$doc" ]; then
        echo -e "${GREEN}✅ $doc exists${NC}"
    else
        echo -e "${RED}❌ FAILED: $doc missing!${NC}"
        FAILED=1
    fi
done

# Check 6: Verify GitHub Actions don't expose secrets
echo ""
echo "6️⃣  Checking GitHub Actions workflows..."
if find .github/workflows -name "*.yml" -o -name "*.yaml" 2>/dev/null | xargs grep -i "secrets\." | grep -v "secrets\.[A-Z_]*" | grep -v "#" ; then
    echo -e "${YELLOW}⚠️  WARNING: Check that secrets are properly referenced${NC}"
else
    echo -e "${GREEN}✅ PASSED: GitHub Actions secrets look properly configured${NC}"
fi

# Check 7: Look for TODO or FIXME related to security
echo ""
echo "7️⃣  Checking for security-related TODOs..."
if git grep -i "TODO.*security\|FIXME.*security\|XXX.*security" 2>/dev/null; then
    echo -e "${YELLOW}⚠️  WARNING: Found security-related TODOs${NC}"
else
    echo -e "${GREEN}✅ No urgent security TODOs found${NC}"
fi

# Check 8: Verify self-hosted runner configuration
echo ""
echo "8️⃣  Checking for self-hosted runner references..."
if grep -r "runs-on:.*self-hosted" .github/workflows/ 2>/dev/null; then
    echo -e "${YELLOW}⚠️  WARNING: Self-hosted runners detected${NC}"
    echo "   Make sure you understand the security implications:"
    echo "   - Anyone with write access can run code on your runner"
    echo "   - Review SECURITY.md section on self-hosted runners"
else
    echo -e "${GREEN}✅ No self-hosted runners in workflows${NC}"
fi

# Final summary
echo ""
echo "=========================================="
if [ $FAILED -eq 1 ]; then
    echo -e "${RED}❌ SECURITY CHECK FAILED${NC}"
    echo "   Please fix the issues above before making the repository public."
    exit 1
else
    echo -e "${GREEN}✅ ALL CRITICAL CHECKS PASSED${NC}"
    echo ""
    echo "Additional steps before going public:"
    echo "  1. Configure GitHub repository settings (branch protection, etc.)"
    echo "  2. Double-check SECURITY.md contact information"
    echo "  3. Review all recent commits"
    echo "  4. Consider security implications of self-hosted runners"
    echo "  5. Set up GitHub security features (Dependabot, code scanning)"
    echo ""
    echo "When ready, change repository visibility in Settings -> Danger Zone"
fi
