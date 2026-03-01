# AI Architecture: Graph-Aware Learned Eviction + Operational Intelligence

## Research Thesis

"Graph-Aware Learned Eviction for Content-Addressable Build Caches"

Build caches differ from CDN/web caches in ways that make standard eviction suboptimal:

1. **Objects form a DAG.** Evicting a shared intermediate artifact cascades rebuilds of all
   downstream targets. A CAS blob used by 50 actions has 50x the eviction cost of one used by 1.
2. **Miss cost is wildly non-uniform.** A 500-byte header file's rebuild cost is negligible.
   A 45-minute LLVM compilation output has enormous miss cost.
3. **Access patterns are structured.** Release branches, sprint cadences, CI schedules create
   semi-predictable access patterns that a learned policy can exploit.
4. **S3 has no native LRU.** No cheap "touch" operation, no built-in eviction. Eviction must be
   explicitly implemented as delete operations.

No existing work applies RL-based eviction to build caches. Prior art (Cold-RL, HALP, LRB,
RL-Cache from Akamai) all target CDN/web caching with uniform-cost misses and no DAG structure.

---

## Two-Tier Brain Architecture

```
                    ┌─────────────────────────────────┐
                    │        Slow Brain (LLM)          │
                    │  Async reasoning, diagnostics,   │
                    │  policy tuning, anomaly narration │
                    │  Runs: minutes-scale              │
                    └──────────┬──────────────────────┘
                               │ adjusts reward weights,
                               │ suggests policy changes
                    ┌──────────▼──────────────────────┐
                    │        Fast Brain (RL)            │
                    │  Microsecond eviction decisions,  │
                    │  lightweight DQN model,           │
                    │  runs inline or sidecar           │
                    └──────────┬──────────────────────┘
                               │ eviction decisions
                    ┌──────────▼──────────────────────┐
                    │      S3 Eviction Worker (Go)     │
                    │  Rate-limited DeleteObjects,      │
                    │  respects AC→CAS references       │
                    └─────────────────────────────────┘
```

**Fast brain (RL):** Trained offline on access traces, deployed as ONNX model loaded in Go
(or Python sidecar). Makes per-object eviction decisions at cache-operation timescales.

**Slow brain (LLM agents):** Runs asynchronously. Monitors RL outcomes over hours/days. Queries
system state via MCP servers. Produces human-readable explanations and policy adjustments.

---

## Phase 6a: Access Logger Middleware

The bridge between the Go cache server and the Python RL training pipeline.

**Runs in:** Go, as middleware in the cache server's HTTP/gRPC handlers.

**Log record per operation:**
```json
{
  "timestamp": "2025-03-15T10:30:00.123Z",
  "operation": "GET",
  "store": "cas",
  "key": "a1b2c3d4e5f6...",
  "size_bytes": 1048576,
  "hit": true,
  "store_type": "valkey",
  "latency_ms": 2.3,
  "action_hash": "f7e8d9c0..."
}
```

**Key fields:**
- `store`: `ac` or `cas` — which logical store
- `hit`: whether the object was found
- `store_type`: `valkey`, `s3`, or `miss` — where it was served from
- `action_hash`: for AC operations, the action hash (enables AC→CAS graph reconstruction)
- `size_bytes`: object size (enables size-weighted eviction analysis)

**Output format:** Structured JSON lines (`.jsonl`). Optionally converted to Parquet via sidecar
for efficient offline analysis. Access logs are append-only and rotated by size/time.

---

## Phase 6b: Cache Trace Simulator

A Python tool that replays access logs through a configurable cache model. This is the RL
training environment — it runs thousands of times faster than real builds.

**Runs in:** Python, standalone CLI tool.

**What it models:**
- **Valkey tier:** Fixed capacity, configurable eviction policy (LRU, LFU, Size-weighted, RL agent)
- **S3 tier:** Lifecycle-limited (TTL-based or budget-based), configurable deletion policy
- **Two-tier interaction:** L1 miss → L2 fetch → L1 populate (mirrors real FastSlowStore behavior)

**Configurable policies:**
| Policy | Description |
|---|---|
| LRU | Least Recently Used (baseline) |
| LFU | Least Frequently Used |
| Size-weighted LRU | LRU weighted by object size (evict largest first) |
| TTL | Time-based expiry only |
| GDSF | Greedy Dual Size Frequency (strong classical baseline) |
| RL agent | Trained DQN model |

