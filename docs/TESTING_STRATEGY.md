# Testing Strategy

## Layers

1. Go unit + integration tests (standard `_test.go`)
2. Real workload test bed (Envoy Proxy)
3. Synthetic workload generator (controlled experiments)
4. Cache trace simulator (policy comparison + RL training environment)

## Go Tests

### Unit Tests

Per-package `_test.go` files:

**`internal/cache/s3/`** — key generation format, PutObject/GetObject round-trip
against MinIO, HeadObject, multipart upload threshold, error handling (unavailable,
access denied, not found).

**`internal/cache/valkey/`** — Set/Get round-trip, TTL expiry, key not found, connection
failure handling.

**`internal/cache/tiered/`** — both stores receive writes, L1 hit serves immediately,
L1 miss falls through to L2, L1 populated on L2 hit, singleflight dedup (concurrent
gets → one S3 fetch), graceful degradation (L1 down, L2 still works).

**`internal/cache/completeness/`** — all CAS refs present → return AC entry, any CAS
ref missing → 404, malformed ActionResult → error, empty ActionResult → returned,
large ActionResult → batched FindMissing.

**`internal/server/http.go`** — URL parsing (`/ac/{hash}`, `/cas/{hash}`,
`/{instance}/ac/{hash}`), PUT with/without Content-Length, GET hit/miss, HEAD hit/miss,
invalid hash → 400, wrong method → 405.

**`internal/server/grpc.go`** — GetCapabilities response, GetActionResult hit/miss,
FindMissingBlobs, BatchRead/BatchUpdate, ByteStream read/write.

### Integration Tests

docker-compose with MinIO + Valkey:

```yaml
services:
  minio:
    image: minio/minio
    command: server /data
  valkey:
    image: valkey/valkey:8
  open-cache:
    build: .
    depends_on: [minio, valkey]
```

Scenarios:

- Full Bazel build cycle: clean → all PUTs → rebuild → all GETs hit
- Cross-protocol: write via HTTP, read via gRPC (and vice versa)
- Valkey restart: degrades to S3-only, recovers on reconnect
- Large blob (>5MB): triggers multipart upload
- Concurrent access: 100 parallel requests for same key → one S3 fetch

### Running

```bash
go test ./internal/... -v -race                    # unit
go test ./internal/... -tags=integration -v        # integration (needs docker-compose)
go test ./internal/... -coverprofile=coverage.out   # coverage
```

## Envoy Proxy Test Bed

~36K targets, ~12.8K build actions per full build. C++ + protobuf with deep dependency
chains and shared intermediates. Multiple build configs (debug, release, ASAN, TSAN)
create overlapping cache keyspaces.

```bash
git clone https://github.com/envoyproxy/envoy.git
cd envoy
echo 'build --remote_cache=http://localhost:8080' >> .bazelrc.local
echo 'build --remote_upload_local_results=true' >> .bazelrc.local

# Full build (populates cache)
bazel build //source/exe:envoy-static

# Incremental builds (change files at different graph depths, measure hit rate)
```

With access logging enabled, each build produces a `.jsonl` trace usable for
simulator input and RL training.

## Synthetic Monorepo Generator

Python tool generating valid Bazel workspaces with tunable parameters.

```python
@dataclass
class MonorepoConfig:
    num_libraries: int = 500
    num_binaries: int = 50
    max_depth: int = 8
    avg_fan_out: float = 3.0
    shared_lib_ratio: float = 0.15
    file_size_distribution: str = "lognormal(mu=10, sigma=2)"
    num_branches: int = 5
    change_rate_per_commit: float = 0.02
    seed: int = 42
```

Generates `cc_library` / `cc_binary` rules with valid `deps` edges and stub `.cc`/`.h`
files that compile. Same seed → same workspace → same traces.

```bash
python -m tools.synthetic_monorepo generate --config large.yaml --output /tmp/synthetic
python -m tools.synthetic_monorepo simulate \
  --workspace /tmp/synthetic \
  --remote-cache http://localhost:8080 \
  --num-commits 20 \
  --output traces/synthetic_large.jsonl
```

## CI (GitHub Actions)

```yaml
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22' }
      - run: go test ./internal/... -v -race -coverprofile=coverage.out

  integration:
    runs-on: ubuntu-latest
    services:
      minio:
        image: minio/minio
        ports: ['9000:9000']
        options: --entrypoint "minio server /data"
      valkey:
        image: valkey/valkey:8
        ports: ['6379:6379']
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22' }
      - run: go test ./internal/... -tags=integration -v

  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: golangci/golangci-lint-action@v4

  simulator:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with: { python-version: '3.12' }
      - run: pip install -r tools/simulator/requirements.txt
      - run: python -m pytest tools/simulator/tests/ -v
```

## Definition of Done

| Phase | Ship when |
|---|---|
| 1 (HTTP+S3) | Unit + integration pass. Bazel build round-trips with MinIO. |
| 2 (Completeness) | AC with missing CAS refs returns 404. |
| 3 (gRPC) | All REAPI services pass. Bazel works with `grpc://`. |
| 4 (Valkey L1) | Valkey hit path, miss-through-to-S3, singleflight all tested. |
| 5 (Helm) | `helm template` valid. `helm install --dry-run` succeeds. |
| 6a (Logging) | Bazel build produces valid `.jsonl` trace. |
| 6b (Simulator) | Reproduces known LRU behavior on synthetic trace. |
| 6c (RL) | Training converges. Beats LRU on training traces. |
| 6d (Graph) | Correct DAG from known ActionResult set. |
| 7a (MCP) | Servers respond to tool calls with correct data. |
| 7b (Agents) | Coherent diagnostic on test scenario. |
