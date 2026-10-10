#!/bin/bash
# Tests for scripts/ci/nolint-budget.sh. Run: bash scripts/ci/nolint-budget_test.sh
set -u

SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/nolint-budget.sh"
WORK=$(mktemp -d)
trap 'rm -rf "${WORK}"' EXIT
failures=0

# fixture <dir>: two Go files exercising single, multi-linter and bare directives.
fixture() {
    mkdir -p "$1/pkg/a" "$1/cmd/b"
    cat > "$1/pkg/a/a.go" <<'EOF'
package a

func f() {
	_ = g() //nolint:errcheck // reason
	_ = g() //nolint:errcheck,gosec // reason
}

//nolint // legacy blanket directive
func g() error { return nil }
EOF
    cat > "$1/cmd/b/b.go" <<'EOF'
package b

var x = 1 //nolint:gosec // reason
// This comment mentions nolint without a directive and must not count.
EOF
}

# expect <expected_exit> <description> -- <args...>
expect() {
    local want=$1 desc=$2
    shift 3
    bash "${SCRIPT}" "$@" >"${WORK}/out.txt" 2>&1
    local got=$?
    if [ "${got}" -eq "${want}" ]; then
        echo "PASS: ${desc}"
    else
        echo "FAIL: ${desc} (expected exit ${want}, got ${got})"
        sed 's/^/    /' "${WORK}/out.txt"
        failures=$((failures + 1))
    fi
}

ROOT="${WORK}/repo"
fixture "${ROOT}"
B="${WORK}/budget.txt"

printf 'all 1\nerrcheck 2\ngosec 2\n' > "${B}"
expect 0 "counts equal budget (multi-linter directive counts once per linter; bare counts as all)" -- --root "${ROOT}" --budget "${B}"

printf 'all 1\nerrcheck 1\ngosec 2\n' > "${B}"
expect 1 "a linter over budget fails" -- --root "${ROOT}" --budget "${B}"

printf 'all 1\nerrcheck 2\n' > "${B}"
expect 1 "a linter missing from the budget fails" -- --root "${ROOT}" --budget "${B}"

printf 'all 1\nerrcheck 3\ngosec 2\n' > "${B}"
expect 1 "a stale budget (count dropped) fails so the ratchet tightens" -- --root "${ROOT}" --budget "${B}"

printf 'all 1\nerrcheck 2\ngosec 2\nunused 1\n' > "${B}"
expect 1 "a budget entry for a linter with no directives left fails as stale" -- --root "${ROOT}" --budget "${B}"

rm -f "${B}"
expect 0 "--update writes the budget" -- --root "${ROOT}" --budget "${B}" --update
expect 0 "the written budget then passes" -- --root "${ROOT}" --budget "${B}"
if [ "$(cat "${B}")" != "$(printf 'all 1\nerrcheck 2\ngosec 2')" ]; then
    echo "FAIL: --update content mismatch:"; sed 's/^/    /' "${B}"; failures=$((failures + 1))
else
    echo "PASS: --update writes sorted 'linter count' lines"
fi

if [ "${failures}" -ne 0 ]; then
    echo "${failures} test(s) failed"
    exit 1
fi
echo "all nolint-budget tests passed"
