# NativeLink: Deep Technical Analysis

> Based on direct source code analysis of https://github.com/TraceMachina/nativelink (HEAD, Feb 2026).
> Focused on patterns relevant to building an S3-primary, K8s-native, tiered Bazel remote cache.

---

## 1. Store Abstraction: The `StoreDriver` Trait

The entire storage system is built around a single `StoreDriver` trait in `nativelink-util/src/store_trait.rs`. Every store — memory, disk, S3, Redis, gRPC proxy — implements this trait. This is the cleanest, most composable storage abstraction of any OSS cache reviewed.

**Core methods:**
```rust
trait StoreDriver {
    async fn has_with_results(key: &[StoreKey], results: &mut [Option<u64>]) -> Result<()>;
    async fn update(key: StoreKey, reader: DropCloserReadHalf, size_info: UploadSizeInfo) -> Result<()>;
    async fn get_part(key: StoreKey, writer: &mut DropCloserWriteHalf, offset: u64, length: Option<u64>) -> Result<()>;
    fn optimized_for(&self, optimization: StoreOptimizations) -> bool;
    fn inner_store(&self, key: Option<StoreKey>) -> &dyn StoreDriver;
}
```

**Key design decisions:**
- `has_with_results` takes a batch of keys and fills results in-place — batch-first API at the trait level.
- `get_part` takes `offset` and optional `length` — **range reads are first-class**, not an afterthought.
- `optimized_for(StoreOptimizations)` is a capability introspection mechanism. Values include: `NoopUpdates`, `NoopDownloads`, `FileUpdates`, `LazyExistenceOnSync`. Stores use this to skip unnecessary work (e.g., FastSlowStore checks if slow store is NoopUpdates before trying to write to it).
- `inner_store(key)` allows transparent unwrapping through wrapper stores — used by FastSlowStore to inspect whether the underlying slow store can skip certain operations.

**Full list of store implementations** (`nativelink-store/src/lib.rs`):

| Store | Purpose |
|---|---|
| `memory_store` | In-process LRU with configurable max size |
| `filesystem_store` | Local disk with async eviction |
| `s3_store` | AWS S3 — full first-class store |
| `gcs_store` | Google Cloud Storage |
| `grpc_store` | Remote REAPI server as a store |
| `redis_store` | Redis/Valkey — full first-class store |
| `mongo_store` | MongoDB backend |
| `fast_slow_store` | Tiering: fast L1 + slow L2, simultaneous writes |
| `shard_store` | Consistent hash sharding across multiple stores |
| `size_partitioning_store` | Routes to different stores by blob size |
| `compression_store` | Transparent zstd compression wrapper |
| `dedup_store` | Content dedup by digest |
| `existence_cache_store` | In-memory bloom/LRU cache for has() results |
| `completeness_checking_store` | AC wrapper that validates all CAS refs before returning |
| `verify_store` | Validates digest integrity on read/write |
| `ref_store` | Named reference to another store (avoids config duplication) |
| `noop_store` | /dev/null — always reports empty |
| `ontap_s3_store` | NetApp ONTAP S3 variant |
| `ontap_s3_existence_cache_store` | ONTAP with existence caching |

---

## 2. S3 Store: First-Class, Production-Grade

`nativelink-store/src/s3_store.rs` — S3 is a **genuine primary store**, not a proxy or sidecar.

### Connection and Auth
- Uses `aws-sdk-s3` (Rust AWS SDK, BehaviorVersion::v2025_08_07).
- Default credential chain (IAM roles, env vars, config file, IMDS).
- Custom `TlsClient` from `common_s3_utils.rs` (configurable TLS).
- 15-second connect timeout (hardcoded, configurable in the spec struct).

### Object Keys
```rust
fn make_s3_path(&self, key: &StoreKey<'_>) -> String {
    format!("{}{}", self.key_prefix, key.as_str())
}
```
Key is just `{prefix}{store_key}`. Store key for CAS is the digest hash. Simple and clean.

### Upload Strategy
- Blobs < 5MB and exact size known → single `PutObject` (1 network round-trip).
- Blobs ≥ 5MB or unknown size → multipart upload:
  - `CreateMultipartUpload` → upload N parts concurrently → `CompleteMultipartUpload`.
  - Default: 10 concurrent part uploads (`DEFAULT_MULTIPART_MAX_CONCURRENT_UPLOADS`).
  - 5MB retry buffer per request (`DEFAULT_MAX_RETRY_BUFFER_PER_REQUEST`).
  - On failure: `AbortMultipartUpload` cleanup (best-effort, not retried).
  - Parts are sorted by number before completion (in case futures complete out of order).
  - Uses `mpsc::channel` bounded by `multipart_max_concurrent_uploads` as backpressure.

### Download / Range Reads
```rust
.range(format!("bytes={}-{}", offset + writer.get_bytes_written(), ...))
```
Full HTTP range request support. On retry, resumes from `get_bytes_written()` — **partial retry without re-reading from offset 0**.

