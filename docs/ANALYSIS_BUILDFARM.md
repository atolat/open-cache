# buildfarm: Deep Technical Analysis

> Based on direct source code analysis of https://github.com/buildfarm/buildfarm (HEAD, Feb 2026).
> Focused on Helm chart patterns, Redis architecture, storage design, and K8s deployment.

---

## 1. Helm Chart Structure

`kubernetes/helm-charts/buildfarm/` — the most mature official Helm chart among all OSS REAPI servers.

### Chart Metadata
```yaml
# Chart.yaml
name: buildfarm
version: 0.5.0
appVersion: 2.15.0
dependencies:
  - condition: redis.enabled
    name: redis
    repository: oci://registry-1.docker.io/bitnamicharts
    version: ~18.14.2
```

**Redis is a chart dependency** (Bitnami). Toggle between embedded Redis (`redis.enabled: true`) and external Redis (`externalRedis.uri`) via values. This is the right pattern — no hard assumption about who manages Redis.

### Template Structure
```
templates/
  _helpers.tpl          — Name/label helpers
  configmap.yaml        — Shared config.yml + log properties per component
  serviceaccount.yaml   — RBAC service account
  server/               — Server Deployment + Service + HPA + PDB
  shard-worker/         — ShardWorker StatefulSet + Service + HPA + PDB + PVC
  exec-worker/          — ExecWorker StatefulSet (disabled by default)
  tests/                — Helm test pod
```

### Key Values Exposed

**Global config** (applies to all components):
```yaml
config:
  digestFunction: SHA256
  defaultActionTimeout: 600       # seconds
  maximumActionTimeout: 3600
  maxEntrySizeBytes: "2147483648" # 2GB
  prometheusPort: 9090
  backplane:
    queues:
      - name: "cpu"
        allowUnmatched: true
        properties: [min-cores, max-cores]
```

**Server** (`server.*`):
- `replicaCount: 1` (server is stateless, can scale)
- `javaToolOptions` — JVM tuning baked in: `UseContainerSupport`, `MaxRAMPercentage=80.0`, `UseStringDeduplication`, `UseCompressedOops`, OOM heapdump
- gRPC liveness/readiness probes on port 8980
- ServiceMonitor support (Prometheus Operator)
- PodDisruptionBudget (disabled by default)
- `extraVolumes`, `extraVolumeMounts`, `extraEnv` escape hatches

**ShardWorker** (`shardWorker.*`):
- `replicaCount: 2` default
- **HPA enabled by default**: `minReplicas: 2`, `maxReplicas: 4`, target CPU 50%
- `podManagementPolicy: Parallel` — all pods start simultaneously (not ordered)
- `storage.enabled: true`, `storage.size: 50Gi` — PVC per worker
- `persistentVolumeClaimRetentionPolicy: Retain` on scale-down and delete
- `capabilities.execution: true` — workers handle both CAS and execution by default

**ExecWorker** (`execWorker.*`):
- Disabled by default (`enabled: false`)
- Same HPA and PVC config as shardWorker
- `capabilities.cas: false` — pure execution worker, no CAS

**Redis** (`redis.*`):
- `auth.enabled: false` (no password by default — not production-safe)
- `replica.replicaCount: 1`

### ConfigMap Rendering Pattern

The config ConfigMap renders YAML from Helm values:
```yaml
config.yml: |-
  {{- range $key, $value := .Values.config }}
  {{- if kindIs "map" $value }}
  {{- else }}
  {{ $key }}: {{ $value }}
  {{- end }}
  {{- end }}
  backplane:
    redisUri: 'redis://{{ .Release.Name }}-redis-master.{{ .Release.Namespace }}:6379'
    {{- with .Values.config.backplane }}
    {{- toYaml . | nindent 6 }}
    {{- end }}
  server:
    {{- toYaml .Values.server.config | nindent 6 }}
  worker:
    {{- toYaml .Values.shardWorker.config | nindent 6 }}
```

**Key pattern**: Redis URI is auto-constructed from the Helm release name and namespace — no manual config needed when using embedded Redis. External Redis requires setting `redis.enabled: false` and `externalRedis.uri`.

