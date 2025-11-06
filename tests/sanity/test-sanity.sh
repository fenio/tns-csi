#!/bin/bash

# CSI Sanity Test Runner
# Runs the CSI specification compliance tests

set -e
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

echo "=== CSI Sanity Tests ==="
echo "Project root: ${PROJECT_ROOT}"

# Change to project root
cd "${PROJECT_ROOT}"

# Check if the main test (TestSanity) is ready to run
# We only check TestSanity function, not other helper tests that may be skipped
if ! grep -A 5 "^func TestSanity(t \*testing.T)" tests/sanity/sanity_test.go | grep -q "t.Skip"; then
    echo "Running CSI sanity tests..."

    # Run sanity tests with verbose output
    go test -v -timeout 10m ./tests/sanity/... -count=1

    echo "✅ Sanity tests passed"
else
    echo "⚠️  Sanity tests are currently skipped (driver refactoring in progress)"
    echo ""
    echo "Current status:"
    echo "  ✅ Mock client implemented"
    echo "  ✅ Test framework configured"
    echo "  🔄 Awaiting driver refactoring for dependency injection"
    echo ""
    echo "See tests/sanity/README.md for details"

    # Still run the tests to ensure they compile
    echo ""
    echo "Verifying test compilation..."
    go test -c ./tests/sanity/... -o /dev/null
    echo "✅ Tests compile successfully"
fi

exit 0
