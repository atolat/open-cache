#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="open-cache"
RELEASE="open-cache"
CHART_DIR="$(cd "$(dirname "$0")/../charts/open-cache" && pwd)"

echo "Deploying ${RELEASE} to namespace ${NAMESPACE}..."
helm upgrade --install "${RELEASE}" "${CHART_DIR}" \
  --namespace "${NAMESPACE}" \
  "$@"

echo "Waiting for pod to be ready..."
kubectl rollout status deployment/${RELEASE}-envoy -n "${NAMESPACE}" --timeout=120s

echo "Done."
