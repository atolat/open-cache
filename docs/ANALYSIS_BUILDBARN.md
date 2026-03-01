# Buildbarn: Deep Technical Analysis

> Based on direct source code analysis of:
> - https://github.com/buildbarn/bb-storage (HEAD, Feb 2026)
> - https://github.com/buildbarn/bb-deployments (HEAD, Feb 2026)
>
> Focused on patterns relevant to building an S3-primary, K8s-native, tiered Bazel remote cache.

---

## 1. BlobAccess: The Core Interface

`pkg/blobstore/blob_access.go` — the single interface everything is built on.

```go
type BlobAccess interface {
    capabilities.Provider
    Get(ctx context.Context, digest digest.Digest) buffer.Buffer
    GetFromComposite(ctx context.Context, parentDigest, childDigest digest.Digest, slicer slicing.BlobSlicer) buffer.Buffer
    Put(ctx context.Context, digest digest.Digest, b buffer.Buffer) error
    FindMissing(ctx context.Context, digests digest.Set) (digest.Set, error)
}
```

**Key design decisions:**
- `Get` returns a `buffer.Buffer` — an abstract, lazy handle to the data, not an `io.Reader`. The buffer can be consumed once, cloned, discarded, or converted to various forms (proto, byte slice, reader). This enables efficient zero-copy paths.
- `GetFromComposite` fetches a child digest from a parent blob using a `BlobSlicer` — supports CAS decomposition (ADR#3) without changing the interface.
- `FindMissing` takes and returns `digest.Set` (not a slice) — deduplication is baked in.
- The TODO in `blob_access.go` notes that `FindMissing` should ideally be a streaming API to handle batching transparently when sharding is in use. Currently callers must manually batch to `RecommendedFindMissingDigestsCount` (10,000).

---

## 2. Backend Composition: All Backends via Configuration

`pkg/blobstore/configuration/new_blob_access.go` — all backend types composed via protobuf configuration. The factory function `newNestedBlobAccessBare` is a large switch statement over protobuf oneof variants. Each backend is transparently wrapped with `MetricsBlobAccess` for Prometheus instrumentation.

**Available backends:**

| Backend | Description |
|---|---|
| `Local` | Custom block device format (in-memory or block device backed) |
| `ReadCaching` | Slow/fast with background replication |
| `ReadFallback` | Primary/secondary with replication on miss |
| `Sharding` | Rendezvous hash sharding across N backends |
| `Mirrored` | Bidirectional replication across 2 backends |
| `Demultiplexing` | Route to different backends by instance name prefix |
| `ReadCanarying` | Shadow-reads to a replica for comparison |
| `DeadlineEnforcing` | Wraps any backend with a per-operation timeout |
| `ZipReading` | Read-only ZIP file as a cache backend |
| `ZipWriting` | Write-only ZIP file (for export/archival) |
| `gRPC` (client) | Any remote BlobAccess over gRPC |
| `Error` | Always returns a configured error |

**Critical gap: No S3 backend.** Buildbarn has no S3 or cloud storage backend. All durable storage uses the `Local` backend (block device files on disk or tmpfs) or gRPC to another Buildbarn instance. This is an architectural philosophy — Buildbarn owns the storage format completely.

---

## 3. Local Storage: Custom Block Device Format

`pkg/blobstore/local/` — Buildbarn does not use the filesystem for blob storage. It implements its own block-structured storage engine on top of raw block device files.

### Architecture
- Storage is divided into **blocks** of configurable size.
- Blocks are categorized: `OldBlocks`, `CurrentBlocks`, `NewBlocks`, `SpareBlocks`.
- New writes go to new blocks. When new blocks fill, the oldest old blocks are recycled (ring buffer eviction).
- A **key-location map** (separate block device or in-memory) maps `hash → (block_index, offset)`.
- The key-location map uses FNV-1a hashing with a prime-sized table for best dispersion.
- A **persistent state** file records epoch IDs and block layout, enabling crash recovery without full scan.

### Key-Location Map
Two implementations:
- `KeyLocationMapInMemory`: pure in-memory hash table.
- `KeyLocationMapOnBlockDevice`: memory-mapped block device — survives process restarts.

Lookup attempts: `keyLocationMapMaximumGetAttempts` (default 16). Put attempts: `keyLocationMapMaximumPutAttempts` (default 64). These control how many probes the open-addressing hash table performs before giving up.

### Persistence
Epoch-based: every `minimumEpochInterval` (default 300s in K8s config), a background goroutine flushes the persistent state (block layout, key-location map hash seed) to disk. This means:
- Crash recovery replays only since the last epoch, not since the beginning.
- Cache is **not** lost on restart — state is restored from the persistent state file.

### Reference: K8s Config (from storage.jsonnet)
```jsonnet
contentAddressableStorage: {
  backend: {
    local: {
      keyLocationMapOnBlockDevice: {
        file: { path: '/storage-cas/key_location_map', sizeBytes: 400 * 1024 * 1024 }
      },
      keyLocationMapMaximumGetAttempts: 16,
      keyLocationMapMaximumPutAttempts: 64,
      oldBlocks: 8, currentBlocks: 24, newBlocks: 3,
      blocksOnBlockDevice: {
        source: { file: { path: '/storage-cas/blocks', sizeBytes: 32 * 1024 * 1024 * 1024 } },
        spareBlocks: 3,
      },
      persistent: {
        stateDirectoryPath: '/storage-cas/persistent_state',
        minimumEpochInterval: '300s',
      },
    }
  }
}
```
CAS: 32GB block device. AC: 20MB block device. Key-location map: 400MB.

---

## 4. BWOB / AC-CAS Consistency: The Complete Solution

`pkg/blobstore/completenesschecking/completeness_checking_blob_access.go`

Buildbarn's solution to the BWOB race condition is the `CompletenessCheckingBlobAccess` wrapper around the AC.

**On every `Get(AC)` call:**
1. Fetch the `ActionResult` proto from the underlying AC store.
2. Clone the buffer (so it can be returned if valid).
3. Collect all `remoteexecution.Digest` fields from the ActionResult:
   - All `output_files[].digest`
   - All `output_directories[].tree_digest` and `root_directory_digest`
   - `stdout_digest`, `stderr_digest`
4. For each `OutputDirectory`: fetch the `Tree` proto from CAS, parse it, collect all file digests from all nested directories recursively.
5. Run `FindMissing` in batches (`batchSize`) across all collected digests.
6. If any digest is missing → return `codes.NotFound` for the AC entry (cache miss, trigger rebuild).
7. If all present → return the cloned ActionResult buffer.

**This is correct.** The AC entry itself is never deleted — only invalidated at read time. The CAS and AC evict independently, and the completeness check prevents returning broken cache hits.

**Cost:** Each AC hit involves:
- 1 AC read (ActionResult proto)
- N CAS `FindMissing` calls (batched to `batchSize`) where N = number of output files + output directories
- For each output directory: 1 CAS read (Tree proto) + M more CAS digests

For large actions with hundreds of output files, this is significant overhead. Buildbarn accepts this cost for correctness.

---

## 5. Sharding: Rendezvous Hashing

`pkg/blobstore/sharding/rendezvous_shard_selector.go`

Buildbarn uses Rendezvous (HRW — Highest Random Weight) hashing for shard selection.

```go
func (s *rendezvousShardSelector) GetShard(hash uint64) int {
    var best uint64
    for _, shard := range s.shards {
        mixed := splitmix64(shard.hash ^ hash)
        current := score(mixed, shard.weight)
        if current > best { best = current; bestIndex = shard.index }
    }
    return bestIndex
}
```

**Properties:**
- Shard key is SHA256 of a string key, truncated to uint64.
- `splitmix64` mixer combines shard hash with blob hash.
- `score()` approximates `-weight / log2(x)` in fixed-point arithmetic (no floating point — deterministic on all platforms).
- **Adding a shard**: only blobs that would map to the new shard are affected.
- **Removing a shard**: only blobs that mapped to that shard are affected (minimal disruption).
- **Reordering shards**: no effect (order-independent).

This is the correct algorithm for cache sharding. Consistent hashing rings have more disruption on rebalancing; rendezvous hashing is simpler and achieves better minimal disruption.

Collision detection: if two shard keys have the same SHA256 prefix, construction fails with an error.

---

## 6. Mirroring: Bidirectional Replication

`pkg/blobstore/mirrored/mirrored_blob_access.go`

`MirroredBlobAccess` wraps two backends (A and B) with bidirectional replication.

**Read path:**
- Alternates between A and B via atomic round-robin (`round.Add(1)%2`).
- On a miss in the selected backend, falls back to the other.
- On fallback hit, replicates the blob back to the originally-selected backend asynchronously.
- Prometheus metrics track synchronizations (A→B and B→A directions separately).

**Write path:**
- Writes to both A and B concurrently using `errgroup`.
- Both writes must succeed (no partial write tolerance).

**FindMissing path:**
- Runs FindMissing on both backends concurrently.
- Blobs missing in A but present in B → scheduled for A←B replication.
- Blobs missing in B but present in A → scheduled for B←A replication.
- Returns only blobs missing in both.

**Key insight:** The mirrored backend self-heals — after any split-brain or node failure, subsequent FindMissing calls will repair inconsistencies via the replicator. No external reconciliation job needed.

---

## 7. ReadCaching: L1/L2 Pattern

`pkg/blobstore/readcaching/` — provides a `ReadCachingBlobAccess(slow, fast, replicator)`.

- **Read**: check fast first. On miss, read from slow and replicate to fast asynchronously.
- **Write**: write to slow only (fast is populated lazily on read).
- **FindMissing**: check slow only (authoritative).

This is used in production to put a small local `Local` store in front of a large remote gRPC store. The replication happens in a background goroutine — the client gets the response from slow while replication runs.

**Difference from NativeLink FastSlowStore:** Buildbarn's ReadCaching only writes to the slow store (fast is read-only from the write perspective). NativeLink's FastSlowStore writes to both simultaneously. For a cache use case where the slow store is S3, Buildbarn's approach means: S3 is always the authoritative write target; local is just a read cache. NativeLink's approach requires both to succeed.

---

## 8. ReadFallback: Hot/Cold Tiering

`pkg/blobstore/readfallback/` — similar to ReadCaching but with different semantics.

- **Read**: primary first. On miss, try secondary. On secondary hit, replicate to primary asynchronously.
- **Write**: primary only.
- **FindMissing**: primary only.

Used when primary is expected to have everything (hot) and secondary is a fallback (cold/overflow). The replicator promotes blobs from cold to hot on access.

---

## 9. Kubernetes Deployment Patterns (bb-deployments)

`kubernetes/` directory — production reference deployment.

### Services
```
bb-namespace.yaml     — Kubernetes namespace
storage.yaml          — StatefulSet: 2 replicas, PVCs for CAS+AC
frontend.yaml         — Deployment: stateless gRPC frontends
scheduler.yaml        — Deployment: action scheduler
worker-ubuntu22-04.yaml — StatefulSet: execution workers
browser.yaml          — Deployment: bb-browser analytics UI
kustomization.yaml    — Kustomize root
```

### Storage StatefulSet (storage.yaml)
```yaml
kind: StatefulSet
replicas: 2
containers:
  - image: ghcr.io/buildbarn/bb-storage:20250827T121715Z-fbb7c11
    args: [/config/storage.jsonnet]
    volumeMounts:
      - mountPath: /storage-cas  (PVC: 33Gi ReadWriteOnce)
      - mountPath: /storage-ac   (PVC: 1Gi ReadWriteOnce)
      - mountPath: /config/      (ConfigMap: common.libsonnet + storage.jsonnet)
initContainers:
  - name: volume-init
    image: busybox:1.31.1-uclibc
    command: [sh, -c, "mkdir -m 0700 -p /storage-cas/persistent_state /storage-ac/persistent_state"]
```

Key details:
- **StatefulSet with PVCs** — because storage is local block device files, pods are stateful.
- **Init container** creates persistent state directories — simple but important (missing dirs crash the process).
- **No resource limits in the base config** — operators are expected to set these per cluster.
- **Config via ConfigMap mounting Jsonnet files** — single source of truth for storage topology.
- **headless Service** (`clusterIP: None`) — storage pods are accessed by pod DNS name, not load-balanced.
- **Prometheus scraping annotations** on the Service.

### Frontend Deployment
Stateless gRPC frontends that proxy to the storage StatefulSet pods by DNS. This is the Buildbarn scaling model: scale frontends horizontally, keep storage pods at 1 per shard.

### Config Format: Jsonnet
All configuration is Jsonnet, not YAML. `common.libsonnet` provides shared values (max message size, global tracing config). Each component imports it. This enables DRY configuration across services but adds a Jsonnet dependency.

---

## 10. gRPC Patterns

### Internal gRPC (frontend → storage)
Frontends connect to storage pods via headless service DNS (`storage-0.storage.buildbarn.svc.cluster.local`, etc.). Each storage pod is addressed directly — no load balancer. The sharding logic in the frontend determines which storage pod to contact for each blob.

### gRPC Clients as BlobAccess
`pkg/blobstore/grpcclients/cas_blob_access.go` — wraps a gRPC channel as a `BlobAccess`. This is how frontend pods talk to storage pods. The sharding frontend has a `ShardingBlobAccess` wrapping N `grpcClientsBlobAccess` instances, one per storage pod.

### Health Checks
Standard gRPC health protocol. ALB/NLB compatible.

---

## 11. bb-browser: Analytics UI

Buildbarn's `bb-browser` (separate repo) provides a web UI for browsing cache contents — inspecting action results, viewing CAS files, navigating directory trees. It connects to the storage gRPC endpoint as a regular client.

**What it shows:**
- Action result contents (output files, exit codes, stdout/stderr)
- CAS blob browser (navigate directory trees)
- No build metrics, no hit rate charts, no aggregate analytics

This is far less capable than BuildBuddy's UI. There are no aggregate cache hit rates, no per-project breakdown, no latency charts. It's a debug tool, not an analytics platform.

---

## 12. Gaps and Limitations

1. **No S3 or cloud storage backend.** All storage requires local block devices (PVCs). Cannot run diskless. This is the biggest architectural gap for K8s deployments where stateless pods are preferred.

2. **StatefulSet-only storage.** Because storage requires local PVCs, you cannot autoscale storage pods without a resharding operation. Frontend pods scale freely, but storage is effectively fixed at deployment time.

3. **Jsonnet dependency.** Configuration requires Jsonnet knowledge. Not as approachable as YAML for ops teams unfamiliar with it.

4. **No Helm chart.** The `bb-deployments` repo uses Kustomize + Jsonnet. No Helm chart exists. Production deployments require understanding the Jsonnet config structure.

5. **Completeness checking latency.** On every AC hit, CAS existence checks proportional to output file count. For builds with thousands of output files, this adds measurable latency.

6. **FindMissing is not streaming.** The TODO in `blob_access.go` notes this. Large shard deployments require careful batching to avoid message size limits.

7. **No multi-region or CDN patterns.** Mirroring is across two local storage backends. No support for geographically distributed replicas or edge caching.

8. **Resource limits not in reference config.** The `storage.yaml` and other K8s manifests don't set resource requests/limits. Production operators must add these manually.

9. **Block device format is not portable.** The custom block format cannot be read by any other tool. Disaster recovery requires the persistent state file and the block device files. No export to S3, no snapshot support.

10. **Only SHA256 digest function.** Not configurable in the storage layer (though the proto supports others).

---

## Key Lessons for a New Project

| Pattern | What to Borrow |
|---|---|
| `BlobAccess` interface | Clean minimal interface: Get, Put, FindMissing — no disk assumptions |
| `CompletenessCheckingBlobAccess` | The definitive solution to BWOB: check all CAS refs on every AC read |
| `ReadCachingBlobAccess` | L1 read cache in front of authoritative backend; async background replication |
| `MirroredBlobAccess` | HA via bidirectional replication with self-healing FindMissing |
| Rendezvous shard selector | The correct sharding algorithm: minimal disruption, weight-aware, no floating point |
| Jsonnet ConfigMap pattern | DRY config across services via shared libsonnet |
| Init container for directories | Simple pattern to ensure state directories exist before main container starts |
| Headless service for StatefulSet | Direct pod DNS access for storage — no unintended load balancing |
| Per-direction Prometheus metrics | Track sync counts A→B and B→A separately for operational visibility |
| `buffer.Buffer` abstraction | Lazy data handle that supports clone, discard, proto decode without multiple reads |
