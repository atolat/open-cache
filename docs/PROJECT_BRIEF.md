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

This is non-negotiable. Implement from Phase 2.

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

**Write path:** Simultaneous L1 + L2 via goroutines. Wait for both.

**Read path:** L1 first → miss → S3 fetch + L1 populate. `singleflight.Group`
deduplicates concurrent fetches for the same key.

## Build Phases

### Phase 1: HTTP + S3

Go HTTP server on `:8080`. GET/PUT/HEAD for `/ac/` and `/cas/` backed by S3.
Prometheus metrics on `/metrics`. Health check on `/healthz`.

```yaml
s3:
  bucket: my-cache-bucket
  region: us-east-1
  key_prefix: "cache/"
http:
  listen: ":8080"
```

### Phase 2: Completeness Checking

Parse ActionResult proto on AC reads. Verify all CAS references exist via
batched HeadObject calls. Return 404 if any missing.

### Phase 3: gRPC REAPI

Dual-protocol serving. Implement Capabilities, ActionCache, CAS, ByteStream.
Fan out `FindMissingBlobs` concurrently with `errgroup`. Multipart upload for
blobs > 5MB.

```yaml
grpc:
  listen: ":9092"
  max_batch_size_bytes: 4194304
```

### Phase 4: Valkey L1

`go-redis` client. FastSlowStore pattern: write to both tiers simultaneously.
`singleflight.Group` on read misses. Size partitioning: small blobs go to
Valkey, large blobs S3-only.

```yaml
valkey:
  enabled: true
  addr: "valkey:6379"
  max_blob_size_bytes: 1048576
  ac_ttl: "1h"
  cas_ttl: "24h"
```

### Phase 5: Helm Chart

Deployment (not StatefulSet — pods are stateless). IRSA for S3 access.
Optional Bitnami Valkey subchart. ServiceMonitor, HPA, PDB.

### Phase 6: Access Logging + RL Eviction

See [ARCHITECTURE_AI.md](ARCHITECTURE_AI.md).

### Phase 7: MCP Servers + LLM Agents

See [ARCHITECTURE_AI.md](ARCHITECTURE_AI.md).

## Implementation Patterns

### FastSlowStore Write

```go
func (c *Cache) put(ctx context.Context, key string, data []byte) error {
    var wg sync.WaitGroup
    errs := make([]error, 2)
    wg.Add(2)
    go func() { defer wg.Done(); errs[0] = c.valkey.Set(ctx, key, data) }()
    go func() { defer wg.Done(); errs[1] = c.s3.Put(ctx, key, data) }()
    wg.Wait()
    return errors.Join(errs...)
}
```

### Singleflight Read

```go
func (c *Cache) get(ctx context.Context, key string) ([]byte, error) {
    if data, err := c.valkey.Get(ctx, key); err == nil {
        return data, nil
    }
    v, err, _ := c.group.Do(key, func() (interface{}, error) {
        data, err := c.s3.Get(ctx, key)
        if err == nil {
            c.valkey.Set(ctx, key, data)
        }
        return data, err
    })
    return v.([]byte), err
}
```

### Completeness Check

```go
func (c *Cache) getActionResult(ctx context.Context, hash string) (*repb.ActionResult, error) {
    ar, err := c.getRaw(ctx, "ac", hash)
    if err != nil { return nil, err }

    digests := collectDigests(ar)
    missing, err := c.findMissing(ctx, digests)
    if err != nil { return nil, err }
    if len(missing) > 0 {
        return nil, status.Errorf(codes.NotFound, "AC references missing CAS blobs")
    }
    return ar, nil
}
```

## Project Structure

```
cmd/open-cache/          main.go
internal/
  cache/
    cache.go             Cache interface
    s3/s3.go             S3 store
    valkey/valkey.go      Valkey L1
    tiered/tiered.go      FastSlowStore
    completeness/         AC completeness checking
  server/
    http.go              HTTP handlers
    grpc.go              gRPC REAPI
  config/config.go       Config + viper
  metrics/metrics.go     Prometheus
charts/open-cache/       Helm chart
tools/
  simulator/             Cache trace simulator (Python)
  synthetic-monorepo/    Workload generator (Python)
  rl-agent/              RL training (Python)
```

## Local Dev

```yaml
# docker-compose.yml
services:
  open-cache:
    build: .
    ports: ["8080:8080", "9092:9092"]
    environment:
      S3_BUCKET: cache
      S3_ENDPOINT: http://minio:9000
      S3_REGION: us-east-1
      S3_ACCESS_KEY: minioadmin
      S3_SECRET_KEY: minioadmin
      VALKEY_ADDR: valkey:6379

  minio:
    image: minio/minio
    command: server /data
    ports: ["9000:9000"]

  valkey:
    image: valkey/valkey:8
    ports: ["6379:6379"]
```

```
# .bazelrc
build --remote_cache=http://localhost:8080
build --remote_upload_local_results=true
```

## Dependencies

| Package | Purpose |
|---|---|
| `aws-sdk-go-v2` | S3 client |
| `google.golang.org/grpc` | gRPC server |
| `github.com/bazelbuild/remote-apis` | REAPI proto definitions |
| `go-redis` | Valkey client |
| `prometheus/client_golang` | Metrics |
| `golang.org/x/sync/singleflight` | Thundering herd prevention |
| `viper` | Config loading |