### TTL / Expiry
`consider_expired_after_s`: if set, `has()` checks `last_modified` of the S3 object. If older than the threshold, treats the object as missing and fires remove callbacks. This is **emulated TTL via HeadObject** — not a native S3 lifecycle rule. Useful for AC (short-lived) vs CAS (long-lived) semantics.

### Retry Logic
Uses a `Retrier` abstraction wrapping an unfold stream. All S3 operations use `Code::Aborted` for retryable errors (not `Code::InvalidArgument`). Comment in code explicitly warns developers not to use non-retryable codes. Stream rewind via `reader.try_reset_stream()` for single-part upload retries.

### What S3Store does NOT do
- No deletion (no eviction callback deletes S3 objects by default — callbacks are registered by wrappers like `ontap_s3_existence_cache_store`).
- No explicit connection pool size config — relies on AWS SDK's default HTTP connection pool.
- No object tagging.

---

## 3. FastSlowStore: Tiering Done Right

`nativelink-store/src/fast_slow_store.rs` — the core tiering primitive.

### Write Path (update)
On write, data is **tee'd simultaneously** to both fast and slow stores using a channel pair:
```rust
let (mut fast_tx, fast_rx) = make_buf_channel_pair();
let (mut slow_tx, slow_rx) = make_buf_channel_pair();
// stream: fast_tx.send(buf.clone()), slow_tx.send(buf)  ← concurrent
join!(data_stream_fut, fast_store_fut, slow_store_fut)
```
- Both stores receive the same data simultaneously — no sequential write.
- Backpressure warning: if `send()` stalls for >5 seconds, logs a warn with chunk details.
- If slow store is NoopUpdates / ReadOnly / Get direction → skip it. Same for fast store.
- `StoreDirection` enum: `ReadOnly`, `Get` (get-only), `Update` (update-only), or bidirectional.

### Read Path (get_part)
1. Check fast store first via `has()`.
2. On fast hit: serve directly from fast store.
3. On fast miss: use `OnceCell`-based `LoaderGuard` to deduplicate concurrent fetches.
   - Only the **first** caller populates; subsequent callers for the same key wait on the OnceCell.
   - After population, callers either served inline (first caller) or re-enter get_part (subsequent).
4. Population = read from slow store, tee to fast store and to the waiting writer simultaneously.

### Thundering Herd Prevention
`populating_digests: Mutex<HashMap<StoreKey, Arc<OnceCell<()>>>>` — concurrent requests for the same missing key are de-duplicated. This is cancel-safe via the `LoaderGuard` drop impl which cleans up the OnceCell.

### File-Level Optimization
`optimized_for(StoreOptimizations::FileUpdates)` → `update_with_whole_file()` path. If the fast store supports file updates (filesystem_store does), it can move the file rather than copying it — zero-copy for large blobs.

---

## 4. Redis Store: Full Cache Tier, Not Just Scheduler

`nativelink-store/src/redis_store.rs` — Redis is a **full StoreDriver** for blob storage, not just used for scheduling.

### Connection Modes
Supports three connection modes via `RedisMode` config:
- **Single**: `ConnectionManager` with automatic reconnection.
- **Cluster**: `ClusterClient` with `ClusterConnection`.
- **Sentinel**: `SentinelClient` with `SentinelNodeConnectionInfo`.

Default connection pool size: 3 (`DEFAULT_CONNECTION_POOL_SIZE`).
Default connection timeout: 3000ms.
Default command timeout: 10,000ms.
Client semaphore for limiting concurrent Redis operations: 500 (`DEFAULT_CLIENT_PERMITS`).

### Blob Storage in Redis
Blobs are chunked into Redis strings:
- Default chunk size: 64KB (`DEFAULT_READ_CHUNK_SIZE`).
- Default max chunks per update: 10 (`DEFAULT_MAX_CHUNK_UPLOADS_PER_UPDATE`).
- Keys for chunks: `{base_key}:{chunk_index}`.
- Stored as Redis strings via `SET`.

### Pub/Sub Eviction Callbacks
`pub_sub_channel: Option<String>` — when set, publishes a message to the channel when a key is added, removed, or modified. Used by `register_remove_callback()` subscribers to react to eviction. This is the pattern for coordinating L1/L2 cache invalidation.

### Scheduler Store
Redis also implements `SchedulerStore` trait (separate from `StoreDriver`) for the scheduler use case:
- `SchedulerIndexProvider` — RediSearch for indexed queries over scheduler state.
- `SchedulerSubscriptionManager` — pub/sub for scheduler event notifications.
- Uses `ft_create` and `ft_aggregate` helpers from `redis_utils/`.

---

## 5. BWOB / AC-CAS Consistency

`nativelink-store/src/completeness_checking_store.rs`

NativeLink implements AC-CAS consistency checking at the store level, similar to Buildbarn. On every AC `get()`:

1. Deserialize the `ActionResult` proto from the AC store.
2. Collect all referenced digests: output files, stdout/stderr digests, tree digests.
3. For output directories, recursively fetch and parse `Tree` protos to get nested file digests.
4. Run `has_with_results` in parallel (via `FuturesUnordered`) on all collected digests.
5. If any are missing → return `Code::NotFound` for the AC entry (treat as cache miss).

