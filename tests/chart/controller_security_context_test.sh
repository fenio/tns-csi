#!/bin/bash
# Chart tests for the controller pod's securityContext.
# Run: bash tests/chart/controller_security_context_test.sh
#
# The controller runs non-root with a read-only root filesystem and no
# capabilities; the node plugin keeps its privileged context. `helm upgrade
# --reuse-values` renders the NEW templates against the OLD chart's values, so
# the keys are absent; that is simulated by rendering a copy of the chart with
# them removed from values.yaml.

set -u

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART="${SCRIPT_DIR}/../../charts/tns-csi-driver"
TMPDIR_ROOT="$(mktemp -d)"
trap 'rm -rf "${TMPDIR_ROOT}"' EXIT

failures=0

pass() { echo "PASS: $1"; }
fail() {
    echo "FAIL: $1"
    failures=$((failures + 1))
}

# render <chart dir> <template> [helm args...]
render() {
    local chart=$1 template=$2
    shift 2
    helm template sc-test "${chart}" \
        --show-only "templates/${template}" \
        --set truenas.url=wss://truenas.example/api/current \
        --set truenas.apiKey=test \
        --set 'storageClasses[0].server=truenas.example' \
        --set snapshots.enabled=true \
        "$@"
}

# pod_security_context <rendered> -> the pod-level securityContext block, without its header.
pod_security_context() {
    awk '/^      securityContext:$/ { in_block = 1; next }
         in_block && /^        / { print; next }
         in_block { exit }' <<<"$1"
}

# container_count <rendered> -> the number of containers in the pod template.
container_count() {
    awk '/^      containers:$/ { in_list = 1; next }
         in_list && /^      [a-z]/ { exit }
         in_list && /^        - name: / { n++ }
         END { print n + 0 }' <<<"$1"
}

if ! rendered=$(render "${CHART}" controller.yaml 2>&1); then
    fail "controller renders with defaults (helm template failed)"
    printf '    %s\n' "${rendered}"
else
    pod=$(pod_security_context "${rendered}")
    for line in 'runAsNonRoot: true' 'runAsUser: 65532' 'runAsGroup: 65532' 'fsGroup: 65532' 'type: RuntimeDefault'; do
        if grep -q -- "${line}" <<<"${pod}"; then
            pass "controller pod sets ${line}"
        else
            fail "controller pod is missing ${line}"
        fi
    done

    containers=$(container_count "${rendered}")
    [ "${containers}" -eq 6 ] || fail "expected 6 controller containers with snapshots enabled, got ${containers}"
    for line in 'readOnlyRootFilesystem: true' 'allowPrivilegeEscalation: false' '- ALL'; do
        got=$(grep -c -- "^            *${line}$" <<<"${rendered}")
        if [ "${got}" -eq "${containers}" ]; then
            pass "all ${containers} controller containers set ${line}"
        else
            fail "${got} of ${containers} controller containers set ${line}"
        fi
    done
fi

# OpenShift assigns the IDs from the namespace's range; the rest still applies.
pod=$(pod_security_context "$(render "${CHART}" controller.yaml --set openshift.enabled=true 2>&1)")
if grep -q -E 'runAsUser|runAsGroup|fsGroup' <<<"${pod}"; then
    fail "openshift.enabled leaves fixed IDs in the controller pod"
else
    pass "openshift.enabled leaves the IDs to the SCC"
fi
if grep -q 'runAsNonRoot: true' <<<"${pod}"; then
    pass "openshift.enabled keeps runAsNonRoot"
else
    fail "openshift.enabled dropped runAsNonRoot"
fi

# Both contexts can be switched off.
if render "${CHART}" controller.yaml --set controller.podSecurityContext=null --set controller.securityContext=null 2>/dev/null | grep -q securityContext; then
    fail "null contexts still render a securityContext"
else
    pass "null contexts render no securityContext"
fi

# --reuse-values from a release older than these settings.
old_values_chart="${TMPDIR_ROOT}/old-values"
cp -r "${CHART}" "${old_values_chart}"
awk '/^  podSecurityContext:$|^  securityContext:$/ { skip = 1; next }
     skip && /^    / { next }
     { skip = 0; print }' "${CHART}/values.yaml" >"${old_values_chart}/values.yaml"
if grep -q -E '^  (podSecurityContext|securityContext):' "${old_values_chart}/values.yaml"; then
    fail "test setup did not remove the controller contexts"
elif ! out=$(render "${old_values_chart}" controller.yaml 2>&1); then
    fail "values without the controller contexts fail to render"
    printf '    %s\n' "${out}"
elif grep -q securityContext <<<"${out}"; then
    fail "values without the controller contexts render a securityContext"
else
    pass "values without the controller contexts render without one"
fi

# The node plugin is unaffected.
node=$(render "${CHART}" node.yaml 2>&1)
if grep -q '^            privileged: true$' <<<"${node}" && ! grep -q -E 'runAsNonRoot|readOnlyRootFilesystem' <<<"${node}"; then
    pass "node plugin keeps its privileged context"
else
    fail "node plugin securityContext changed"
fi

if [ "${failures}" -ne 0 ]; then
    echo "${failures} chart test(s) failed"
    exit 1
fi
echo "all controller securityContext chart tests passed"
