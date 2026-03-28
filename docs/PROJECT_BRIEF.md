# Design Document

## Goals

- Cache-only remote cache for Bazel (no remote execution)
- S3 as the primary store — no local disk required, pods are stateless
- Tiered caching: L1 RAM, L2 disk, L3 S3
- HTTP and gRPC (REAPI v2) protocols
- Helm chart for K8s/EKS deployment
- Apache 2.0

## Current State (v0.1.0)

Go HTTP server proxying Bazel cache requests to S3. Deployed on EKS
via Helm chart with NLB and TLS.

```
Bazel → HTTPS → NLB (TLS termination) → Go server → S3
```

## Target Architecture

```
Bazel → HTTPS → NLB → Go server → L1 RAM (AC + small CAS)
                                 → L2 Disk (AC + medium CAS)
                                 → L3 S3 (everything)
```

**Write path:** Stream to all tiers simultaneously using `io.MultiWriter`.

**Read path:** L1 → L2 → L3, populate upper tiers on miss.

### Tiering Policy

| Tier | Stores | Default Size Limit |
|------|--------|--------------------|
| L1 RAM | All AC + CAS under threshold | 4 GiB total, 1 MiB per blob |
| L2 Disk | All AC + CAS under threshold | 100 GiB total, 256 MiB per blob |
| L3 S3 | Everything | Unlimited |

AC entries are always in all tiers (small, always hot).
CAS blobs are routed by size — small blobs go to RAM, medium to disk,
everything to S3.

## Protocol Reference

### HTTP (Simple Cache)

```
PUT  /[instance/]ac/{hash}    Store ActionResult
GET  /[instance/]ac/{hash}    Fetch ActionResult
HEAD /[instance/]ac/{hash}    Check existence

PUT  /[instance/]cas/{hash}   Store blob
GET  /[instance/]cas/{hash}   Fetch blob
HEAD /[instance/]cas/{hash}   Check existence
```

200 on hit, 404 on miss. Content-Length required on PUT.

### gRPC (REAPI v2) — planned

Capabilities, ActionCache, CAS, ByteStream services.

## Completeness Checking (BWOB) — planned

On every AC read, verify all referenced CAS hashes still exist.
Return 404 if any are missing. Forces Bazel to rebuild cleanly.

## S3 Key Layout

```
{prefix}/cas/{hash[0:2]}/{hash}
{prefix}/ac/{hash[0:2]}/{hash}
```

Note: v0.1.0 uses flat keys (`cas/{hash}`, `ac/{hash}`).
Sharded keys are planned for better S3 partition distribution.

## Build Phases

1. ~~HTTP + S3~~ — **done** (v0.1.0)
2. Completeness checking
3. L1 RAM + L2 disk caching tiers
4. Prometheus metrics
5. gRPC REAPI
6. Access logging + RL eviction (see [ARCHITECTURE_AI.md](ARCHITECTURE_AI.md))
7. MCP servers + LLM agents (see [ARCHITECTURE_AI.md](ARCHITECTURE_AI.md))
