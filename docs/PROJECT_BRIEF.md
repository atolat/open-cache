# open-cache: Project Brief

> A cache-only, S3-primary, HTTP+gRPC, Valkey-L1, K8s-native Bazel remote cache.
> Written in Go. Deployed via Helm. True open source (Apache 2.0).
>
> This document is the canonical briefing for any new session working on this project.
> It covers: why this exists, what the ecosystem looks like, the Bazel caching protocol,
> the architecture, and the build order from bare minimum upward.

---

## 1. What This Is (and Is Not)

**Is:**
- A remote cache for Bazel (and other REAPI-compatible build systems: Buck2, Pants)
- Cache-only — no remote execution, no scheduler, no workers
- S3 as the primary durable store — no mandatory local disk
- Optional Valkey (Redis-compatible) L1 for hot blobs
- Speaks both Bazel's Simple HTTP Cache protocol and gRPC REAPI
- Deployed on Kubernetes/EKS via Helm chart
- Apache 2.0 licensed

**Is not:**
- A remote execution platform (not competing with Buildbarn/NativeLink on RBE)
- A replacement for bazel-remote (complementary — simpler deployment, better S3 story)
- A SaaS product

**One-line pitch:**
> The build cache that actually treats S3 as a first-class backend, deploys in 30 minutes on EKS, and doesn't require a PhD to operate.

---

## 2. Why This Gap Exists: Ecosystem Summary

### Open Source Landscape

| Project | Language | S3 Primary? | HTTP Cache? | Helm Chart | Complexity |
|---|---|---|---|---|---|
| **bazel-remote** | Go | No (proxy only) | Yes (limited) | Stale 3rd-party | Low |
| **NativeLink** | Rust | Yes | No | No | High |
| **Buildbarn** | Go | No (block device) | No | No (Jsonnet) | Very High |
| **buildfarm** | Java | No (worker disk) | No | Yes (best OSS) | High |

**bazel-remote** is the closest in spirit but fundamentally disk-first:
- `dir` (disk path) is a required config field — server refuses to start without it
- S3 is wired as a `Proxy` interface with no `Delete` method — objects grow forever
- S3 writes are async and silently dropped if the queue is full
- LRU only applies to disk — S3 is never evicted
- `BatchUpdateBlobs` and `BatchReadBlobs` are sequential (not parallelized) — TODOs in source
- Stale third-party Helm chart

**NativeLink** validates the S3-primary concept but:
- gRPC REAPI only — no Bazel Simple HTTP Cache protocol
- No Helm chart — raw K8s manifests only
- Functional Source License (FSL), not Apache 2.0 — enterprise legal teams reject it
- Redis used for scheduler state, not as an optimized blob cache tier
- Full RBE platform overhead if you only want caching
- S3 "LRU" is TTL-only (`consider_expired_after_s` via HeadObject) — no true eviction

**Buildbarn** has the best architecture but:
- No S3 backend — uses custom block device format on local disk
- Requires StatefulSets with PVCs — cannot run diskless
- Config is Jsonnet, not YAML — steep learning curve
- No Helm chart

**Commercial alternatives** (BuildBuddy, EngFlow, Depot):
- SaaS lock-in
- Bazel-only or limited multi-system support
- No self-hosted path without enterprise contract

### The Gap

No project offers all of:
1. S3 as a first-class primary backend (no disk required)
2. Bazel Simple HTTP Cache protocol support
3. Valkey/Redis L1 for hot blobs
4. True coordinated LRU across tiers
5. Production-grade Helm chart
6. Simple enough to deploy in 30 minutes
7. Apache 2.0 license

---

## 3. Bazel Caching Protocol: What You Need to Know

Understanding this is essential before writing any code.

### Two Protocols

Bazel supports two remote cache protocols. You need to implement both eventually, but start with HTTP.

#### Protocol 1: Simple HTTP Cache (start here)

Bazel config: `--remote_cache=http://your-cache:8080`