This is the correct solution: the AC entry is still present in storage, but the completeness check prevents returning a stale pointer. It also avoids the need for atomic AC+CAS GC — the stores can evict independently.

**Cost:** An AC hit requires additional CAS existence checks (proportional to output file count). For large actions with many outputs this adds latency.

---

## 6. Shard Store

`nativelink-store/src/shard_store.rs` — consistent hash sharding across multiple stores.

Uses a `weight`-based selection (like Rendezvous hashing). Each shard has a configurable weight. `has_with_results` fans out to all shards concurrently (via FuturesUnordered). `get_part` and `update` target the selected shard only.

---

## 7. Size Partitioning Store

`nativelink-store/src/size_partitioning_store.rs` — route to different stores based on blob size.

**Extremely useful pattern for a new cache**: small blobs (AC metadata, small outputs) → Redis/memory; large blobs (binaries, archives) → S3 or disk. This avoids polluting Redis with large objects and avoids S3 round-trips for tiny metadata.

---

## 8. Kubernetes Deployment Patterns

`/tmp/nativelink/kubernetes/` — Kubernetes manifests.

NativeLink provides Kubernetes deployment examples but no Helm chart. Key patterns:
- Separate Deployments for cache service and scheduler.
- ConfigMap-mounted JSON configuration (NativeLink uses JSON config, not YAML).
- No resource limits defined in the base manifests (operator sets these).
- Health checks via gRPC health protocol.
- No StatefulSet — stateless cache nodes pointing to external S3.

**This is the important pattern**: because S3 is a first-class store, NativeLink cache pods can be stateless `Deployment` objects (not StatefulSets), enabling easy horizontal scaling.

---

## 9. Key Performance Patterns

### Async Channel Tee (FastSlowStore)
Writing to both fast and slow stores simultaneously via `make_buf_channel_pair()` means upload latency is `max(fast_write, slow_write)`, not `fast_write + slow_write`. For an L1 memory/Redis tier + L2 S3 backend, this means the client waits for S3 write completion — one area where a new project might improve by making slow writes async.

### Existence Cache
`existence_cache_store.rs` — wraps any store with an in-memory LRU set that caches positive `has()` results. This eliminates redundant `HeadObject` calls to S3 for blobs that are known to exist. Critical for performance since `FindMissingBlobs` generates a `has()` per digest.

### Backpressure Warnings
Channel sends that stall >5 seconds log detailed warnings. This is good operational hygiene — silent stalls are one of the hardest cache bugs to diagnose.

### Zero-Copy File Moves
When `fast_slow_store` detects `StoreOptimizations::FileUpdates` on the fast store (filesystem_store), it moves the file from a worker temp directory rather than copying. For large compiler outputs this is a significant win.

---

## 10. Gaps and Limitations

1. **No Helm chart.** Kubernetes manifests in `kubernetes/` are illustrative but not production-ready. No values abstraction, no templating, no resource profiles.

2. **Redis chunking is naive.** Blobs stored as N fixed-size Redis string keys. No compression at the Redis tier. No TTL management per chunk (TTL must be set on all N chunks). Chunk count limits effective max blob size to `DEFAULT_MAX_CHUNK_UPLOADS_PER_UPDATE * DEFAULT_READ_CHUNK_SIZE = 640KB` by default.

3. **FastSlowStore writes are synchronous end-to-end.** Client waits for both fast and slow stores to complete the write. For an S3 slow store, this means client latency includes S3 write time. No async-to-slow mode.

4. **S3 connection pool not directly configurable.** Relies on AWS SDK HTTP client defaults.

5. **No native HTTP cache endpoint.** NativeLink only speaks gRPC/REAPI. HTTP REST cache protocol (used by older Bazel versions and some tools) is not supported.

6. **`consider_expired_after_s` requires HeadObject per has().** Emulated TTL via S3 `last_modified` field means every existence check against S3 issues a `HeadObject`. At scale with many blobs and frequent `FindMissingBlobs` calls, this generates substantial S3 API costs (mitigated by `existence_cache_store`).

7. **License.** NativeLink uses Functional Source License (FSL), which is not OSI-approved open source. It converts to Apache 2.0 after 2 years. Worth noting for any project that wants to borrow patterns.

---

## Key Lessons for a New Project

| Pattern | What to Borrow |
|---|---|
| `StoreDriver` trait | Universal storage abstraction — range reads, batch has, capability introspection |
| `FastSlowStore` | Simultaneous fast+slow writes via channel tee; OnceCell thundering herd prevention |
| `S3Store` | Multipart upload with concurrent parts; range reads on retry; TTL via `consider_expired_after_s`; retrier abstraction |
| `RedisStore` | Pub/sub eviction callbacks; cluster + sentinel support; chunk-based storage |
| `CompletenessCheckingStore` | AC-CAS consistency via parallel FindMissing on every AC hit |
| `SizePartitioningStore` | Route small metadata to Redis/memory, large blobs to S3 |
| `ExistenceCacheStore` | In-memory LRU to skip redundant S3 HeadObject calls |
| Stateless K8s pods | S3 as primary = no StatefulSet, easy horizontal scale |
