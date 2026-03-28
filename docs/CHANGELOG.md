# Changelog

## v0.1.0 — 2026-03-28

First working release. S3-only cache proxy.

### What works

- Go HTTP server handling Bazel Simple HTTP Cache protocol (GET/PUT/HEAD for `/ac/` and `/cas/`)
- Streaming uploads to S3 via multipart upload manager (handles multi-GB blobs without buffering)
- Streaming downloads from S3
- Helm chart deploys on EKS with NLB + ACM TLS termination
- CI pipeline: test → build Docker image → push to GHCR → push Helm chart to GHCR
- Terraform for S3 bucket, VPC endpoint, Route53, ACM cert

### Known limitations

- No completeness checking (BWOB unsafe — AC can reference evicted CAS blobs)
- No caching tiers (every request hits S3)
- No auth
- No gRPC / REAPI v2
- No metrics

### What's next

- L1 RAM cache (all AC entries + small CAS blobs)
- L2 disk cache
- Completeness checking on AC reads
- Prometheus metrics
