# Design Document

## Goals

- Cache-only remote cache for Bazel (no remote execution)
- S3 as the primary store — no local disk required, pods are stateless
- Optional Valkey L1 for hot blobs
- HTTP and gRPC (REAPI v2) protocols
- Helm chart for K8s/EKS deployment
- Apache 2.0

## Protocol Reference

### HTTP (Simple Cache)

Bazel config: `--remote_cache=http://host:8080`

```
PUT  /[instance/]ac/{hash}    Store ActionResult
GET  /[instance/]ac/{hash}    Fetch ActionResult
HEAD /[instance/]ac/{hash}    Check existence

PUT  /[instance/]cas/{hash}   Store blob
GET  /[instance/]cas/{hash}   Fetch blob
HEAD /[instance/]cas/{hash}   Check existence
```

- `Content-Length` required on PUT
- 200 on hit, 404 on miss
- Optional instance name prefix

### gRPC (REAPI v2)

Bazel config: `--remote_cache=grpc://host:9092`

Services (in implementation order):

1. **Capabilities** — `GetCapabilities` — digest functions, compression, batch limits
2. **ActionCache** — `GetActionResult`, `UpdateActionResult`
3. **CAS** — `FindMissingBlobs`, `BatchReadBlobs`, `BatchUpdateBlobs`, `GetTree`
4. **ByteStream** — `Read`, `Write` — streaming for large blobs

Proto source: https://github.com/bazelbuild/remote-apis

## Stores

**AC (Action Cache)** — maps `hash(Action)` → `ActionResult` proto. Contains output
file digests, stdout/stderr digests, exit code. Small (KBs), frequently read.

**CAS (Content Addressable Storage)** — maps `sha256(content)` → raw bytes. Build
outputs, source files, Tree protos. Bytes to GBs. Immutable.

### Completeness Checking (BWOB)

Bazel 7+ defaults to `--remote_download_minimal`. Bazel skips downloading intermediate
outputs, which creates a failure mode: AC references CAS blobs that have been evicted.

On every AC read:

1. Parse output digests from the ActionResult
2. `FindMissing` on all referenced CAS entries
3. If any missing → return 404 (forces Bazel to rebuild)

### S3 Key Layout

```
{prefix}/cas/{hash[0:2]}/{hash}
{prefix}/ac/{hash[0:2]}/{hash}
```

Two-char prefix distributes objects across S3 partitions.

## Architecture

```
┌──────────────────────────────────────────┐
│              open-cache pod              │
│                                          │
│   HTTP :8080        gRPC :9092           │
│        \              /                  │
│         Cache Router                     │
│         (completeness check on AC reads) │
│              /        \                  │
│       Valkey L1      S3 L2               │
│       (optional)     (authoritative)     │
└──────────────────────────────────────────┘
```

**Write path:** Simultaneous L1 + L2. Wait for both.

**Read path:** L1 first → miss → S3 fetch + L1 populate. Deduplicate concurrent
fetches for the same key.

## Build Phases

1. **HTTP + S3** — HTTP server backed by S3. Metrics. Health check.
2. **Completeness checking** — verify CAS references on AC reads.
3. **gRPC REAPI** — dual-protocol serving.
4. **Valkey L1** — tiered caching with size partitioning.
5. **Helm chart** — Deployment, IRSA, ServiceMonitor, HPA.
6. **Access logging + RL eviction** — see [ARCHITECTURE_AI.md](ARCHITECTURE_AI.md).
7. **MCP servers + LLM agents** — see [ARCHITECTURE_AI.md](ARCHITECTURE_AI.md).