```
PUT /ac/{sha256-hash}         — store an ActionResult
GET /ac/{sha256-hash}         — fetch an ActionResult
HEAD /ac/{sha256-hash}        — check existence

PUT /cas/{sha256-hash}        — store a blob
GET /cas/{sha256-hash}        — fetch a blob
HEAD /cas/{sha256-hash}       — check existence
```

- Content-Length header required on PUT
- 200 OK on GET hit, 404 on miss
- No auth required for minimal implementation
- Optional: instance name prefix (`/{instance}/ac/{hash}`)
- This is what your current nginx+S3 setup approximates

#### Protocol 2: gRPC REAPI (add later)

Bazel config: `--remote_cache=grpc://your-cache:9092`

Key services (implement in this order):
1. **Capabilities** — `GetCapabilities` — tells Bazel what the cache supports (digest functions, compression, max batch size)
2. **ActionCache** — `GetActionResult`, `UpdateActionResult`
3. **ContentAddressableStorage** — `FindMissingBlobs`, `BatchReadBlobs`, `BatchUpdateBlobs`, `GetTree`
4. **ByteStream** — `Read`, `Write` — for large blobs that don't fit in batch requests

Proto definitions: https://github.com/bazelbuild/remote-apis

### The Two Stores

Every Bazel cache manages two distinct stores:

**AC (Action Cache)**
- Maps: `hash(Action proto)` → `ActionResult proto`
- ActionResult contains: list of output file digests, stdout/stderr digests, exit code
- Small objects (KBs), frequently read
- Short-lived relative to CAS

**CAS (Content Addressable Storage)**
- Maps: `sha256(content)` → `raw bytes`
- Contains: actual build outputs, source files, Tree protos for directories
- Large objects (bytes to GBs)
- Immutable: same hash always means same content

### The BWOB Problem (Build Without the Bytes)

Bazel 7+ default: `--remote_download_minimal`. Bazel skips downloading intermediate outputs — it only downloads final outputs. This means:

1. Bazel writes to AC: `action_hash → {output_file: cas_hash_A, cas_hash_B, ...}`
2. Later: CAS evicts `cas_hash_A` (cache pressure)
3. Bazel reads AC: gets the ActionResult back (AC hit)
4. Bazel tries to use `cas_hash_A` for a downstream action
5. **CAS miss → build failure**

