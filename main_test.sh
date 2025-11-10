#!/bin/bash
set -e

source /Users/bfenski/tns-csi/common_test.sh

trap 'echo "ERR TRAP IN MAIN"; exit 1' ERR

echo "Before wait_for_driver"
wait_for_driver
echo "After wait_for_driver - SHOULD NOT PRINT"
