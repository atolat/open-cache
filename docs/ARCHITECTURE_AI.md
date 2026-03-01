# RL Eviction + Operational Intelligence

## Problem

Build caches differ from CDN/web caches:

1. **Objects form a DAG.** Evicting a shared intermediate artifact cascades rebuilds
   of all downstream targets.
2. **Miss cost is non-uniform.** A header rebuild is cheap. A 45-minute compilation
   output is expensive.
3. **Access patterns are structured.** Release branches, sprint cadences, CI schedules
   create semi-predictable patterns a learned policy can exploit.
4. **S3 has no native LRU.** Eviction must be explicitly implemented as deletes.

Standard eviction policies (LRU, LFU, GDSF) ignore the DAG structure.

## Architecture

```
┌─────────────────────────────────┐
│        Slow Brain (LLM)         │
│  Async diagnostics, policy      │
│  tuning, anomaly narration      │
│  Timescale: minutes/hours       │
└──────────┬──────────────────────┘
           │ adjusts reward weights
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

## Phases

### 6a: Access Logger

Go middleware. Structured JSONL per cache operation — timestamp, operation,
store type, key, size, hit/miss, latency, action hash. The action hash enables
AC→CAS graph reconstruction from logs alone.

This is the Go/Python boundary. Go writes logs, Python reads them.

### 6b: Cache Trace Simulator

Python tool that replays access logs through configurable cache models (LRU,
LFU, GDSF, RL). Outputs per-policy hit rate, rebuild cost, eviction count,
cascade depth.

Also serves as the RL training environment.

### 6c: RL Agent

Dueling DQN trained offline on access log replay.

**State features per cache entry:**

- Access features — recency, frequency, inter-arrival time
- Object features — size, estimated rebuild cost
- Graph features — downstream dependents, DAG depth, shared ratio
- Lifecycle features — branch type, age, CI vs developer

The graph features are the novel part — no prior RL eviction work uses build
graph structure.

**Action:** Binary keep/evict per candidate under cache pressure.

**Reward:** Weighted combination of rebuild cost, cascade rebuilds, S3 API cost,
and space freed.

**Deployment:** Start with offline batch (periodic eviction plans). Graduate to
ONNX-in-Go for inline decisions.

### 6d: Graph Features

Three sources of build graph topology:

1. AC→CAS references from access logs (no external tooling needed)
2. BUILD file parser (Python)
3. `bazel query` (most accurate, used for validation)

### 7a: MCP Servers

Expose S3 stats, Valkey stats, Prometheus metrics, and eviction history to
LLM agents via Model Context Protocol.

### 7b: LLM Agents

- **Diagnostic** — correlates metrics across tiers to explain hit rate changes
- **Policy advisor** — compares RL against baselines, suggests reward tuning
- **Anomaly narrator** — detects metric anomalies, produces summaries

### 7c: Fine-Tuning

Accumulate diagnostic tuples. Fine-tune a small model for routine cases.
Full model handles novel situations.

## Boundaries

| Concern | Language |
|---|---|
| Cache serving, access logging, eviction execution | Go |
| RL training, LLM agents, graph extraction | Python |

Go/Python boundary: the access log file.
