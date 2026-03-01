# open-cache

Building a remote build cache from scratch — one concept at a time.

## Learning Journal

Daily notes on what we learn as we design and build the cache.

- [Content Addressable Storage](learning/01-content-addressable-storage.md)

## Design Docs

Architecture decisions, analysis of existing systems, and the build plan.

- [Project Brief](PROJECT_BRIEF.md) — full architecture + phased build plan
- [AI/RL Architecture](ARCHITECTURE_AI.md) — learned eviction + LLM agents
- [Testing Strategy](TESTING_STRATEGY.md) — test layers + CI pipeline

## Research

Deep dives into the remote cache ecosystem.

- [Ecosystem Analysis](RESEARCH.md) — what exists and where the gaps are
- [NativeLink](ANALYSIS_NATIVELINK.md) — Rust store traits, S3, FastSlowStore
- [Buildbarn](ANALYSIS_BUILDBARN.md) — BlobAccess, completeness checking
- [Buildfarm](ANALYSIS_BUILDFARM.md) — Helm chart, Redis patterns
