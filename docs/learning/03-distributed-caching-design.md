# Distributed Caching Design

## The Problem

A monolith Bazel build with thousands of actions checks the cache for each one.
With S3 as the only backend (~50-100ms per round trip), cache checks can take
longer than just building locally. An hour-long build becomes slower with caching
than without.

## Why L1 RAM Matters

Cache lookups need to be microseconds, not milliseconds. An in-memory L1 tier
in front of S3 eliminates the network latency for hot entries.

## AC vs CAS: Different Strategies

AC and CAS entries have fundamentally different characteristics:

| Property | AC | CAS |
|---|---|---|
| Size | Hundreds of bytes | Bytes to GBs |
| Count per build | Bounded (~thousands) | Large (~tens of thousands) |
| Access pattern | Checked on every build | Accessed on cache hit |

This means they should be cached differently. AC entries are so small that
every pod can hold all of them. CAS entries are too large for full replication —
they should be sharded across pods.

## Distributed Cache Options

We evaluated several approaches for sharing cached data across pods:

**External shared store (Valkey/Redis):** Simple to reason about, but adds
infrastructure to deploy and manage. Every lookup is a network hop (~0.5ms).
Users have to size and monitor Valkey.

**Embedded cache per pod (no sharing):** Simplest. Each pod has its own hashmap.
No coordination. But cache miss on Pod A doesn't benefit from Pod B having the
entry. Each pod warms up independently via S3.

**Peer-to-peer with consistent hashing (groupcache):** Each pod owns a slice
of the keyspace. On a miss, pods ask the owning peer before falling back to S3.
Popular entries get replicated to requesting pods via a "hot cache." No external
infrastructure. Total cache = sum of all pod memory.

## Why groupcache

`golang/groupcache` (written by the memcached author at Google) handles:

- **Consistent hashing** — deterministic key-to-pod mapping
- **Peer coordination** — on local miss, fetch from owning peer
- **Singleflight** — concurrent requests for the same key trigger one fetch
- **Hot cache** — frequently accessed remote keys get cached locally

We use it as a **transport and coordination layer**, not for eviction policy.
Our eviction is custom (LRU now, DAG-aware RL agent later).

## Data Flow

### Write path

```
Bazel PUTs /ac/abc123 → NLB → Pod 2
  Pod 2 → groupcache (key hashes to Pod 4, the owner)
  Pod 4 stores in RAM
  Pod 2 writes to S3 (durable)
```

### Read path (first access from Pod 5)

```
Bazel GETs /ac/abc123 → NLB → Pod 5
  Pod 5 checks local hot cache → miss
  Pod 5 asks Pod 4 (the owner) → Pod 4 has it in RAM → returns it
  Pod 5 stores in local hot cache
```

### Read path (second access from Pod 5)

```
Bazel GETs /ac/abc123 → NLB → Pod 5
  Pod 5 checks local hot cache → hit → instant
```

Every read is served from RAM. The only variable is whose RAM — the owner's
or the local hot cache. S3 is only hit if the owning pod lost its cache
(restart).

## Why Not Valkey

For our use case, groupcache is better because:

- No external infrastructure for users to deploy
- Eviction is custom anyway (DAG-aware), so Valkey's LRU doesn't help
- AC/CAS entries are immutable — no consistency/conflict problems
- S3 is the durable fallback — losing RAM cache is not data loss

The `Store` interface makes the L1 backend swappable. If a team prefers
Valkey, they can implement the same interface against a Valkey client.

## Building the Cache Layer

An in-memory KV store requires:

- **Hashmap + mutex** — concurrent reads/writes
- **Size tracking** — know when cache is full
- **Eviction interface** — swappable policy (LRU now, RL later)
- **Peer discovery** — Kubernetes headless service DNS

None of these are complex individually. The reason distributed caches are
hard in general (consensus, conflict resolution, split-brain) doesn't apply
here because our entries are immutable and content-addressed.

## Approximate LRU (Redis Approach)

A naive LRU uses a linked list reordered on every access. Under thousands
of concurrent reads, the lock protecting the list becomes a bottleneck —
every reader contends on it just to update ordering.

Redis solved this with **approximate LRU**:

- Each key stores a last-access timestamp (atomic int64, no lock needed)
- On eviction, sample N random keys (default 10), evict the oldest
- No linked list, no reordering, no lock on the read path

With 10 samples, eviction quality is nearly identical to true LRU.
Redis tested this extensively and documented the results.

**Our read path under this approach:**

```
Get(key) →
  1. RLock the hashmap (many concurrent readers allowed)
  2. Look up the key
  3. Atomic timestamp write (no lock, no contention)
  4. Return the value
```

Compare to the naive approach where step 3 would be "acquire a
separate mutex, move a linked list node, release mutex." Under
1000s of concurrent reads, that's the difference between scaling
and not.

The eviction accuracy tradeoff: a key accessed 1ms before eviction
might not be saved because the sampler didn't pick it. For a cache
with S3 as the durable fallback, this is acceptable — a false eviction
just means one extra S3 fetch to repopulate.
