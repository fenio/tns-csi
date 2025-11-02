#!/bin/bash
# Download nvme-cli and dependencies for Ubuntu 22.04 arm64
mkdir -p packages
cd packages

# Download nvme-cli and deps
apt-get download nvme-cli libnvme1 libjson-c5 2>&1

echo "Downloaded:"
ls -lh
