#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="open-cache"
RELEASE="open-cache"
DOMAIN="open-cache.io"
SUBDOMAIN="cache"
ZONE_ID="Z05235392JP19X0CC9OUW"

echo "Fetching NLB hostname..."
NLB_HOSTNAME=$(kubectl get svc ${RELEASE}-envoy -n "${NAMESPACE}" \
  -o jsonpath='{.status.loadBalancer.ingress[0].hostname}')

if [ -z "${NLB_HOSTNAME}" ]; then
  echo "ERROR: NLB hostname not available yet. Wait a minute and re-run."
  exit 1
fi

echo "NLB: ${NLB_HOSTNAME}"

echo "Updating Route53: ${SUBDOMAIN}.${DOMAIN} -> ${NLB_HOSTNAME}"
aws route53 change-resource-record-sets \
  --hosted-zone-id "${ZONE_ID}" \
  --change-batch '{
    "Changes": [{
      "Action": "UPSERT",
      "ResourceRecordSet": {
        "Name": "'"${SUBDOMAIN}.${DOMAIN}"'",
        "Type": "CNAME",
        "TTL": 300,
        "ResourceRecords": [{"Value": "'"${NLB_HOSTNAME}"'"}]
      }
    }]
  }'

echo "Done. Verify with: dig ${SUBDOMAIN}.${DOMAIN} +short"
