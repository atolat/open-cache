#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="open-cache"

echo "Creating namespace ${NAMESPACE}..."
kubectl create namespace "${NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -

echo "Done."
