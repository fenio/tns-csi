#!/bin/bash
set -e
trap 'echo "Trap 1"; echo "Trap 2"; echo "Trap 3"; exit 99' ERR

failing_func() {
    echo "In failing function"
    return 1
}

echo "Before failing_func"
failing_func
echo "After failing_func"
