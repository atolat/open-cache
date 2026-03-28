# open-cache

S3-primary remote build cache for Bazel.

## Install

```bash
helm install open-cache oci://ghcr.io/atolat/charts/open-cache
```

See [Changelog](CHANGELOG.md) for the latest release.

## Learning Journal

- [Content Addressable Storage](learning/01-content-addressable-storage.md)
- [Bazel Remote Cache Protocol](learning/02-bazel-remote-cache-protocol.md)

## Design

- [Design Document](PROJECT_BRIEF.md) — architecture, protocols, build phases
- [RL Eviction + Agents](ARCHITECTURE_AI.md) — learned eviction, MCP servers, LLM agents
- [Testing Strategy](TESTING_STRATEGY.md) — test layers, workload generation