**Limitation**: The ConfigMap template naively loops over `Values.config` and skips map values — this means nested configuration objects must go in the `backplane`, `server`, or `worker` stanzas separately. Not a clean separation.

---

## 2. Redis Architecture: Everything Through Redis

Redis is not just a cache tier in buildfarm — it is the **entire backplane**. `RedisShardBackplane.java` (1,413 lines) is the core of the system.

### What Redis Stores

Redis is used for multiple distinct purposes simultaneously:

| Data | Redis Structure | Description |
|---|---|---|
| Worker registry | Set / Hash | Active workers, capabilities, heartbeat timestamps |
| Action cache (AC) | Hash / String | Action results (small AC entries cached in Redis) |
| CAS index | Set per worker | Which worker(s) hold which CAS digests |
| Operation queue | List (BRPOPLPUSH) | Pending build actions waiting for workers |
| Dispatched operations | Hash | In-flight operations being executed |
| Operation state | Pub/Sub channel | Real-time status updates to watching clients |
| Worker change events | Pub/Sub | Worker join/leave notifications |
| Client start times | Hash | For tracking build session metadata |

### Key Redis Operations

**Worker registration** — workers heartbeat into Redis Sets with TTL. The server polls the worker set (max age: 3 seconds). Worker departure is detected by TTL expiry.

**CAS index** — for each CAS blob, Redis tracks which worker(s) hold it locally. When a client needs a blob for execution, the server can route the request to a worker that already has it, avoiding re-transfer. This is the "CAS locality" optimization.

**Operation lifecycle** — operations move through Redis states:
1. Enqueued in an execution queue (Redis List, `BRPOPLPUSH` for atomic dequeue)
2. Worker dequeues and publishes state changes via pub/sub
3. Clients subscribe to operation channels for streaming status
4. On completion, result written to AC

**Pipeline usage** — `AbstractPipeline` (Jedis pipeline) is used for batching multiple Redis commands in a single round-trip. Important for performance given the number of operations per build action.

### Redis Pub/Sub for Streaming Operations
`RedisShardSubscriber` subscribes to operation channels. When a build client calls `WaitExecution`, it subscribes to the Redis pub/sub channel for that operation and receives real-time state updates as workers progress through QUEUED → EXECUTING → COMPLETED.

### Critical: Redis is a Single Point of Failure
If Redis goes down, the entire system stops — no new builds can be scheduled, no operation state updates, no worker registration. Redis HA (sentinel or cluster) is essential for production. The chart's default of `replica.replicaCount: 1` is not production-safe.

---

## 3. Storage Backend Architecture

### Server-Side Storage
The buildfarm server (`ShardInstance`) maintains the CAS and AC through a combination of:
- Local per-worker CAS storage (each worker has its own local CAS on disk)
- Redis for routing (which worker holds which blob)
- The server is a routing layer, not a storage layer

Workers store CAS blobs locally in a configurable directory. There is no shared S3 or cloud storage — each worker is its own independent CAS node.

### Cache-Only Deployment (No Execution)
Technically possible: set `shardWorker.config.capabilities.execution: false` and keep `capabilities.cas: true`. Workers act as pure CAS nodes. The server routes CAS operations to workers. This is not well-documented but the capability flag exists.

### No Cloud Storage Backend
Like Buildbarn, buildfarm has no S3 or GCS backend. All durable CAS storage is worker-local disk (50Gi PVC per worker by default). This means:
- Workers are stateful (StatefulSet, PVCs that survive scale-down via Retain policy)
- Losing a worker means losing its local CAS blobs (not replicated by default)
- No geographic distribution

---

## 4. Configuration Pattern

buildfarm uses a YAML config file (`config.yml`) loaded by the server and workers at startup. Key sections:

```yaml
digestFunction: SHA256
defaultActionTimeout: 600
maximumActionTimeout: 3600
maxEntrySizeBytes: 2147483648

backplane:
  redisUri: redis://...
  queues:
    - name: cpu
      allowUnmatched: true
      properties:
        - name: min-cores
          value: "*"

server:
  name: shard
  recordBesEvents: true

worker:
  port: 8982
  capabilities:
    execution: true
    cas: true
```

