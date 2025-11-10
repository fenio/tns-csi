#!/bin/bash
set -e

summary_func() {
    local status=$1
    echo "Summary: $status"
    if [[ "$status" == "FAILED" ]]; then
        exit 1
    else
        exit 0
    fi
}

trap 'echo "Trap 1"; summary_func "FAILED"; echo "Trap 2 - should not print"; exit 99' ERR

failing_func() {
    echo "In failing function"
    return 1
}

echo "Before failing_func"
failing_func
echo "After failing_func - should not print"
summary_func "PASSED"
