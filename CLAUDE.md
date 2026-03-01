# open-cache

S3-primary, Valkey-L1, K8s-native Bazel remote cache. Go. Apache 2.0. Cache-only (no RBE).

## What This Is

A remote build cache that treats S3 as the primary store (no mandatory disk), with optional
Valkey L1 for hot blobs. Speaks both Bazel Simple HTTP Cache and gRPC REAPI. Deploys on
EKS/K8s via Helm. Novel research layer: graph-aware RL-based eviction + LLM operational agents.

## Rules

- Never add remote execution features -- cache-only forever
- S3 is source of truth; Valkey is purely additive L1
- Completeness checking on every AC read (BWOB safety)
- S3 keys: `{prefix}/{ac|cas}/{hash[0:2]}/{hash}`
- Go for the cache server, Python for RL/AI -- cleanly separated
- Access logs bridge Go (producer) and Python (consumer)
- Each phase must be independently valuable and shippable
- Standard Go project layout, no framework magic

## Architecture

```
Bazel --> HTTP :8080 / gRPC :9092
              |
        Cache Router (completeness check on AC reads)
         /          \
   Valkey L1      S3 L2
   (optional)     (always present, authoritative)
```

Write: simultaneous L1+L2 via FastSlowStore pattern
Read: L1 first, miss -> S3 fetch + L1 populate (singleflight for dedup)

## Build Phases

1. HTTP + S3 (MVP)
2. Completeness checking (BWOB-safe)
3. gRPC REAPI
4. Valkey L1 (FastSlowStore + singleflight)
5. Helm chart (IRSA, HPA, ServiceMonitor)
6. Access logging -> RL eviction agent -> graph-aware features
7. MCP servers + LLM diagnostic/policy agents

## Project Layout

```
cmd/open-cache/          -- entry point
internal/cache/          -- cache.go, s3/, valkey/, tiered/, completeness/
internal/server/         -- http.go, grpc.go
internal/config/         -- config.go
internal/metrics/        -- metrics.go
charts/open-cache/       -- Helm chart
tools/simulator/         -- Python cache trace simulator
tools/synthetic-monorepo/-- Python monorepo generator
tools/rl-agent/          -- Python RL training
```

## Key Dependencies

- `aws-sdk-go-v2` (S3), `google.golang.org/grpc`, `github.com/bazelbuild/remote-apis`
- `go-redis` (Valkey), `prometheus/client_golang`, `golang.org/x/sync/singleflight`
- `viper` (config)

## Learning & Teaching Mode

- Document learnings daily in `docs/learning/` with Mermaid visuals and code snippets
- MkDocs site renders at `mkdocs serve` -- all docs and learning entries are browsable
- Quiz the user and probe for deep intuitive understanding of every concept
- When coding: give hints and let the user write code themselves, don't dump solutions
- Small incremental daily progress, not one-night vibe-coded projects
- Every session should produce either a learning entry, code, or both

## Docs

- [docs/PROJECT_BRIEF.md](docs/PROJECT_BRIEF.md) -- Full architecture + phased build plan
- [docs/ARCHITECTURE_AI.md](docs/ARCHITECTURE_AI.md) -- RL eviction + LLM agent design
- [docs/TESTING_STRATEGY.md](docs/TESTING_STRATEGY.md) -- Testing approach + test bed
- [docs/RESEARCH.md](docs/RESEARCH.md) -- Ecosystem analysis + competitor deep dives
- [docs/ANALYSIS_NATIVELINK.md](docs/ANALYSIS_NATIVELINK.md) -- Store trait, S3, FastSlowStore
- [docs/ANALYSIS_BUILDBARN.md](docs/ANALYSIS_BUILDBARN.md) -- BlobAccess, completeness checking
- [docs/ANALYSIS_BUILDFARM.md](docs/ANALYSIS_BUILDFARM.md) -- Helm chart, Redis patterns
- `docs/learning/` -- Daily learning journal (rendered via MkDocs)
