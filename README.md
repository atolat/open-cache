# open-cache

A remote build cache for Bazel.

- Stores build artifacts in S3 (no local disk required)
- Optional in-memory L1 cache using Valkey for hot data
- Speaks HTTP and gRPC
- Deploys on Kubernetes via Helm
- Written in Go, Apache 2.0 licensed

## Status

Early development. Not yet usable.

## Docs

Run `mkdocs serve` to browse project documentation and learning notes locally.
