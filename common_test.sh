#!/bin/bash
set -e

wait_for_driver() {
    echo "In wait_for_driver"
    if ! false; then
        echo "Driver check failed"
        return 1
    fi
    echo "Driver ready"
}
