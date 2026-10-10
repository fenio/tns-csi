#!/bin/bash
# Ratchet on //nolint directives: the per-linter count may never grow, and when it
# shrinks the budget file must be lowered in the same change.
#
# Usage: scripts/ci/nolint-budget.sh [--root DIR] [--budget FILE] [--update]
#   --root    repository root to scan (default: repo root of this script)
#   --budget  budget file of "linter count" lines (default: <root>/nolint-budget.txt)
#   --update  rewrite the budget file from the current counts and exit 0
#
# Counting: each linter named in a directive counts once ("//nolint:errcheck,gosec"
# adds one to errcheck and one to gosec); a bare "//nolint" counts as "all".
# Scope: Go files under pkg/ and cmd/, including tests.

set -euo pipefail
# sort and join must agree on collation.
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BUDGET=""
UPDATE=0
while [ $# -gt 0 ]; do
    case "$1" in
        --root) ROOT=$2; shift 2 ;;
        --budget) BUDGET=$2; shift 2 ;;
        --update) UPDATE=1; shift ;;
        *) echo "unknown argument: $1" >&2; exit 2 ;;
    esac
done
BUDGET=${BUDGET:-${ROOT}/nolint-budget.txt}

current_counts() {
    local dirs=()
    for d in pkg cmd; do [ -d "${ROOT}/${d}" ] && dirs+=("${ROOT}/${d}"); done
    [ ${#dirs[@]} -eq 0 ] && return 0
    grep -rhoE --include='*.go' '//nolint(:[A-Za-z0-9_,-]+)?' "${dirs[@]}" \
        | sed -E 's#^//nolint:?##' \
        | awk '{ if ($0 == "") { print "all" } else { n = split($0, l, ","); for (i = 1; i <= n; i++) if (l[i] != "") print l[i] } }' \
        | sort | uniq -c | awk '{ print $2, $1 }' | sort
}

CURRENT=$(current_counts || true)

if [ "${UPDATE}" -eq 1 ]; then
    printf '%s\n' "${CURRENT}" > "${BUDGET}"
    echo "Wrote ${BUDGET}"
    exit 0
fi

if [ ! -f "${BUDGET}" ]; then
    echo "Budget file ${BUDGET} not found; create it with: $0 --update" >&2
    exit 1
fi

status=0
# Join current and budget on linter name: "linter current budget" (missing side = -1).
while read -r linter cur bud; do
    if [ "${bud}" -lt 0 ]; then
        echo "NEW   ${linter}: ${cur} //nolint directive(s), no budget entry. Fix the code instead of suppressing."
        status=1
    elif [ "${cur}" -gt "${bud}" ]; then
        echo "OVER  ${linter}: ${cur} > budget ${bud}. Fix the code instead of adding //nolint."
        status=1
    elif [ "${cur}" -lt "${bud}" ]; then
        echo "STALE ${linter}: ${cur} < budget ${bud}. Lower the budget (run: $0 --update) so it cannot creep back."
        status=1
    fi
done < <(join -a1 -a2 -e -1 -o 0,1.2,2.2 \
            <(printf '%s\n' "${CURRENT}" | sed '/^$/d') \
            <(sed '/^$/d' "${BUDGET}" | sort))

if [ "${status}" -eq 0 ]; then
    echo "nolint budget OK ($(printf '%s\n' "${CURRENT}" | awk '{ s += $2 } END { print s + 0 }') directive-linter pairs)"
fi
exit "${status}"
