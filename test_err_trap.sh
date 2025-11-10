#!/bin/bash
set -e
trap 'echo "ERR trap fired!"; exit 1' ERR

test_func() {
    echo "In test_func"
    if ! false; then
        echo "Condition failed"
        echo "Running command with || true"
        false || true
        return 1
    fi
    echo "After condition"
}

echo "Before test_func"
test_func
echo "After test_func - THIS SHOULD NOT PRINT"
