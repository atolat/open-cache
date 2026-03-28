# open-cache

[![Build](https://github.com/atolat/open-cache/actions/workflows/build.yml/badge.svg)](https://github.com/atolat/open-cache/actions/workflows/build.yml)
[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/open-cache)](https://artifacthub.io/packages/search?repo=open-cache)

S3-primary remote build cache for Bazel. Go. Apache 2.0.

## Install

```bash
helm install open-cache oci://ghcr.io/atolat/charts/open-cache
```

## Configure

Create a `values.yaml`:

```yaml
image:
  repository: ghcr.io/atolat/open-cache
  tag: latest

s3:
  bucket: your-cache-bucket
  region: us-east-1

server:
  port: 8080

nlb:
  certificateArn: arn:aws:acm:us-east-1:123456789:certificate/abc-123
```

```bash
helm install open-cache oci://ghcr.io/atolat/charts/open-cache -f values.yaml
```

## Use with Bazel

```
# .bazelrc
build --remote_cache=https://your-cache-endpoint
build --remote_upload_local_results=true
```

## Values

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/atolat/open-cache` | Container image |
| `image.tag` | `latest` | Image tag |
| `s3.bucket` | `open-cache-bazel` | S3 bucket name |
| `s3.region` | `us-east-1` | AWS region |
| `server.port` | `8080` | Listen port |
| `nlb.certificateArn` | `""` | ACM cert ARN for TLS termination |

## Prerequisites

- Kubernetes cluster with AWS Load Balancer Controller
- S3 bucket accessible from the cluster (VPC endpoint or IAM role)
- ACM certificate (optional, for TLS)

## Development

```bash
# Run tests
cd server && go test ./... -v -race

# Build locally
cd server && go build ./cmd/open-cache/

# Run locally
S3_BUCKET=my-bucket S3_REGION=us-east-1 ./open-cache
```

## Docs

https://open-cache.io
