#!/bin/bash
set -e
trap 'echo "ERR TRAP FIRED"; exit 1' ERR

wait_for_driver() {
    echo "Starting wait_for_driver"
    
    if ! false; then
        echo "Kubectl wait failed"
        
        # Show diagnostics
        echo "=== Diagnostics ===" 
        echo "Getting pod info"
        kubectl get pod nonexistent 2>/dev/null || true
        kubectl describe pod nonexistent 2>/dev/null || true
        
        return 1
    fi
    
    echo "Driver is ready"
}

echo "Before wait_for_driver"
wait_for_driver
echo "After wait_for_driver - SHOULD NOT PRINT"
