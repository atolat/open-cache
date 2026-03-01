# Bazel Remote Cache Ecosystem Research

> Research conducted February 2026. Intended to inform the design of a new open-source,
> K8s-native, S3-primary Bazel remote cache project.

---

## Table of Contents

1. [Open Source Projects](#1-open-source-projects)
2. [Commercial Offerings](#2-commercial-offerings)
3. [DIY Patterns & Reference Architectures](#3-diy-patterns--reference-architectures)
4. [Ecosystem Gaps & Community Pain Points](#4-ecosystem-gaps--community-pain-points)
5. [bazel-remote: Deep Technical Analysis](#5-bazel-remote-deep-technical-analysis)
6. [Multi-Build-System Support Matrix](#6-multi-build-system-support-matrix)
7. [Kubernetes / EKS Native Status](#7-kubernetes--eks-native-status)
8. [Summary: Where a New Project Fits](#8-summary-where-a-new-project-fits)

---

## 1. Open Source Projects

### bazel-remote (Go)

- **Repo**: https://github.com/buchgr/bazel-remote
- **Protocol**: HTTP/1.1 REST + gRPC (REAPI v2), Byte Stream API
- **Architecture**: Single binary. Disk-first LRU cache. S3/GCS/AzBlob/HTTP/gRPC as async proxy backends (secondary tier only).
- **K8s**: Stale third-party Helm chart on Artifact Hub. Official `kubernetes.yml` example in repo. Buildkite has Terraform/Pulumi IaC for ECS Fargate.
- **Activity**: ~926 commits, 713 stars, 182 forks. Actively maintained.
- **Complexity**: Low. Single binary, YAML or CLI flag config.
- **Key limitations**: See [Section 5](#5-bazel-remote-deep-technical-analysis) for full deep-dive.

---

### Buildbarn (Go)

- **Repo**: https://github.com/buildbarn (org with multiple repos)
- **Protocol**: REAPI v2 (cache + remote execution)
- **Architecture**: Microservices. Separate daemons: `bb-storage`, `bb-scheduler`, `bb-worker`, `bb-browser`. Stateless frontends fan out to sharded storage daemons.
- **Key features**:
  - Full remote execution, not just caching
  - Platform-aware scheduling ("size classes")
  - mTLS between all components
  - Compatible with Bazel, Buck2, Pants, BuildStream, Goma/recc
  - **Bonanza**: Experimental successor that pushes analysis + Starlark evaluation into the remote cluster (eliminates Bazel cold analysis). Highly experimental as of March 2025.
- **K8s**: Via `bb-deployments/kubernetes` using Jsonnet. Tweag published a production-grade mTLS guide (August 2024). Aspect Build operates it commercially as "Managed BuildBarn."
- **Complexity**: Very high. Multiple services, cert-manager, Jsonnet config. No one-click Helm install.

---

### buildfarm (Java)

- **Repo**: https://github.com/buildfarm/buildfarm (forked from bazelbuild/bazel-buildfarm)
- **Protocol**: REAPI v2 (cache + remote execution)
- **Architecture**: Server + Worker + Redis. Redis (7.2.4+) for metadata. Cassandra referenced for distributed persistence.
- **Key features**:
  - Official OCI Helm chart: `helm install ... oci://ghcr.io/buildfarm/buildfarm`
  - `topologySpreadConstraints`, annotations for Server/ShardWorker
  - amd64 + arm64 Docker images
  - Compatible with Bazel, Buck2, Pants
- **K8s**: Best official Helm support of all OSS REAPI servers.
- **Activity**: v2.16.0 released February 2026. 2,402 commits, 755 stars, 231 forks.
- **Complexity**: Medium-high. Java, requires Redis, YAML config.

---

### NativeLink (Rust)

- **Repo**: https://github.com/TraceMachina/nativelink
- **Protocol**: REAPI v2 (cache + remote execution)
- **Architecture**: Single configurable binary. No GC pauses. Async-first via Tokio. Redis backend for scheduler HA. Pluggable store backends.
- **Key features**:
  - Supports Bazel, Buck2, Pants, Soong (AOSP), Goma, Reclient
  - OriginEvent API for tracing
  - 1B+ requests/month in production (Samsung)
  - v0.8.0 released February 2026
- **K8s**: Kubernetes deployment configs in repo. No official Helm chart.
- **Activity**: 1,359 commits, 1.5k stars, 206 forks. Very actively maintained. Commercial product (NativeLink Cloud) built on top.
- **Complexity**: Medium. Single binary. JSON-based config more complex than bazel-remote.

---

### BuildGrid (Python)

- **Repo**: https://buildgrid.gitlab.io/buildgrid/
- **Protocol**: REAPI v2 (cache + remote execution)
- **Architecture**: Python-based reference implementation.
- **Activity**: Maintained but limited community traction. GIL and performance limit throughput at scale.
- **Complexity**: Medium.

---

### nginx + WebDAV (DIY Minimal)

- Official Bazel documentation supports nginx with `nginx-extras` WebDAV module as a minimal cache.
- HTTP REST only (no gRPC/REAPI). No LRU eviction. No S3 backend. No metrics.
- Suitable only for very small teams or POC.

---

### bazels3cache (Archived)

- **Repo**: https://github.com/Asana/bazels3cache — **archived December 2022**
- Was a Node.js WebDAV proxy to S3 with async uploads and optional in-memory cache.
- Historical data point: the thin-proxy-to-S3 pattern is now subsumed by bazel-remote's native S3 backend (though bazel-remote's S3 support has significant limitations — see Section 5).

---

## 2. Commercial Offerings

### BuildBuddy

- **Model**: SaaS + self-hosted open source tier
- **Backend**: Custom caching infrastructure. ~2ms avg read latency vs ~100ms with direct S3.
- **Free tier**: 10 users, 100 GB cache transfer/month, 80 Linux cores for RBE, community support only.
- **Enterprise**: Unlimited cache/cores, dedicated support, SSO/SAML, isolated infra, Mac cores ($45/core).
- **Notable**: Build event viewer, result store, cache analytics, per-project cache partitioning, Prometheus metrics. Official Helm chart (`buildbuddy-io/buildbuddy-helm`).
- **Limitation**: Bazel-only.

---

### EngFlow

- **Model**: Hosted SaaS + self-hosted enterprise. Quote-based pricing.
- **Free tier**: Single-machine RBE, in-cluster caching only, Bazel on Linux only, max 1 machine / 32 cores.
- **Enterprise**: 100,000+ cores, external S3/GCS backends, multi-platform (Linux/macOS/Windows), SAML/OAuth/IAM, 99.9% SLA.
- **Supports**: Bazel, Goma (Chromium), Soong (AOSP).
- **2025**: Acquired tipi.build (CMake/C++ ecosystem). Customers include Arm, Asana, BMW, Canva, Databricks, Lyft, Perplexity, Snap.
- **Limitation**: Bazel/Goma/Soong only. No Gradle, no Go, no sccache.

---

### Aspect Build (Managed BuildBarn + Workflows)

- **Model**: CI platform built on Buildbarn + Bazel ruleset expertise.
- **Approach**: Wraps Buildbarn with managed operations. Every Workflows deployment includes REAPI v2 cache; RBE available with minimal additional config. SSO/OIDC for developer machine cache access.
- **Pricing**: 30-day trial; enterprise pricing on request.
- **Notable**: Physical Intelligence case study: time-to-robot reduced from 1.5 hours to 5 minutes.

---

### Depot Cache

- **Model**: SaaS, CDN-backed globally distributed cache nodes.
- **Supported**: Bazel, Go, Gradle, Turborepo, sccache, Pants, moonrepo — **broadest multi-system support of any commercial offering**.
- **Architecture**: Routes traffic to nearest CDN node. Native GitHub Actions runner integration.
- **Pricing**: 7-day trial; not public.
- **Notable**: Cache-only (no RBE), which the team argues is safer and simpler.

---

### Develocity (formerly Gradle Enterprise)

- **Model**: Enterprise SaaS + on-prem.
- **Supported**: Gradle, Maven, Bazel, SBT — unique multi-system analytics.
- **Key differentiator**: Richest analytics layer of any offering. Per-task cache key inspection, cache hit/miss breakdown, download timing, geographic replication.
- **Pricing**: Enterprise sales only.

---

### Buildless

- **Model**: SaaS, Cloudflare-powered global CDN.
- **Supported**: Gradle, Maven, Bazel, CCache, Turbo, sccache.
- **Approach**: "World's first build cache CDN." Config is just an endpoint + API key.
- **Status**: Active as of late 2023/early 2024. Long-term funding/viability unclear.

---

## 3. DIY Patterns & Reference Architectures

### Pattern 1: bazel-remote on ECS/Fargate + S3

Most common well-documented pattern. Reference: [Buildkite blog + IaC](https://buildkite.com/resources/blog/setting-up-a-self-hosted-bazel-remote-cache-on-aws-with-terraform/).

- ECS Fargate running `buchgr/bazel-remote-cache` with FluentBit sidecar
- EBS for local disk (primary LRU), S3 for durable async proxy backend
- ALB with gRPC health checks, htpasswd + HTTPS or mTLS auth, zstd compression

**Critical operational note**: Do NOT round-robin load balance across multiple bazel-remote instances with local disk — cache becomes inconsistent. Use a single instance, or shared network storage (EFS, JuiceFS) behind a load balancer, or tiered caching (smaller instances as proxies to a central large instance).

---

### Pattern 2: bazel-remote + MinIO (air-gapped)

MinIO provides S3-compatible API, bazel-remote's S3 backend works unmodified. Popular in on-prem / air-gapped enterprise environments.

---

### Pattern 3: Direct GCS/S3 (no proxy)

Bazel can hit GCS or S3 directly via its HTTP cache backend.
- GCS/S3 P50 latency ~100ms vs ~2ms for purpose-built cache
- HTTP/1.1 connection overhead per request is significant (many small AC metadata files)
- No LRU eviction — bucket grows unbounded without lifecycle policies

---

### Pattern 4: nginx reverse proxy in front of S3

Pre-bazel-remote era pattern. Essentially what bazels3cache was. Now obsolete.

---

### Pattern 5: Tweag Nix+Bazel Remote Execution

[Reference repo](https://github.com/tweag/nix-bazel-remote-execution-infra) + [mTLS Buildbarn on K8s guide](https://www.tweag.io/blog/2024-08-29-buildbarn-mtls/) with [bb-deployments fork](https://github.com/tweag/bb-deployments/tree/buildbarn-mtls-blog/kubernetes). Production-grade but complex.

---

## 4. Ecosystem Gaps & Community Pain Points

### From bazelbuild/bazel GitHub Issues

| Issue | Status | Description |
|---|---|---|
| **#2964** | Open since 2017 | Cache inaccessibility should fall back gracefully instead of failing the build |
| **#4757** | Open | Uploads should be non-blocking; builds stall waiting for cache uploads |
| **#6091** | Open | Actions with large output file counts cause severe slowdown (11,000 files → 50s-2min serial upload) |
| **#7664** | Open | "Remote cache is not a clear win" — slow networks make cache slower than local builds |
| **#11801** | Open | gRPC concurrency limited by single connection per IP (MAX_CONCURRENT_STREAMS ~100-128), bottlenecking parallel builds |
| **#19348** | Open | `CacheNotFoundException` when cache evicts objects between AC write and CAS read (BWOB race condition) |
| **#21777** | Open | Cache eviction during active build hangs action evaluation |
| **#22119** | Open | Transient remote cache errors cause hard build failures instead of logged warnings |
| **#23033** | Open | Download errors from remote cache fail the build |

### Structural Ecosystem Gaps

1. **No open-source build analytics UI.** All dashboards are commercial (BuildBuddy, EngFlow, Develocity). bazel-remote has Prometheus metrics but no UI. Buildbarn has `bb-browser` (limited).

2. **No Kubernetes operator.** All solutions are Deployment/StatefulSet based. No CRD-driven lifecycle management for any OSS cache.

3. **No production-ready Helm chart for Buildbarn or NativeLink.** The two most technically sophisticated OSS RE systems lack first-class Helm packaging.

4. **No L1 in-memory/Valkey/Redis cache layer in any open-source offering.** Tiered caching requires manual proxy chaining with bazel-remote or custom NativeLink store config.

5. **No open-source CDN/edge cache.** Only commercial (Depot, Buildless, Develocity with replication).

6. **Remote analysis caching (Skycache) does not exist in open-source Bazel.** Bonanza is experimental. Every Bazel server restart re-runs analysis.

7. **BWOB + eviction race is an open protocol-level problem.** No server-side solution that prevents AC/CAS divergence under eviction pressure. `--experimental_remote_cache_eviction_retries` partially addresses this client-side.

8. **Mac remote execution is cost-prohibitive.** Warm cache for Mac developers requires Mac CI workers. Most RBE setups are Linux-only.

9. **gRPC single-connection bottleneck.** Bazel opens one gRPC connection per resolved IP. With MAX_CONCURRENT_STREAMS typically 100-128, `--jobs=200` builds are bottlenecked.

10. **S3 as a primary backend doesn't exist in any OSS project.** Every OSS cache either requires local disk (bazel-remote) or uses its own internal store (Buildbarn, NativeLink, buildfarm). No project treats S3 as a first-class primary store.

11. **Cache poisoning risk.** AC write access from developer machines allows supply-chain attacks. Most self-hosted setups don't enforce read-only for devs / write-only from post-merge CI.

---

## 5. bazel-remote: Deep Technical Analysis

> Based on direct code analysis of the repository at commit HEAD, February 2026.

### 5.1 S3 is Structurally a Secondary Proxy — Never Primary

S3 lives in `cache/s3proxy/s3proxy.go` and implements the `cache.Proxy` interface:

```go
type Proxy interface {
    Put(ctx context.Context, kind EntryKind, hash string, size int64, sizeOnDisk int64, rc io.ReadCloser)
    Get(ctx context.Context, kind EntryKind, hash string, size int64, offset int64) (io.ReadCloser, int64, error)
    Contains(ctx context.Context, kind EntryKind, hash string, size int64) bool
}
```

There is **no Delete method** — S3 objects are never evicted or cleaned up by bazel-remote.

S3 writes are fire-and-forget: after writing to disk, the S3 upload is enqueued into a buffered channel and returns immediately. If the queue is full, the upload is **silently dropped**:

```go
default:
    c.errorLogger.Printf("too many uploads queued\n")
    _ = rc.Close()
```

**Disk is mandatory, not optional.** `config/config.go` hard-fails if `dir` is not set:

```go
if c.Dir == "" {
    return errors.New("the 'dir' flag/key is required")
}
```

`main.go` calls `disk.New()` unconditionally before any servers start. `disk.New()` creates 768 subdirectories and scans the full directory tree before accepting connections. There is no code path that bypasses disk initialization.

**Startup blocks all connections** until the LRU is fully built from disk. On large caches this can take minutes. No warm-up mode.

---

### 5.2 HTTP Limitations

**Endpoints:**
```
GET/PUT/HEAD  /[instance/]ac/{sha256}  — Action Cache
GET/PUT/HEAD  /[instance/]cas/{sha256} — Content Addressable Storage
GET           /status                   — Disk-only stats (no S3 state)
GET           /metrics                  — Prometheus (if enabled)
```

**Protocol**: bazel-remote's own custom REST protocol. Compatible with Bazel's Simple HTTP Caching Protocol but adds extensions (zstd, JSON AC, instance name prefix).

**Known limitations:**
- `Content-Length` is mandatory for PUT. Chunked/streaming uploads without declared size return HTTP 400.
- Compressed GET responses (zstd) have **no Content-Length header** (TODO in `server/http.go:269`).
- **No range requests.** Offset is hardcoded to 0 in all HTTP handlers.
- **No batch operations over HTTP.** Batch reads/writes are gRPC-only.
- No ETag, Last-Modified, If-None-Match, or Cache-Control semantics.
- `/status` reports disk stats only — no S3 connectivity, hit rates, or backend state.

---

### 5.3 gRPC / REAPI Implementation

**Registered services:**
- `ActionCache` — fully implemented
- `ContentAddressableStorage` — partially implemented
- `Capabilities` — implemented (SHA256 only, no SHA384/SHA512/BLAKE3)
- `ByteStream` — implemented with limitations
- `RemoteAsset.Fetch` — experimental, partial
- `Execution` — **not registered**. bazel-remote is purely a cache.

**CAS service gaps:**
- `BatchUpdateBlobs` — **sequential processing** (TODO: `// consider fanning-out goroutines here`)
- `BatchReadBlobs` — **sequential processing** (same TODO)
- `GetTree` — **no pagination**, dumps all directories in one response (TODO in code)
- `SplitBlob` — returns `codes.Unimplemented`
- `FindMissingBlobs` — proxy check workers hardcoded at 512, queue at 2048, not configurable

**ByteStream gaps:**
- **Resumable writes not supported.** Non-zero write offsets hard-fail: `"bytestream writes from non-zero offsets are unsupported"`
- `QueryWriteStatus` returns binary state only (0 or full size)

**Remote Asset API (experimental):**
- `FetchBlob` — partial. Supports SHA256 checksum qualifier and HTTP/HTTPS URIs only.
- `FetchDirectory` — returns `nil, nil` (stub, not implemented)
- `PushBlob` / `PushDirectory` — commented out entirely
- `fetchItem` uses `http.DefaultClient` — **zero timeout**, no custom transport. A hung external server blocks a goroutine indefinitely.

---

### 5.4 Connection Pooling to S3

Uses minio-go client with a standard `*http.Transport`:

```go
tr.MaxIdleConns = MaxIdleConns
tr.MaxIdleConnsPerHost = MaxIdleConns
```

`MaxIdleConns` comes from `s3.max_idle_conns` config. **The "default: 1024" in documentation is `DefaultText` only — not an actual default.** If the config key is absent, Go's zero value `0` is passed, resulting in Go's built-in defaults: 100 global idle connections, 0 per-host limit.

No `IdleConnTimeout`, `ResponseHeaderTimeout`, or `DialContext` timeout is configured for S3 operations.

For HTTP proxy backends using HTTPS: a bare `&http.Transport{TLSClientConfig: config}` is created — no idle connection limits, no timeout configuration.

---

### 5.5 LRU Eviction

**LRU only applies to disk.** S3 objects are never evicted (no Delete in the Proxy interface).

The `SizedLRU` (`cache/disk/lru.go`) is an in-memory doubly-linked list + hashmap:
- Evictions are **asynchronous**: evicted entries are placed into a channel; a single background goroutine performs `os.Remove` calls sequentially.
- Two limits: `maxSize` (triggers eviction) and `maxSizeHardLimit` (rejects new writes with HTTP 507).
- Size tracking is based on `sizeOnDisk` rounded up to 4096-byte blocks, not logical blob sizes.
- Startup: all files scanned, sorted by atime, added to LRU oldest-first. If usage exceeds `max_size`, eviction runs to completion before the server accepts connections.
- **No coordination with S3.** When disk evicts a blob, nothing notifies S3. The blob persists in S3 indefinitely. On next request for that blob, bazel-remote fetches it synchronously from S3 and re-stores to disk.

---

### 5.6 Source Code TODOs (Selected)

| File | Line | TODO |
|---|---|---|
| `cache/disk/findmissing.go` | 238 | 512 workers / 2048 queue for proxy `Contains` checks are hardcoded — not configurable |
| `cache/disk/load.go` | 228, 311 | Cache migration doesn't work across filesystems |
| `cache/disk/zstdimpl/gozstd.go` | 15–16 | zstd encoder/decoder run at default (single-threaded) concurrency |
| `server/http.go` | 269 | Compressed GET responses lack Content-Length header |
| `server/grpc_cas.go` | 85, 265 | `BatchUpdateBlobs` and `BatchReadBlobs` are sequential, not parallelized |
| `server/grpc_cas.go` | 329 | `GetTree` sends all directories in one response — no pagination |
| `server/grpc_cas.go` | 228 | Inconsistent error codes between zstd and non-zstd paths in `BatchReadBlobs` |
| `server/grpc_ac.go` | 276 | Inconsistent AC inlining behavior |
| `main.go` | 501 | Unauthenticated write attempts are not logged |

---

### 5.7 Configuration Constraints

- **At most one proxy backend.** Only one of S3/GCS/AzBlob/HTTP/gRPC proxy can be configured at a time.
- **No disk-free mode.** `dir` and `max_size` are required fields.
- **No configurable timeouts for proxy operations.**
- **No retry configuration** for S3/proxy operations.
- **No configurable worker pool sizes** for `FindMissingBlobs` (hardcoded).
- **Only SHA256** digest function supported.

---

## 6. Multi-Build-System Support Matrix

> Note: REAPI protocol is used by Bazel, Buck2, Pants, Goma, Reclient, Soong.
> Gradle, sccache, Turborepo, Go use entirely different cache protocols.
> Sharing infrastructure across both groups requires a purpose-built multi-protocol service.

| Solution | Bazel | Buck2 | Pants | Gradle | sccache | Go | Turborepo |
|---|---|---|---|---|---|---|---|
| bazel-remote | Yes | Yes | Yes | No | No | No | No |
| buildfarm | Yes | Yes | Yes | No | No | No | No |
| Buildbarn | Yes | Yes | Yes | No | No | No | No |
| NativeLink | Yes | Yes | Yes | No | No | No | No |
| **Depot Cache** | Yes | No | Yes | Yes | Yes | Yes | Yes |
| **Buildless** | Yes | No | No | Yes | Yes | No | Yes |
| BuildBuddy | Yes | No | No | No | No | No | No |
| EngFlow | Yes | No | No | No | No | No | No |
| Develocity | Yes | No | No | Yes | No | No | No |

---

## 7. Kubernetes / EKS Native Status

| Solution | Helm Chart | K8s Maturity | Notes |
|---|---|---|---|
| bazel-remote | Third-party (stale) | Low-medium | Official `kubernetes.yml` example; no operator; Buildkite IaC for ECS |
| buildfarm | Official OCI Helm | High | Best official Helm; `topologySpreadConstraints`; amd64/arm64 |
| Buildbarn | No Helm | Medium-high | Jsonnet in `bb-deployments`; Tweag mTLS guide; Aspect Build operates it commercially |
| NativeLink | No Helm | Medium | K8s manifests in repo; commercial NativeLink Cloud built on top |
| BuildBuddy | Official Helm | High | Best managed-service Helm experience |
| EngFlow | N/A (SaaS) | N/A | — |

**EKS-specific notes:**
- bazel-remote supports gRPC health checks compatible with AWS ALB ingress
- No Kubernetes operator exists for any OSS cache — all are Deployment/StatefulSet based, no CRD-driven lifecycle
- NativeLink and Buildbarn both lack Helm charts despite being the most technically capable OSS RE systems

---

## 8. Summary: Where a New Project Fits

### The gap in one sentence

There is no open-source Bazel cache that:
- Treats **S3 as a first-class primary backend** (not an async proxy)
- Has a **tiered L1 in-memory cache** (Valkey/Redis) for hot blobs
- Deploys natively on **K8s/EKS with a working Helm chart**
- Requires **no local disk** to operate
- Is **simple enough to deploy in under an hour**

### Competitive positioning

| Project | Closest to new project | Key difference |
|---|---|---|
| bazel-remote | Most similar in scope | Disk-first, S3 is secondary proxy, no L1 memory tier, stale K8s story |
| buildfarm | Has Helm, but... | Full RBE platform, Java, Redis-required, much more complex |
| NativeLink | Technically sophisticated | Full RBE platform, Rust, no Helm, complex config |
| BuildBuddy (OSS tier) | Good UX | SaaS-first, no S3-primary mode, Bazel-only, no K8s operator |

### Unique value propositions for a new project

1. **S3 as primary store** — no mandatory disk, works natively with EKS node ephemeral storage as optional spill-over
2. **Valkey L1 / shared memory tier** — hot blob cache at microsecond latency before S3 (milliseconds)
3. **Emulated LRU on S3** — proper eviction coordination between tiers, not "S3 grows forever"
4. **Connection pooling first-class** — tuned nginx upstream pools, configurable S3 client pool, not afterthought
5. **Production-grade Helm chart** — opinionated EKS/K8s defaults that actually work out of the box
6. **Build-system aware HTTP** — proper Bazel HTTP caching protocol with all headers, compression, range requests
7. **Extensible to other build systems** — REAPI-compatible (Buck2, Pants) from day one; optional sccache/Gradle adapters later

### Known risks

- Maintenance burden once public
- BuildBuddy and EngFlow are polished commercial products with free tiers — need a clear "why self-host" story
- Multi-build-system support (Gradle, sccache) requires a distinct protocol layer — large surface area, worth scoping carefully
- BWOB + eviction race is a hard protocol-level problem; any cache with aggressive S3 eviction needs a clear answer for this

---

*Research compiled from: GitHub source code analysis (bazel-remote), GitHub issues (bazelbuild/bazel), project documentation (Buildbarn, NativeLink, buildfarm, BuildBuddy, EngFlow, Depot, Buildless, Develocity), Buildkite blog, Tweag blog, EngFlow blog, MobileNativeFoundation discussions, Bazel blog.*
