# RL Eviction + Operational Intelligence

## Problem

Build caches differ from CDN/web caches:

1. **Objects form a DAG.** Evicting a shared intermediate artifact cascades rebuilds
   of all downstream targets. A CAS blob used by 50 actions costs 50x more to evict
   than one used by 1.
2. **Miss cost is non-uniform.** A 500-byte header rebuild is cheap. A 45-minute LLVM
   compilation output is expensive.
3. **Access patterns are structured.** Release branches, sprint cadences, CI schedules
   create semi-predictable patterns a learned policy can exploit.
4. **S3 has no native LRU.** Eviction must be explicitly implemented as delete operations.

Standard eviction policies (LRU, LFU, GDSF) ignore the DAG structure entirely.

## Architecture

```
┌─────────────────────────────────┐
│        Slow Brain (LLM)         │
│  Async diagnostics, policy      │
│  tuning, anomaly narration      │
│  Timescale: minutes/hours       │
└──────────┬──────────────────────┘
           │ adjusts reward weights,
           │ suggests policy changes
┌──────────▼──────────────────────┐
│        Fast Brain (RL)          │
│  Per-object eviction decisions  │
│  DQN model, runs inline        │
│  Timescale: microseconds        │
└──────────┬──────────────────────┘
           │ eviction decisions
┌──────────▼──────────────────────┐
│    S3 Eviction Worker (Go)      │
│  Rate-limited DeleteObjects     │
└─────────────────────────────────┘
```

## Phase 6a: Access Logger

Go middleware in HTTP/gRPC handlers. Writes structured JSONL per cache operation.

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

`action_hash` on AC operations enables AC→CAS graph reconstruction from logs alone.

Output: append-only `.jsonl`, rotated by size/time. This is the Go/Python boundary —
Go writes, Python reads.

## Phase 6b: Cache Trace Simulator

Python CLI that replays access logs through a configurable cache model.

Models Valkey (fixed capacity, configurable policy) and S3 (TTL/budget-based deletion)
with two-tier interaction (L1 miss → L2 fetch → L1 populate).

Configurable policies: LRU, LFU, Size-weighted LRU, TTL, GDSF, RL agent.

```bash
python -m tools.simulator \
  --trace access_log.jsonl \
  --valkey-capacity 4GB \
  --policies lru,lfu,gdsf,rl \
  --rl-model models/eviction_v1.onnx \
  --output results/
```

Output: per-policy hit rate, rebuild cost, eviction count, cascade depth.

## Phase 6c: RL Agent

Dueling DQN. Trained offline on access log replay in the simulator.

### State (per cache entry)

| Feature | Description |
|---|---|
| `recency` | Time since last access (normalized) |
| `frequency` | Access count in current window |
| `inter_arrival_time` | Mean time between accesses |
| `size_bytes` | Object size (log-scaled) |
| `estimated_rebuild_cost` | CPU-seconds to reproduce |
| `num_downstream_dependents` | Actions that transitively depend on this blob |
| `build_graph_depth` | Depth in the build DAG |
| `shared_ratio` | Fraction of total actions using this object |
| `branch_type` | main / release / feature |
| `is_ci_produced` | Written by CI vs developer |

The graph features (`num_downstream_dependents`, `build_graph_depth`, `shared_ratio`)
are the novel part — no prior work on RL eviction uses build graph structure.

### Action Space

Binary: `keep` or `evict` per candidate when cache pressure triggers.

### Reward

```
reward = -α * rebuild_cost_of_evicted
         -β * cascade_rebuilds_caused
         -γ * s3_api_cost
         +δ * cache_space_freed_ratio
```

The slow brain monitors these weights and suggests adjustments based on observed outcomes.

### Training

```
access_logs.jsonl → Simulator → DQN training (PyTorch) → ONNX export → Go runtime
```

### Deployment

Start with offline batch (periodic eviction plans from recent logs). Graduate to
ONNX-in-Go for inline decisions as the model matures.

## Phase 6d: Graph Features

Three sources of build graph topology:

1. **AC→CAS references from access logs** — parse ActionResult protos logged during
   AC PUTs. Gives `num_downstream_dependents` and `shared_ratio` without any external
   tooling.
2. **BUILD file parser** — Python tool that parses `BUILD` / `BUILD.bazel` files.
   Gives `build_graph_depth` and rebuild cost estimates.
3. **`bazel query`** — most accurate but requires Bazel + configured workspace.
   Used for validation, not runtime.

## Phase 7a: MCP Servers

Expose cache system state to LLM agents via Model Context Protocol:

- **S3 stats** — object count, size distribution, eviction history
- **Valkey stats** — memory usage, hit/miss rates, key counts
- **Prometheus metrics** — hit rate over time, latency percentiles, error rates
- **Eviction history** — recent decisions with feature vectors, reward history

## Phase 7b: LLM Agents

**Diagnostic agent** — triggered on hit rate drops or operator queries. Correlates
metrics across tiers (Prometheus → S3 → Valkey → eviction history) to produce
explanations and recommendations.

**Policy advisor** — periodic. Compares RL policy against baselines on recent traces.
Flags underperformance. Suggests reward weight adjustments.

**Anomaly narrator** — triggered on metric anomalies (z-score > 3). Produces
Slack-ready summaries with cause, severity, recommended action.

## Phase 7c: Fine-Tuning

Accumulate `(system_state, diagnosis, recommendation)` tuples from agent runs.
Fine-tune a small model for routine diagnostics. Full model handles novel situations.

## Boundaries

| Concern | Language |
|---|---|
| Cache serving (HTTP/gRPC) | Go |
| Access logging | Go |
| S3 eviction execution | Go |
| RL training + inference | Python |
| LLM agents + MCP clients | Python |
| Graph feature extraction | Python |

Go/Python boundary: the access log file.
