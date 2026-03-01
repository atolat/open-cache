# Testing Strategy

## Overview

Three layers of testing, each independently valuable:

1. **Unit + integration tests** for the Go cache server (standard Go testing)
2. **Real workload test bed** using Envoy Proxy (validates correctness + collects traces)
3. **Synthetic workload generator** (controlled experiments for RL training)

Plus the **cache trace simulator** which is both a testing tool and the RL training environment.

---

## 1. Go Cache Server Tests

### Unit Tests

Standard `_test.go` files alongside each package. Focus on:

**`internal/cache/s3/`**
- S3 key generation: verify `{prefix}/cas/{hash[:2]}/{hash}` format
- PutObject → GetObject round-trip (against MinIO in tests)
- HeadObject for existence checks
- Multipart upload triggers at correct size threshold
- Error handling: S3 unavailable, access denied, not found

**`internal/cache/valkey/`**
- Set/Get round-trip
- TTL expiry behavior
- Key not found returns appropriate error
- Connection failure handling

**`internal/cache/tiered/`**
- FastSlowStore write path: both stores receive data
- Read path: L1 hit serves immediately, L1 miss falls through to L2
- L1 miss → L2 hit → L1 populated on return
- singleflight deduplication: concurrent gets for same key result in one S3 fetch
- Partial failure: L1 down but L2 up still works (graceful degradation)

**`internal/cache/completeness/`**
- AC entry with all CAS refs present → returned normally
- AC entry with any CAS ref missing → returns 404
- AC entry with malformed ActionResult proto → returns error
- Empty ActionResult (no output files) → returned normally
- Large ActionResult with many output files → batched FindMissing

**`internal/server/http.go`**
- URL parsing: `/ac/{hash}`, `/cas/{hash}`, `/{instance}/ac/{hash}`
- PUT with Content-Length → 200
- PUT without Content-Length → 400
- GET hit → 200 with body
- GET miss → 404
- HEAD hit → 200, no body
- HEAD miss → 404
- Invalid hash (not 64-char hex) → 400
- Method not allowed (POST, DELETE) → 405

**`internal/server/grpc.go`** (Phase 3)
- GetCapabilities returns correct digest functions, compressors
- GetActionResult hit/miss
- UpdateActionResult stores and is retrievable
- FindMissingBlobs: all present → empty response
- FindMissingBlobs: some missing → returns missing set
- BatchReadBlobs: reads multiple blobs in one call
- BatchUpdateBlobs: writes multiple blobs in one call
- ByteStream.Read: streams blob data with correct offsets
- ByteStream.Write: accepts streamed uploads

### Integration Tests

Use `docker-compose` with MinIO + Valkey for local integration:

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

**Integration test scenarios:**
- Full Bazel build cycle: clean build → all PUTs → second build → all GETs hit
- Mixed HTTP + gRPC: write via HTTP, read via gRPC (and vice versa)
- Valkey restart: cache degrades to S3-only, recovers when Valkey returns
- Large blob (>5MB): triggers multipart upload to S3
- Concurrent access: 100 parallel requests for the same missing key → one S3 fetch

### Test Tooling

```bash
# Run unit tests
go test ./internal/... -v -race

# Run integration tests (requires docker-compose up)
go test ./internal/... -tags=integration -v

# Run with coverage
go test ./internal/... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

---

## 2. Real Workload Test Bed: Envoy Proxy

### Why Envoy

- ~36K configured targets, ~12.8K build actions per full build
- 10-64GB recommended cache size (realistic cache pressure)
- C++ + protobuf: deep dependency chains, shared intermediate libraries (`.o`, `.pic.o`)
- Multiple build configs (debug, release, ASAN, TSAN) create overlapping cache keyspaces
- Well-maintained Bazel build, public repo, reproducible
- Large enough to stress-test eviction policies meaningfully

### Setup

```bash
# Clone Envoy
git clone https://github.com/envoyproxy/envoy.git
cd envoy

