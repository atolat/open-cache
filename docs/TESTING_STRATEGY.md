# Testing Strategy

## Layers

1. Go unit + integration tests
2. Real workload test bed (Envoy Proxy)
3. Synthetic workload generator
4. Cache trace simulator (policy comparison + RL training)

## Go Tests

### Unit Tests

Per-package `_test.go` files covering:

- **S3 store** — key generation, round-trips against MinIO, error handling
- **Valkey store** — round-trips, TTL, connection failures
- **Tiered store** — both tiers receive writes, L1 hit/miss paths, singleflight dedup, graceful degradation
- **Completeness checker** — all CAS refs present vs missing, malformed protos, batched FindMissing
- **HTTP server** — URL parsing, PUT/GET/HEAD responses, content-length enforcement, invalid input
- **gRPC server** — all REAPI service methods

### Integration Tests

MinIO + Valkey via docker-compose. Full Bazel build round-trips, cross-protocol
reads/writes, Valkey restart recovery, large blob multipart, concurrent access dedup.

## Envoy Proxy Test Bed

~36K targets, ~12.8K build actions per full build. C++ + protobuf with deep
dependency chains and shared intermediates. Multiple build configs create
overlapping cache keyspaces.

With access logging enabled, each build produces traces usable as simulator input
and RL training data.

## Synthetic Monorepo Generator

Python tool that generates valid Bazel workspaces with tunable parameters
(target count, depth, fan-out, shared ratio, file sizes, branch count, change rate).

Same seed → same workspace → same traces. Enables controlled experiments that
aren't possible with a real project.

## Definition of Done

| Phase | Ship when |
|---|---|
| 1 (HTTP+S3) | Unit + integration pass. Bazel round-trips with MinIO. |
| 2 (Completeness) | AC with missing CAS refs returns 404. |
| 3 (gRPC) | All REAPI services pass. Bazel works with `grpc://`. |
| 4 (Valkey L1) | Hit path, miss-through, singleflight all tested. |
| 5 (Helm) | `helm template` valid. `helm install --dry-run` succeeds. |
| 6 (RL) | Training converges. Beats LRU on training traces. |
| 7 (Agents) | Coherent diagnostic on test scenario. |
