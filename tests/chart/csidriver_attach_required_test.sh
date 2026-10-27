#!/bin/bash
# Chart tests for CSIDriver.spec.attachRequired (issue #291).
# Run: bash tests/chart/csidriver_attach_required_test.sh
#
# These cover what `helm template` can see: the rendered value for the default,
# for an explicit false, and for a release whose values predate the setting.
# `helm upgrade --reuse-values` renders the NEW templates against the OLD
# chart's values.yaml, so the key is simply absent; that is simulated here by
# rendering a copy of the chart with the key removed from values.yaml.
# The immutability precheck uses `lookup`, which needs a live cluster, and is
# covered by the e2e suite.

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART="${SCRIPT_DIR}/../../charts/tns-csi-driver"
TMPDIR_ROOT="$(mktemp -d)"
trap 'rm -rf "${TMPDIR_ROOT}"' EXIT

failures=0

# render_csidriver <chart dir> [helm args...]
render_csidriver() {
    local chart=$1
    shift
    helm template attach-test "${chart}" \
        --show-only templates/csidriver.yaml \
        --set truenas.url=wss://truenas.example/api/current \
        --set truenas.apiKey=test \
        --set 'storageClasses[0].server=truenas.example' \
        "$@"
}

# expect_attach_required <expected true|false> <description> <chart dir> [helm args...]
expect_attach_required() {
    local expected=$1 desc=$2 chart=$3
    shift 3
    local rendered
    if ! rendered=$(render_csidriver "${chart}" "$@" 2>&1); then
        echo "FAIL: ${desc} (helm template failed)"
        printf '    %s\n' "${rendered}"
        failures=$((failures + 1))
        return
    fi
    local lines
    lines=$(grep -c "^  attachRequired: ${expected}$" <<<"${rendered}")
    if [ "${lines}" -eq 1 ]; then
        echo "PASS: ${desc}"
    else
        echo "FAIL: ${desc} (expected exactly one 'attachRequired: ${expected}' line, got ${lines})"
        grep -n 'attachRequired' <<<"${rendered}" | sed 's/^/    /'
        failures=$((failures + 1))
    fi
}

# chart_without_values_line <pattern> -> prints the path of a chart copy whose
# values.yaml no longer contains lines matching the pattern.
chart_without_values_lines() {
    local pattern=$1 name=$2
    local copy="${TMPDIR_ROOT}/${name}"
    cp -r "${CHART}" "${copy}"
    grep -v -E "${pattern}" "${CHART}/values.yaml" >"${copy}/values.yaml"
    echo "${copy}"
}

expect_attach_required true "default renders attachRequired: true" "${CHART}"
expect_attach_required false "csiDriver.attachRequired=false is honored" "${CHART}" --set csiDriver.attachRequired=false
expect_attach_required true "an explicit true renders true" "${CHART}" --set csiDriver.attachRequired=true

# --reuse-values from a release older than the setting: values.yaml has a
# csiDriver block (seLinuxMount) but no attachRequired key.
old_values_chart=$(chart_without_values_lines '^  attachRequired:' "no-attach-required")
grep -q 'seLinuxMount' "${old_values_chart}/values.yaml" || { echo "FAIL: test setup lost the csiDriver block"; failures=$((failures + 1)); }
expect_attach_required true "values without attachRequired fall back to true" "${old_values_chart}"

# A values file with no csiDriver block at all must still render.
no_block_chart=$(chart_without_values_lines '^csiDriver:|^  attachRequired:|^  seLinuxMount:' "no-csidriver-block")
expect_attach_required true "values without a csiDriver block fall back to true" "${no_block_chart}"
expect_attach_required true "csiDriver=null falls back to true" "${CHART}" --set csiDriver=null

# seLinuxMount still renders when set, and is omitted by default.
if render_csidriver "${CHART}" --set csiDriver.seLinuxMount=true 2>/dev/null | grep -q '^  seLinuxMount: true$'; then
    echo "PASS: csiDriver.seLinuxMount=true renders seLinuxMount"
else
    echo "FAIL: csiDriver.seLinuxMount=true did not render seLinuxMount"
    failures=$((failures + 1))
fi
if render_csidriver "${CHART}" 2>/dev/null | grep -q 'seLinuxMount'; then
    echo "FAIL: seLinuxMount rendered by default"
    failures=$((failures + 1))
else
    echo "PASS: seLinuxMount is omitted by default"
fi

if [ "${failures}" -ne 0 ]; then
    echo "${failures} chart test(s) failed"
    exit 1
fi
echo "all csidriver chart tests passed"