# Configure to use open-cache
echo 'build --remote_cache=http://localhost:8080' >> .bazelrc.local
echo 'build --remote_upload_local_results=true' >> .bazelrc.local

# Full build (populates cache)
bazel build //source/exe:envoy-static

# Simulated development loop
# 1. Change a file in source/common/
# 2. Rebuild
# 3. Observe: which cache entries were hit vs rebuilt
# 4. Repeat with different files at different graph depths
```

### What We Measure

| Metric | Purpose |
|---|---|
| Cache hit rate | Overall effectiveness |
| Cold build time (empty cache) | Baseline |
| Warm build time (full cache) | Best case speedup |
| Incremental build time | Realistic developer workflow |
| Cache size after full build | Capacity planning |
| Number of unique CAS/AC entries | Object count at scale |
| Access pattern distribution | Input for RL training |

### Collecting Traces

With access logging enabled (Phase 6a), every Envoy build against open-cache produces a trace:

```bash
# Build Envoy, collect trace
bazel clean && bazel build //source/exe:envoy-static
# -> access_log.jsonl now contains ~12K+ entries

# Simulated CI loop (5 incremental builds with random changes)
for i in $(seq 1 5); do
  touch source/common/common/random_generator.cc  # simulate change
  bazel build //source/exe:envoy-static
done
# -> access_log.jsonl now contains CI-like patterns
```

These traces become the training data for the RL agent and the ground truth for policy comparison.

---

## 3. Synthetic Monorepo Generator

A Python tool that generates valid Bazel workspaces with tunable parameters for controlled
experiments.

### Why Synthetic

- **Controlled variables:** Vary one parameter (depth, fan-out, shared ratio) while holding
  others constant. Impossible with a real project.
- **Scale testing:** Generate 100-target, 1K-target, 10K-target, 100K-target repos.
- **Reproducible:** Same seed → same workspace → same build graph → same traces.
- **Fast iteration:** Generate, build, collect traces in minutes, not hours.

### Generator Parameters

```python
@dataclass
class MonorepoConfig:
    num_libraries: int = 500        # total library targets
    num_binaries: int = 50          # binary targets (link libraries)
    max_depth: int = 8              # max dependency chain depth
    avg_fan_out: float = 3.0        # average deps per target
    shared_lib_ratio: float = 0.15  # fraction of libs used by >5 targets
    file_size_distribution: str = "lognormal(mu=10, sigma=2)"  # bytes
    num_branches: int = 5           # simulated git branches
    change_rate_per_commit: float = 0.02  # fraction of files changed per commit
    seed: int = 42                  # reproducibility
```

### What It Generates

```
synthetic-repo/
  WORKSPACE
  BUILD  (root)
  libs/
    lib_0001/
      BUILD           # cc_library with deps on other libs
      lib_0001.cc     # stub file (genrule, no real code needed)
      lib_0001.h
    lib_0002/
      ...
  bins/
    bin_001/
      BUILD           # cc_binary linking libs
      main.cc
    ...
```

BUILD files contain real `cc_library` / `cc_binary` rules with valid `deps` edges. The
`.cc` / `.h` files are stubs (minimal valid C++ that compiles). This produces real Bazel
actions with real cache entries.

### Driver Script (Simulated Development)

```bash
# Generate monorepo
python -m tools.synthetic_monorepo generate --config large.yaml --output /tmp/synthetic

# Simulate CI: full build + N incremental builds
python -m tools.synthetic_monorepo simulate \
  --workspace /tmp/synthetic \
  --remote-cache http://localhost:8080 \
  --num-commits 20 \
  --change-rate 0.02 \
  --output traces/synthetic_large.jsonl
```

Each simulated commit: randomly selects files to change (respecting `change_rate`), runs
`bazel build`, collects the access trace. The output is a single `.jsonl` trace file ready
for the simulator.

---

## 4. Cache Trace Simulator

See [ARCHITECTURE_AI.md](ARCHITECTURE_AI.md) Phase 6b for full design.

The simulator serves dual purpose:
- **Testing:** Compare eviction policies against each other on real and synthetic traces
- **Training:** Provide the RL agent's training environment (gym-like interface)

### Quick Validation Workflow

```bash
# 1. Collect traces from Envoy build
# (access_log.jsonl produced by open-cache with logging enabled)

