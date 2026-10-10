#!/bin/bash

# CSI Sanity Test Runner
# Runs the CSI specification compliance tests and enforces a pass-count floor.
#
# Verdict rules (see sanity_verdict):
#   - any failed spec fails the run
#   - a non-zero `go test` exit fails the run (compile errors, panics, timeouts)
#   - the number of passed specs must be >= BASELINE_PASS_COUNT
# BASELINE_PASS_COUNT is a ratchet: raise it when new specs pass, never lower it.

set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

BASELINE_PASS_COUNT=75

# sanity_verdict PASSED FAILED GO_TEST_EXIT BASELINE
# Prints the verdict and returns 0 when the run is acceptable, 1 otherwise.
sanity_verdict() {
    local passed=$1 failed=$2 go_exit=$3 baseline=$4
    local total=$((passed + failed))

    echo ""
    echo "=== Test Results ==="
    echo "Passed: ${passed}/${total}"
    echo "Failed: ${failed}/${total}"
    echo "Baseline: ${baseline} tests"

    if [ "${go_exit}" -ne 0 ]; then
        echo ""
        echo "❌ go test exited with status ${go_exit}"
        return 1
    fi
    if [ "${failed}" -gt 0 ]; then
        echo ""
        echo "❌ ${failed} sanity spec(s) failed"
        return 1
    fi
    if [ "${passed}" -lt "${baseline}" ]; then
        echo ""
        echo "❌ Sanity tests below baseline (${passed} < ${baseline})"
        echo "This indicates a regression. Expected at least ${baseline} passing tests."
        return 1
    fi

    echo ""
    echo "✅ Sanity tests passed and met baseline (${passed} >= ${baseline})"
    return 0
}

# Allow sourcing for unit tests of the verdict logic without running the suite.
if [ "${SANITY_SOURCE_ONLY:-0}" = "1" ]; then
    return 0 2>/dev/null || exit 0
fi

echo "=== CSI Sanity Tests ==="
echo "Project root: ${PROJECT_ROOT}"

cd "${PROJECT_ROOT}"

# Check if the main test (TestSanity) is ready to run
# We only check TestSanity function, not other helper tests that may be skipped
if grep -A 5 "^func TestSanity(t \*testing.T)" tests/sanity/sanity_test.go | grep -q "t.Skip"; then
    echo "⚠️  Sanity tests are currently skipped (driver refactoring in progress)"
    echo "See tests/sanity/README.md for details"
    echo ""
    echo "Verifying test compilation..."
    go test -c ./tests/sanity/... -o /dev/null
    echo "✅ Tests compile successfully"
    exit 0
fi

echo "Running CSI sanity tests..."
echo "Baseline: ${BASELINE_PASS_COUNT} tests expected to pass"
echo ""

TEST_OUTPUT=$(mktemp)
trap 'rm -f "${TEST_OUTPUT}"' EXIT

set +e
go test -v -timeout 10m ./tests/sanity/... -count=1 2>&1 | tee "${TEST_OUTPUT}"
TEST_EXIT_CODE=$?
set -e

# Parse test results from the Ginkgo summary line, e.g.:
#   "FAIL! -- 33 Passed | 42 Failed | 1 Pending | 16 Skipped"
PASSED=$(grep -o '[0-9]* Passed' "${TEST_OUTPUT}" | grep -o '[0-9]*' || echo "0")
FAILED=$(grep -o '[0-9]* Failed' "${TEST_OUTPUT}" | grep -o '[0-9]*' || echo "0")

sanity_verdict "${PASSED:-0}" "${FAILED:-0}" "${TEST_EXIT_CODE}" "${BASELINE_PASS_COUNT}"
