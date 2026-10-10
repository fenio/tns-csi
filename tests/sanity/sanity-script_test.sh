#!/bin/bash
# Unit tests for the verdict logic in test-sanity.sh.
# Run: bash tests/sanity/sanity-script_test.sh

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source only the function definitions (the script must not run tests when sourced).
# shellcheck source=tests/sanity/test-sanity.sh
SANITY_SOURCE_ONLY=1 source "${SCRIPT_DIR}/test-sanity.sh"

failures=0

# expect_verdict <expected_exit> <passed> <failed> <go_test_exit> <baseline> <description>
expect_verdict() {
    local expected=$1 passed=$2 failed=$3 go_exit=$4 baseline=$5 desc=$6
    local actual
    sanity_verdict "${passed}" "${failed}" "${go_exit}" "${baseline}" >/dev/null 2>&1
    actual=$?
    if [ "${actual}" -eq "${expected}" ]; then
        echo "PASS: ${desc}"
    else
        echo "FAIL: ${desc} (expected exit ${expected}, got ${actual})"
        failures=$((failures + 1))
    fi
}

expect_verdict 0 80 0 0 75 "all specs pass and meet baseline"
expect_verdict 1 80 1 1 75 "one failed spec fails the run even though baseline is met"
expect_verdict 1 80 0 1 75 "non-zero go test exit fails the run even with zero parsed failures"
expect_verdict 1 70 0 0 75 "pass count below baseline fails the run"
expect_verdict 0 75 0 0 75 "pass count exactly at baseline is accepted"

if [ "${failures}" -ne 0 ]; then
    echo "${failures} test(s) failed"
    exit 1
fi
echo "all sanity-script tests passed"