# 2. Run baseline comparison
python -m tools.simulator \
  --trace access_log.jsonl \
  --valkey-capacity 4GB \
  --policies lru,lfu,gdsf \
  --output results/baseline/

# 3. Check results
cat results/baseline/comparison.json
# {
#   "lru":  {"hit_rate": 0.72, "rebuild_cost": 14520, "evictions": 3400},
#   "lfu":  {"hit_rate": 0.68, "rebuild_cost": 16200, "evictions": 3100},
#   "gdsf": {"hit_rate": 0.75, "rebuild_cost": 12800, "evictions": 3600}
# }

# 4. Train RL agent
python -m tools.rl_agent train \
  --trace access_log.jsonl \
  --episodes 5000 \
  --output models/eviction_v1.onnx

# 5. Evaluate RL agent against baselines
python -m tools.simulator \
  --trace access_log.jsonl \
  --valkey-capacity 4GB \
  --policies lru,gdsf,rl \
  --rl-model models/eviction_v1.onnx \
  --output results/rl_comparison/
```

---

## 5. Testing Build Order

This order ensures each testing layer builds on the previous:

| Step | What | Depends On |
|---|---|---|
| 1 | Go unit tests for Phases 1-2 | Phase 1-2 code |
| 2 | Integration tests with MinIO + Valkey | docker-compose setup |
| 3 | Run Envoy builds against open-cache | Phase 1-2 working + access logging (Phase 6a) |
| 4 | Build cache trace simulator | Access logs from step 3 |
| 5 | Evaluate baseline policies on Envoy traces | Simulator from step 4 |
| 6 | Build synthetic monorepo generator | Independent (Python tool) |
| 7 | Generate traces at multiple scales | Generator from step 6 |
| 8 | Train RL agent on combined traces | Simulator + traces from steps 5+7 |
| 9 | Validate RL on held-out Envoy traces | Trained model from step 8 |
| 10 | Generalization test: train on synthetic, eval on Envoy | Steps 7+8+9 |

Step 10 is the key research result: does a policy trained on synthetic data generalize to
real-world build patterns?

---

## 6. CI Pipeline (GitHub Actions)

```yaml
# .github/workflows/test.yml
name: Test
on: [push, pull_request]
jobs:
  unit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.22' }
      - run: go test ./internal/... -v -race -coverprofile=coverage.out
      - uses: codecov/codecov-action@v4

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

---

## 7. Definition of Done per Phase

| Phase | Tests Required to Ship |
|---|---|
| 1 (HTTP+S3) | Unit tests for HTTP handler + S3 store. Integration test: Bazel build round-trip with MinIO. |
| 2 (Completeness) | Unit tests for completeness checker. Integration: AC with missing CAS refs returns 404. |
| 3 (gRPC) | Unit tests for all REAPI services. Integration: `grpc_cli` or Bazel with `--remote_cache=grpc://`. |
| 4 (Valkey L1) | Unit tests for tiered store. Integration: Valkey hit path, miss-through-to-S3, singleflight. |
| 5 (Helm) | `helm template` renders valid YAML. `helm install --dry-run` succeeds. `ct lint` passes. |
| 6a (Logging) | Unit tests for log format. Integration: Bazel build produces valid `.jsonl` trace. |
| 6b (Simulator) | Python unit tests. Reproduces known LRU behavior on synthetic trace. |
| 6c (RL Agent) | Training converges (loss decreases). Beats LRU on training traces. |
| 6d (Graph) | Feature extraction produces correct DAG from known ActionResult set. |
| 7a (MCP) | MCP server responds to tool calls with correct data. |
| 7b (Agents) | Agent produces coherent diagnostic output on a test scenario. |