**Output metrics per policy:**
- `hit_rate` — overall and per-tier (Valkey hit, S3 hit, total miss)
- `total_rebuild_cost` — estimated CPU-seconds of rebuilds caused by misses
- `eviction_count` — how many objects were evicted
- `miss_cascade_depth` — how deep in the DAG did misses propagate
- Per-policy side-by-side comparison tables

**CLI interface:**
```bash
python -m tools.simulator \
  --trace access_log.jsonl \
  --valkey-capacity 4GB \
  --s3-ttl 30d \
  --policies lru,lfu,gdsf,rl \
  --rl-model models/eviction_v1.onnx \
  --output results/
```

---

## Phase 6c: RL Eviction Agent (DQN)

**Architecture:** Dueling DQN (following the Cold-RL approach from academic literature).

**Trained on:** Access log replay in the simulator. Thousands of episodes, each replaying a
time window of real or synthetic build activity.

### State Representation (Feature Vector Per Cache Entry)

**Access features:**
| Feature | Type | Description |
|---|---|---|
| `recency` | float | Time since last access (normalized) |
| `frequency` | int | Access count in current window |
| `inter_arrival_time` | float | Mean time between accesses |

**Object features:**
| Feature | Type | Description |
|---|---|---|
| `size_bytes` | float | Object size (log-scaled) |
| `estimated_rebuild_cost` | float | Estimated CPU-seconds to reproduce this object |

**Graph features (the novel contribution):**
| Feature | Type | Description |
|---|---|---|
| `num_downstream_dependents` | int | Actions that transitively depend on this CAS blob |
| `build_graph_depth` | int | Depth in the build DAG (0 = leaf, high = shared lib) |
| `shared_ratio` | float | Fraction of total actions that use this object |

**Lifecycle features:**
| Feature | Type | Description |
|---|---|---|
| `branch_type` | enum | `main`, `release`, `feature` (encoded as one-hot) |
| `days_since_branch_creation` | float | Age of the branch that produced this artifact |
| `is_ci_produced` | bool | Whether this was written by CI (vs developer) |

### Action Space

Binary: `keep` or `evict` for each candidate entry when cache pressure triggers eviction.

### Reward Signal

```
reward = -alpha * rebuild_cost_of_evicted_objects
         -beta  * cascade_rebuilds_caused
         -gamma * s3_api_cost_of_eviction
         +delta * cache_space_freed_ratio
```

Where:
- `alpha` weights direct rebuild cost (dominant term)
- `beta` weights downstream cascade effect (the graph-aware term)
- `gamma` penalizes excessive S3 API calls
- `delta` rewards freeing space (prevents the agent from never evicting)

The slow brain (LLM) monitors these weights over time and suggests adjustments based on
observed outcomes.

### Training Pipeline

```
access_logs.jsonl
      │
      ▼
Cache Trace Simulator (Python)
      │  ← RL agent makes eviction decisions
      │  → simulator computes reward
      ▼
DQN Training Loop (PyTorch)
      │  ← experience replay buffer
      │  → gradient updates
      ▼
Trained Model (ONNX export)
      │
      ▼
Go Cache Server (ONNX runtime) or Python Sidecar
```

### Deployment Options

1. **ONNX in Go:** Load the trained model via `onnxruntime-go`. Lowest latency, no sidecar.
   Best for production.
2. **Python sidecar:** Run the PyTorch model in a sidecar container. Easier development
   iteration, higher latency. Best for research/experimentation.
3. **Offline batch:** Run the RL agent periodically on recent access logs, produce an eviction
   plan (list of keys to delete), execute via S3 Eviction Worker. Simplest to implement,
   highest latency. Good starting point.

Start with option 3 (offline batch), graduate to option 1 (ONNX in Go) as the model matures.

---

## Phase 6d: Graph-Aware Features

Extracting build graph topology to enrich the RL feature vector.

**Source 1: AC→CAS Reference Graph (from access logs)**
- Every AC PUT includes an ActionResult proto that references CAS digests
- Parse these at log time to build: `cas_digest → [action_hashes that reference it]`
- This gives `num_downstream_dependents` and `shared_ratio` without reading BUILD files

**Source 2: BUILD File Parser (optional, richer features)**
- Parse `BUILD` / `BUILD.bazel` files to extract the declared dependency graph
- Map: `target → [dependency targets]` → compute transitive closure
- This gives `build_graph_depth` and more accurate `estimated_rebuild_cost`
- Implemented as a Python tool that reads a Bazel workspace and outputs a graph JSON

