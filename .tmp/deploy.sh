#!/bin/bash
set -e

# Extract credentials
TRUENAS_URL="wss://10.10.20.100:1443/api/current"
TRUENAS_API_KEY="21-Vs69BgtFwwINmDNKC85iMN8VyOCEqRCgTK0hnBJBcBzjOvXhTJQ1FUFPRlQ71qzE"

# Create namespace
kubectl create namespace tns-csi || true

# Create secret
kubectl create secret generic tns-csi-secret \
  --from-literal=truenasURL="$TRUENAS_URL" \
  --from-literal=truenasAPIKey="$TRUENAS_API_KEY" \
  -n tns-csi --dry-run=client -o yaml | kubectl apply -f -

echo "Secret created"
