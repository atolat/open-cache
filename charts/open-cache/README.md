# open-cache

S3-primary remote build cache for Bazel. Deploys a Go HTTP server on Kubernetes
that proxies Bazel cache requests to S3.

## Install

```bash
helm install open-cache oci://ghcr.io/atolat/charts/open-cache \
  --namespace open-cache --create-namespace \
  --set s3.bucket=your-bucket-name \
  --set s3.region=us-east-1
```

## With TLS

```bash
helm install open-cache oci://ghcr.io/atolat/charts/open-cache \
  --namespace open-cache --create-namespace \
  --set s3.bucket=your-bucket-name \
  --set s3.region=us-east-1 \
  --set nlb.certificateArn=arn:aws:acm:us-east-1:123456789:certificate/abc-123
```

## Values

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/atolat/open-cache` | Container image |
| `image.tag` | `latest` | Image tag |
| `s3.bucket` | `open-cache-bazel` | S3 bucket for cache storage |
| `s3.region` | `us-east-1` | AWS region |
| `server.port` | `8080` | Listen port |
| `nlb.certificateArn` | `""` | ACM cert ARN for TLS termination |

## Prerequisites

- Kubernetes cluster with [AWS Load Balancer Controller](https://kubernetes-sigs.github.io/aws-load-balancer-controller/)
- S3 bucket accessible from the cluster (VPC endpoint or IAM role)
- ACM certificate (optional, for TLS)

## Configure Bazel

```
# .bazelrc
build --remote_cache=https://your-cache-endpoint
build --remote_upload_local_results=true
```

## Documentation

Full docs at [open-cache.io](https://open-cache.io).