Configuration is shared between server and worker via the same ConfigMap but different stanzas. This works but creates a monolithic config that's easy to misconfigure (e.g., worker-specific settings accidentally applied to the server).

---

## 5. Operational Pain Points

Based on code inspection and known GitHub issues:

1. **Redis is a hard dependency and single point of failure.** No alternative backplane implementation exists. No Redis → no builds.

2. **Java GC pauses.** Despite JVM tuning in `javaToolOptions`, the Java runtime introduces GC pauses that can cause gRPC timeout spikes under load. No GC-free hot path exists (unlike Rust/Go alternatives).

3. **Worker local disk = data loss risk.** CAS blobs on worker disk are lost if the PVC is deleted. `persistentVolumeClaimRetentionPolicy: Retain` mitigates accidental deletion on scale-down but not on pod failure with PVC loss.

4. **HPA and StatefulSets don't mix well.** Workers are StatefulSets with PVCs. The HPA scales the StatefulSet, which creates new PVCs on scale-up. Scale-down leaves PVCs behind (Retain policy). This can exhaust storage quota over time.

5. **No AC TTL.** Action cache entries in Redis can grow unbounded. Redis maxmemory eviction policy is the only backstop, and if it's not configured for LRU, Redis can fill up and start failing writes.

6. **Auth disabled by default.** `redis.auth.enabled: false` in the Helm chart default values is a security risk in any shared cluster environment.

7. **ConfigMap template limitation.** Nested config objects in `Values.config` are silently skipped. Users expecting map values to appear in `config.yml` won't see them and may not notice until runtime.

---

## 6. Novel Patterns Worth Borrowing

### 1. Bitnami Redis as Chart Dependency
The pattern of using a well-maintained chart dependency (Bitnami Redis) rather than writing Redis deployment templates from scratch is the right call. The Bitnami Redis chart handles sentinel mode, auth, TLS, persistence, and metrics properly.

For a new cache project: use the same pattern for Valkey (Bitnami has a Valkey chart as of 2024).

### 2. Separate ExecWorker Toggle
The `execWorker.enabled: false` flag allowing dedicated CAS-only vs execution-only workers is a clean architectural split. For a pure cache project, this confirms the pattern: a cache-only deployment should not need to run any execution machinery.

### 3. `podManagementPolicy: Parallel`
Setting `Parallel` on the worker StatefulSet means all pods start simultaneously rather than waiting for each one to be ready before starting the next. For stateless-ish workers where ordering doesn't matter, this dramatically reduces cold-start time.

### 4. HPA on Workers
Having HPA enabled by default (targeting CPU utilization) is the right K8s-native approach. For a cache service, memory utilization would be a better scaling metric, but CPU-based autoscaling is a reasonable starting point.

### 5. ServiceMonitor + prometheusPort in Values
First-class Prometheus Operator support (`serviceMonitor.enabled: false` toggle) is the right pattern. New project should have this from day one, not as an afterthought.

### 6. Per-Component Log Properties ConfigMap
Separate logging ConfigMaps per component (server, shard-worker, exec-worker) allows tuning log verbosity per component without redeploying everything. Useful for debugging production issues.

---

## Summary: Key Takeaways

| Finding | Implication for New Project |
|---|---|
| Redis as central backplane (1413-line class) | Don't couple metadata to blob storage — keep them separate |
| No S3 or cloud storage | Major gap — S3-primary is genuinely novel in this space |
| Java GC pauses | Go or Rust are better choices for a latency-sensitive cache |
| Bitnami Redis chart dependency | Use same pattern for Valkey dependency |
| `persistentVolumeClaimRetentionPolicy: Retain` | Include this in worker StatefulSet if disk is used |
| `podManagementPolicy: Parallel` | Use in StatefulSets where ordering doesn't matter |
| ServiceMonitor toggle | Include from day 1, disabled by default |
| HPA on workers | Good default for cache pods; use memory metric instead of CPU |
| Auth disabled by default | Explicitly document the security implication; consider enabling by default |
| `extraVolumes` / `extraVolumeMounts` / `extraEnv` escape hatches | Include in every component's values for extensibility |