**Source 3: Bazel Query (most accurate, requires Bazel)**
- `bazel query 'deps(//...)'` outputs the full dependency graph
- `bazel query 'rdeps(//..., //some:target)'` gives reverse dependencies
- Most accurate but requires Bazel installed and a configured workspace
- Used for validation, not runtime

---

## Phase 7a: MCP Servers

Model Context Protocol servers that expose cache system state to LLM agents.

**MCP Server: S3 Stats**
- Bucket object count, total size, size distribution histogram
- Object listing with metadata (last-modified, size, key prefix)
- Recent eviction history

**MCP Server: Valkey Stats**
- Memory usage, eviction count, hit/miss rates
- Key count per prefix (ac vs cas)
- Slowlog entries

**MCP Server: Prometheus Metrics**
- Cache hit rate over time
- Request latency percentiles
- S3 API call counts and error rates
- Eviction decision distribution

**MCP Server: Eviction History**
- Recent eviction decisions with feature vectors
- Reward signal history
- Policy comparison results from simulator

---

## Phase 7b: LLM Diagnostic and Policy Agents

Agents that use the MCP servers to reason about cache behavior.

### Diagnostic Agent

**Trigger:** Hit rate drops below threshold, or operator asks "why is the cache slow?"

**Behavior:**
1. Query Prometheus MCP: get hit rate time series, identify when drop started
2. Query S3 MCP: check if bucket size hit a limit, check eviction rate
3. Query Valkey MCP: check memory pressure, eviction counts
4. Query Eviction History MCP: check if RL policy changed recently
5. Reason through causal chain → produce human-readable explanation

**Example output:**
> Hit rate dropped from 78% to 52% starting 2pm. This correlates with the release/3.2 branch
> being cut at 1:45pm, which introduced 2,400 new CAS objects (debug + release configs).
> Valkey is at 98% capacity and evicting at 340 objects/min. The RL agent is correctly
> prioritizing main-branch artifacts, but the release branch cache is churning. Recommendation:
> increase Valkey capacity from 4GB to 8GB, or add a release-branch-specific TTL policy.

### Policy Advisor Agent

**Trigger:** Periodic (daily) or on significant metric changes.

**Behavior:**
1. Query Eviction History MCP: get reward signal history for the last N days
2. Run simulator MCP: compare current RL policy against baselines on recent traces
3. If RL policy underperforms GDSF on rebuild-cost metric → flag for review
4. Suggest reward weight adjustments: "increase beta (cascade penalty) by 0.2"

### Build Graph Parser Agent

**Trigger:** On demand, when fresh graph features are needed for RL training.

**Behavior:**
1. Read BUILD files from the repository (or query Bazel)
2. Extract dependency graph topology
3. Compute per-target: depth, fan-out, shared ratio
4. Output enriched feature vectors for RL training data

### Anomaly Narrator Agent

**Trigger:** Statistical anomaly detected in metrics (z-score > 3 on any key metric).

**Behavior:**
1. Identify the anomalous metric and time window
2. Query relevant MCP servers for context
3. Produce a Slack-ready summary: what happened, likely cause, severity, recommended action

---

## Phase 7c: Fine-Tuning on Domain Diagnostics

Once sufficient diagnostic sessions accumulate:

1. Collect (system_state, diagnosis, recommendation) tuples from agent runs
2. Fine-tune a small model (e.g., Haiku-class) on these tuples
3. The fine-tuned model becomes faster and cheaper for routine diagnostics
4. The full reasoning model (Opus-class) handles novel/complex situations

This creates a flywheel: more usage → better training data → better fine-tuned model →
faster diagnostics → more usage.

---

## Key Boundaries

| Concern | Handled By | Language |
|---|---|---|
| Cache serving (HTTP/gRPC) | open-cache server | Go |
| Access logging | Middleware in cache server | Go |
| S3 eviction execution | Background worker in cache server | Go |
| RL training | Simulator + DQN training loop | Python |
| RL inference (production) | ONNX runtime in Go or Python sidecar | Go or Python |
| LLM agents | Agent framework + MCP clients | Python |
| MCP servers | Lightweight HTTP servers | Go or Python |
| Graph feature extraction | BUILD file parser / bazel query | Python |

The Go/Python boundary is the access log file. Go writes it, Python reads it. No tight coupling.
