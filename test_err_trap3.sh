#!/bin/bash
set -e
trap 'echo "ERR TRAP FIRED"; exit 1' ERR

wait_for_driver() {
    echo "Starting wait_for_driver"
    
    if ! false; then
        echo "Command failed"
        
        # Show diagnostics  
        echo "=== Diagnostics ===" 
        false || true
        ls /nonexistent 2>/dev/null || true
        
        return 1
    fi
    
    echo "Success"
}

echo "Before wait_for_driver"
wait_for_driver
echo "After wait_for_driver - SHOULD NOT PRINT"
