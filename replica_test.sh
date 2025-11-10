#!/bin/bash
set -e

source /Users/bfenski/tns-csi/replica_common.sh

PROTOCOL="Basic CSI (nfs)"

trap 'show_diagnostic_logs; cleanup_test; test_summary "${PROTOCOL}" "FAILED"; exit 1' ERR

echo "Starting test"
wait_for_driver
echo "After wait_for_driver - SHOULD NOT PRINT"

cleanup_test
test_summary "${PROTOCOL}" "PASSED"
