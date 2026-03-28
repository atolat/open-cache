# Getting Started

## Prerequisites

- Kubernetes cluster (EKS recommended)
- [AWS Load Balancer Controller](https://kubernetes-sigs.github.io/aws-load-balancer-controller/) installed on the cluster
- S3 bucket for cache storage
- S3 VPC gateway endpoint (so pods can reach S3 without going over the internet)
- Bucket policy allowing access from the VPC endpoint
- ACM certificate (optional, for TLS)

## Install

### Quick start

```bash
helm install open-cache oci://ghcr.io/atolat/charts/open-cache \
  --namespace open-cache --create-namespace \
  --set s3.bucket=your-bucket-name \
  --set s3.region=us-east-1
```

### With TLS

```bash
helm install open-cache oci://ghcr.io/atolat/charts/open-cache \
  --namespace open-cache --create-namespace \
  --set s3.bucket=your-bucket-name \
  --set s3.region=us-east-1 \
  --set nlb.certificateArn=arn:aws:acm:us-east-1:123456789:certificate/abc-123
```

### With a values file

```yaml
# values.yaml
image:
  repository: ghcr.io/atolat/open-cache
  tag: latest

s3:
  bucket: your-bucket-name
  region: us-east-1

server:
  port: 8080

nlb:
  certificateArn: arn:aws:acm:us-east-1:123456789:certificate/abc-123
```

```bash
helm install open-cache oci://ghcr.io/atolat/charts/open-cache \
  --namespace open-cache --create-namespace \
  -f values.yaml
```

## Values Reference

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/atolat/open-cache` | Container image |
| `image.tag` | `latest` | Image tag |
| `s3.bucket` | `open-cache-bazel` | S3 bucket for cache storage |
| `s3.region` | `us-east-1` | AWS region of the bucket |
| `server.port` | `8080` | Port the server listens on |
| `nlb.certificateArn` | `""` | ACM certificate ARN for TLS on the NLB. If empty, NLB listens on port 443 without TLS. |

## Verify

Check that the pod is running:

```bash
kubectl get pods -n open-cache
```

Get the NLB endpoint:

```bash
kubectl get svc -n open-cache -o jsonpath='{.items[0].status.loadBalancer.ingress[0].hostname}'
```

Test with curl:

```bash
# PUT a test object
curl -X PUT -d "hello" https://your-endpoint/cas/test

# GET it back
curl https://your-endpoint/cas/test
# → hello

# HEAD check
curl -I https://your-endpoint/cas/test
# → HTTP/1.1 200 OK
```

## Configure Bazel

Add to your `.bazelrc`:

```
build --remote_cache=https://your-endpoint
build --remote_upload_local_results=true
```

Or with a DNS name (requires Route53 CNAME pointing to the NLB):

```
build --remote_cache=https://cache.your-domain.com
```

## AWS Setup

### S3 bucket policy

The bucket should only be accessible from within your VPC. Create an
S3 VPC gateway endpoint and restrict the bucket policy:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": "*",
    "Action": ["s3:GetObject", "s3:PutObject", "s3:ListBucket"],
    "Resource": [
      "arn:aws:s3:::your-bucket",
      "arn:aws:s3:::your-bucket/*"
    ],
    "Condition": {
      "StringEquals": {
        "aws:sourceVpce": "vpce-your-endpoint-id"
      }
    }
  }]
}
```

### Terraform

The `terraform/` directory in this repo contains modules for:

- S3 bucket with public access block
- S3 VPC gateway endpoint
- Bucket policy (VPC endpoint only)
- Route53 hosted zone
- ACM certificate with DNS validation

```bash
cd terraform
terraform init
terraform apply
```

## Uninstall

```bash
helm uninstall open-cache -n open-cache
kubectl delete namespace open-cache
```