**The correct fix** (from Buildbarn's source code):

On every AC read, before returning the ActionResult:
- Parse all digest fields from the ActionResult
- Call `FindMissing` on all referenced CAS entries
- If any are missing → return 404 for the AC entry (treat as cache miss)
- This forces Bazel to re-run the action and re-populate the cache

This is called `CompletenessChecking`. Implement this from day one.

### S3 Key Layout

Recommended key structure:
```
{prefix}/cas/{hash[0:2]}/{hash}    — CAS blobs (sharded by first 2 chars)
{prefix}/ac/{hash[0:2]}/{hash}     — Action Cache entries
```

The two-character prefix shard (`hash[0:2]`) distributes objects across S3's internal partitions — important for performance at scale.

---

## 4. Architecture

### Components

```
┌─────────────────────────────────────────────────────────┐
│                    open-cache pod(s)                     │
│                                                          │
│   ┌──────────────┐    ┌──────────────────────────────┐  │
│   │  HTTP server  │    │       gRPC server             │  │
│   │  :8080        │    │       :9092                   │  │
│   │  /ac/{hash}   │    │  ActionCache                 │  │
│   │  /cas/{hash}  │    │  CAS                         │  │
│   └──────┬───────┘    │  ByteStream                  │  │
│          │             │  Capabilities                 │  │
│          └──────┬──────┴──────────────────────────────┘  │
│                 │                                         │
│          ┌──────▼──────────────┐                         │
│          │   Cache Router       │                         │
│          │  (completeness check)│                         │
│          └──────┬──────────────┘                         │
│                 │                                         │
│    ┌────────────┼────────────────┐                        │
│    ▼            ▼                ▼                        │
│  L1: Valkey   L1: Valkey       L2: S3                    │
│  (AC hot)     (CAS hot)        (primary durable)         │
└─────────────────────────────────────────────────────────┘
```

### Tier Design

**L2: S3 (always present, always authoritative)**
- Every write goes to S3
- S3 is the source of truth for durability
- No mandatory local disk
- Cache pods are stateless → `Deployment`, not `StatefulSet`

**L1: Valkey (optional, additive)**
- Hot blob cache: recently written or frequently read blobs
- Shared across all cache pods (DaemonSet with shared memory, or external Valkey cluster)
- Writes go to both L1 and L2 simultaneously (NativeLink FastSlowStore pattern)
- On read: L1 hit → serve immediately; L1 miss → fetch from S3, populate L1
- Eviction: Valkey handles its own LRU; S3 lifecycle rules handle S3 expiry

**Completeness Checker (wraps AC)**
- Every AC GET validates all referenced CAS entries exist (in either L1 or L2)
- Returns 404 if any CAS entry is missing → BWOB-safe

---

## 5. Build Order: Bare Minimum First

### Phase 1: HTTP Cache + S3 (MVP — make it work)

**Goal:** A Go HTTP server that speaks Bazel's Simple HTTP Cache protocol with S3 as storage. Replace nginx+S3 with something that understands the protocol.

**What to build:**
1. Go HTTP server on `:8080`
2. `GET /[instance/]ac/{hash}` → S3 GetObject(`ac/{hash[:2]}/{hash}`)
3. `PUT /[instance/]ac/{hash}` → S3 PutObject
4. `HEAD /[instance/]ac/{hash}` → S3 HeadObject
5. Same for `/cas/` endpoints
6. S3 client with proper connection pooling (`MaxIdleConns`, `IdleConnTimeout`)
7. Basic Prometheus metrics (`/metrics`): hit count, miss count, latency histograms
8. Health endpoint (`/healthz`)
9. Dockerfile + basic K8s Deployment YAML

**What to NOT build yet:** gRPC, Valkey, auth, compression, Helm chart.

**Test it:** Point a local Bazel build at `--remote_cache=http://localhost:8080` and verify cache hits on second build.

**Config (minimal):**
```yaml
s3:
  bucket: my-cache-bucket
  region: us-east-1
  key_prefix: "cache/"
http:
  listen: ":8080"
```

---

### Phase 2: Correctness (make it safe)

**Goal:** Handle BWOB correctly. Don't return stale ActionResults.

**What to build:**
1. On `GET /ac/{hash}`: parse the returned ActionResult proto
2. Check all referenced CAS digests exist (HEAD requests to S3, batched)
3. If any missing → return 404 even though AC entry exists
4. Add `--remote_cache_eviction_retries`-compatible behavior
5. zstd compression support (`Content-Encoding: zstd` on PUT, `Accept-Encoding: zstd` on GET)
6. Configurable `Content-Length` enforcement

**Key dependency:** You need to parse ActionResult proto. Use the generated Go proto from `github.com/bazelbuild/remote-apis`.

---

### Phase 3: gRPC REAPI (make it fast)

**Goal:** Support `--remote_cache=grpc://...` which enables batch operations, streaming, and better performance for large builds.

**Implement in this order:**

1. **Capabilities** — `GetCapabilities` — advertise SHA256, zstd, batch size limits
2. **ActionCache** — `GetActionResult` + `UpdateActionResult` (with completeness checking)
3. **CAS** — `FindMissingBlobs` (fan out to S3 concurrently), `BatchReadBlobs`, `BatchUpdateBlobs`
4. **ByteStream** — `Read` + `Write` (for blobs > batch size limit)

**Key patterns from NativeLink to adopt:**
- `has_with_results` equivalent: batch HeadObject calls to S3 concurrently using `errgroup`
- Retry with backoff on S3 errors (use `Code::Aborted` / retryable gRPC codes)
- Range reads on S3 GetObject (support `offset` parameter in ByteStream.Read)
- Multipart upload for large blobs (> 5MB)

**Config additions:**
```yaml
grpc:
  listen: ":9092"
  max_batch_size_bytes: 4194304  # 4MB
```

---

### Phase 4: Valkey L1 (make it fast for hot blobs)

**Goal:** Add an optional in-memory tier for hot blobs.

**What to build:**
1. Valkey client (use `go-redis` — Valkey is Redis-compatible)
2. `FastSlowStore` pattern: on write, write to Valkey AND S3 simultaneously via goroutines
3. On read: check Valkey first; on miss, fetch from S3 and populate Valkey
4. Deduplication of concurrent fetches for the same key (use `sync.Map` + `singleflight.Group`)
5. Size partitioning: AC entries and small CAS blobs (< configurable threshold) → Valkey; large blobs → S3 only
6. Valkey TTL: set short TTL on AC entries, longer on CAS

**Key pattern from NativeLink (FastSlowStore):**
- Simultaneous writes to both tiers using channels — client waits for both to complete
- `singleflight.Group` prevents thundering herd on L1 miss (only one goroutine fetches from S3)
- Log a warning if channel send stalls > 5 seconds (diagnose hung downstream stores)

**Config additions:**
```yaml
valkey:
  enabled: true
  addr: "valkey:6379"
  max_blob_size_bytes: 1048576  # 1MB — larger blobs go S3-only
  ac_ttl: "1h"
  cas_ttl: "24h"
```

---

### Phase 5: Helm Chart + Production Readiness

**Goal:** One-command deploy on EKS.

**Helm chart structure** (borrow from buildfarm's chart):
```
charts/open-cache/
  Chart.yaml
  values.yaml
  templates/
    deployment.yaml       — cache pods (Deployment, not StatefulSet — stateless!)
    service.yaml          — ClusterIP service
    configmap.yaml        — config.yaml rendered from values
    serviceaccount.yaml   — IAM role annotation for IRSA
    hpa.yaml              — HorizontalPodAutoscaler
    pdb.yaml              — PodDisruptionBudget
    servicemonitor.yaml   — Prometheus Operator ServiceMonitor
    ingress.yaml
```

**Key values to expose:**
```yaml
replicaCount: 2

image:
  repository: ghcr.io/your-org/open-cache
  tag: ""

s3:
  bucket: ""
  region: us-east-1
  keyPrefix: "cache/"

valkey:
  enabled: false        # opt-in
  external:
    addr: ""            # use external Valkey
  embedded:
    enabled: false      # or embed via Bitnami chart dependency

http:
  port: 8080

grpc:
  port: 9092

auth:
  enabled: false
  htpasswd: ""

resources:
  requests:
    cpu: 500m
    memory: 512Mi
  limits:
    cpu: 2
    memory: 2Gi

serviceMonitor:
  enabled: false

autoscaling:
  enabled: false
  minReplicas: 2
  maxReplicas: 10
  targetMemoryUtilizationPercentage: 70

# Escape hatches
extraEnv: []
extraVolumes: []
extraVolumeMounts: []
```

**AWS IAM / IRSA:**
- Cache pods need S3 read/write on the cache bucket
- Use IRSA (IAM Roles for Service Accounts) — annotate the ServiceAccount with the role ARN
- No AWS credentials in pods

**Production checklist before GA:**
- [ ] gRPC health checks (compatible with AWS ALB target groups)
- [ ] Graceful shutdown (drain in-flight requests before pod terminates)
- [ ] `PodDisruptionBudget` (ensure at least 1 pod during rolling deploys)
- [ ] Prometheus metrics with sensible default dashboards
- [ ] `topologySpreadConstraints` to spread pods across AZs

---

### Phase 6: S3 LRU + Advanced Features (nice to have)

These are differentiators but not blockers for initial release.

**Emulated S3 LRU:**
- On write: set S3 object metadata `last-accessed: {timestamp}`
- On read: update `last-accessed` (async, non-blocking — use background goroutine)
- Background worker: scan S3 bucket periodically, delete objects older than threshold that weren't recently accessed
- Or: use S3 Intelligent-Tiering + lifecycle rules for cost management instead of deletion
- Note: true LRU on S3 is expensive (S3 List + many HeadObject calls). Consider S3 lifecycle rules as the pragmatic alternative.

**Auth:**
- Basic auth via `htpasswd` file (compatible with Bazel's `--remote_header` flag)
- mTLS (client cert verification)
- Read-only vs read-write token split (prevent cache poisoning from dev machines)

**Multi-instance consistency:**
- Multiple cache pods reading/writing S3 is safe (S3 provides strong read-after-write consistency as of 2020)
- Valkey is already shared — no issue
- No sticky routing needed

**Buck2 / Pants support:**
- Both speak gRPC REAPI — no extra work needed once Phase 3 is done

---

## 6. Key Technical Decisions

| Decision | Choice | Reason |
|---|---|---|
| Language | Go | Good gRPC ecosystem, fast compilation, easy deployment, matches learning goals |
| Primary store | S3 | No disk required, stateless pods, AWS-native, scales infinitely |
| L1 cache | Valkey | True open source (vs Redis license change), drop-in Redis compatible |
| HTTP framework | `net/http` stdlib | Simple, no dependencies, sufficient for cache protocol |
| gRPC framework | `google.golang.org/grpc` | Standard Go gRPC library |
| Proto codegen | `github.com/bazelbuild/remote-apis` | Official REAPI proto definitions |
| S3 client | `aws-sdk-go-v2` | AWS official SDK v2 for Go, better connection management than v1 |
| Config | YAML via `viper` | Familiar, K8s-native, easy Helm integration |
| Metrics | `prometheus/client_golang` | Standard; ServiceMonitor compatible |
| Concurrency primitive for dedup | `golang.org/x/sync/singleflight` | Prevents thundering herd on L1 miss |

---

## 7. Patterns to Borrow From Existing Projects

### From NativeLink (Rust → translate to Go)

**FastSlowStore write pattern:**
```go
// Write to L1 and L2 simultaneously, wait for both
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

**Thundering herd prevention on read:**
```go
// singleflight ensures only one goroutine fetches from S3 for a given key
func (c *Cache) get(ctx context.Context, key string) ([]byte, error) {
    if data, err := c.valkey.Get(ctx, key); err == nil {
        return data, nil  // L1 hit
    }
    v, err, _ := c.group.Do(key, func() (interface{}, error) {
        data, err := c.s3.Get(ctx, key)
        if err == nil {
            c.valkey.Set(ctx, key, data)  // populate L1 async
        }
        return data, err
    })
    return v.([]byte), err
}
```

**S3 multipart upload:** Use `aws-sdk-go-v2` `UploadManager` which handles multipart automatically.

**TTL-based expiry on AC:** Set S3 object metadata `x-amz-meta-written-at` on upload. On read, check age. If older than AC TTL → treat as miss.

### From Buildbarn (Go → directly usable patterns)

**Completeness checking on AC read:**
```go
func (c *Cache) getActionResult(ctx context.Context, hash string) (*repb.ActionResult, error) {
    ar, err := c.getRaw(ctx, "ac", hash)  // fetch ActionResult proto
    if err != nil { return nil, err }

    // Collect all CAS digests referenced by this ActionResult
    digests := collectDigests(ar)

    // Check all exist (parallel HeadObject to S3)
    missing, err := c.findMissing(ctx, digests)
    if err != nil { return nil, err }
    if len(missing) > 0 {
        return nil, status.Errorf(codes.NotFound, "AC entry references missing CAS blobs")
    }
    return ar, nil
}
```

**Rendezvous sharding** (if you add multi-node Valkey):
Use Buildbarn's algorithm directly. SHA256 of node key, splitmix64 mixer, fixed-point log2 score. No floating point.

### From buildfarm (Helm chart patterns)

- Bitnami Valkey chart as a dependency (`condition: valkey.enabled`)
- Auto-construct Valkey URI from release name: `valkey://{{ .Release.Name }}-valkey-master.{{ .Release.Namespace }}:6379`
- `podManagementPolicy: Parallel` on any StatefulSet
- `serviceMonitor.enabled: false` toggle in every component
- `extraVolumes`, `extraVolumeMounts`, `extraEnv` in every component
- PDB disabled by default, toggle to enable

---

## 8. Project Structure

```
open-cache/
├── cmd/
│   └── open-cache/
│       └── main.go              — entry point, config loading, server startup
├── internal/
│   ├── cache/
│   │   ├── cache.go             — Cache interface
│   │   ├── s3/
│   │   │   └── s3.go            — S3 store implementation
│   │   ├── valkey/
│   │   │   └── valkey.go        — Valkey L1 implementation
│   │   ├── tiered/
│   │   │   └── tiered.go        — FastSlowStore: L1+L2 coordination
│   │   └── completeness/
│   │       └── completeness.go  — AC completeness checking
│   ├── server/
│   │   ├── http.go              — Bazel Simple HTTP Cache handlers
│   │   └── grpc.go              — gRPC REAPI server
│   ├── config/
│   │   └── config.go            — Config struct, viper loading
│   └── metrics/
│       └── metrics.go           — Prometheus metrics registry
├── charts/
│   └── open-cache/              — Helm chart
│       ├── Chart.yaml
│       ├── values.yaml
│       └── templates/
├── Dockerfile
├── docker-compose.yml           — local dev with MinIO + Valkey
├── examples/
│   └── .bazelrc                 — example Bazel config to use this cache
└── README.md
```

---

## 9. Local Development Setup

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

Test with Bazel:
```
# .bazelrc
build --remote_cache=http://localhost:8080
build --remote_upload_local_results=true
```

---

## 10. What Success Looks Like (v1.0)

A Bazel build team can:
1. `helm install open-cache ./charts/open-cache --set s3.bucket=my-bucket` → running in 5 minutes
2. Add `--remote_cache=http://open-cache:8080` to `.bazelrc` → cache hits working
3. See Prometheus metrics out of the box
4. Run multiple cache pods behind a load balancer without cache inconsistency
5. Never worry about local disk filling up on cache pods
6. Never get a `CacheNotFoundException` mid-build (BWOB-safe)

---

## 11. Out of Scope (explicitly, forever)

- Remote execution (action scheduling, workers, sandboxing)
- Build event streaming / BES protocol
- Build analytics UI (v1 — metrics only via Prometheus)
- Mac remote execution
- Gradle, sccache, Turborepo support (v1 — REAPI only)
- Custom storage backends beyond S3 + Valkey (GCS, AzBlob are easy additions later)

---

## 12. Reference Reading

Before coding, read these in order:

1. **Bazel Simple HTTP Cache protocol**: https://bazel.build/remote/caching#http-caching-protocol
2. **REAPI proto definitions**: https://github.com/bazelbuild/remote-apis/blob/main/build/bazel/remote/execution/v2/remote_execution.proto
3. **Build Without the Bytes (BwoB)**: https://blog.bazel.build/2023/10/06/bwob-in-bazel-7.html
4. **bazel-remote source** (what not to do / what to improve on): https://github.com/buchgr/bazel-remote
5. **NativeLink FastSlowStore** (the tiering pattern): `/tmp/nativelink/nativelink-store/src/fast_slow_store.rs`
6. **Buildbarn completeness checking** (BWOB fix): `/tmp/bb-storage/pkg/blobstore/completenesschecking/completeness_checking_blob_access.go`
7. **buildfarm Helm values** (chart structure reference): `/tmp/buildfarm/kubernetes/helm-charts/buildfarm/values.yaml`

---

*Research backing this brief: RESEARCH.md, ANALYSIS_NATIVELINK.md, ANALYSIS_BUILDBARN.md, ANALYSIS_BUILDFARM.md*
