# Bazel Remote Cache Protocol

## How Bazel Builds

Bazel breaks a build into **actions** — discrete steps like "compile this file" or
"link these objects." Each action has defined inputs and outputs. A simple C++ project:

```
cc_library(name = "greeter", srcs = ["greeter.cc"], hdrs = ["greeter.h"])
cc_binary(name = "hello", srcs = ["main.cc"], deps = [":greeter"])
```

Produces this action graph:

```
greeter.cc + greeter.h
    │
    ▼
[compile greeter.cc] → greeter.o
    │
    ▼
[archive] → libgreeter.a
    │
main.cc ──→ [compile main.cc] → main.o
                                  │
                                  ▼
                    [link] → hello binary
```

Changes propagate downstream. Change `greeter.h` → everything rebuilds. Change
only `main.cc` → only main.o and the link step re-run. The greeter compilation
is untouched.

Bazel can visualize this graph natively:

```bash
bazel query 'deps(//:hello)' --output graph | dot -Tpng -o graph.png
```

## Two Stores: AC and CAS

Every Bazel remote cache has two logical stores:

**AC (Action Cache)** — keyed by a hash of the action (command + inputs + environment).
The value is an `ActionResult` protobuf that says "this action produced these output
files, here are their content hashes." Small — hundreds of bytes.

**CAS (Content Addressable Storage)** — keyed by `sha256(content)`. The value is raw
bytes — the actual compiled object files, binaries, etc. Size varies from bytes to
megabytes.

AC entries *point to* CAS entries by hash. An AC entry for "compile greeter.cc"
looks like:

```
output_files {
  path: "bazel-out/.../greeter.o"
  digest { hash: "c6a1e9cb...", size: 3864 }   → points to CAS
}
output_files {
  path: "bazel-out/.../greeter.d"
  digest { hash: "2123d52b...", size: 65962 }   → points to CAS
}
stdout_digest { hash: "e3b0c442..." }   → sha256 of empty string
stderr_digest { hash: "e3b0c442..." }   → sha256 of empty string
```

You can decode these with `protoc --decode_raw < /path/to/ac/entry`.

## HTTP Protocol

Bazel's "Simple HTTP Cache" protocol. Config: `--remote_cache=http://host:8080`

**Cache miss (first build):**

```
GET /ac/{action_hash}     → 404 (not cached)
  Bazel builds locally
PUT /cas/{content_hash}   → 200 (upload output blob)
PUT /cas/{content_hash}   → 200 (upload another blob)
PUT /ac/{action_hash}     → 200 (upload action result)
```

CAS blobs are uploaded **before** the AC entry. If Bazel crashes mid-upload,
no AC entry exists, so no one will try to reference the orphaned CAS blobs.
AC-first would risk pointing to blobs that don't exist.

**Cache hit (second build):**

```
GET /ac/{action_hash}     → 200 (hit — returns ActionResult)
  Bazel skips building, uses cached outputs
```

With `--remote_download_minimal` (default since Bazel 7), Bazel doesn't even
download CAS blobs on a hit — it trusts the digests. This is "Build Without
the Bytes" (BWOB).

**What Bazel reports:**

```
14 processes: 2 remote cache hit, 10 internal, 2 darwin-sandbox
```

- `remote cache hit` — action skipped, outputs came from cache
- `darwin-sandbox` — action ran locally (cache miss)
- `internal` — Bazel bookkeeping (symlinks, module maps)

## What This Means for Our Nginx Proxy

The entire protocol surface nginx needs to handle:

| Method | Path | S3 Operation |
|--------|------|-------------|
| GET    | `/ac/{hash}` | GetObject |
| GET    | `/cas/{hash}` | GetObject |
| PUT    | `/ac/{hash}` | PutObject |
| PUT    | `/cas/{hash}` | PutObject |
| HEAD   | `/ac/{hash}` | HeadObject |
| HEAD   | `/cas/{hash}` | HeadObject |

200 on hit, 404 on miss. Content-Length required on PUT. HTTP/1.1 required
(Bazel 9 rejects HTTP/1.0).

## BWOB and Completeness Checking

BWOB creates a failure mode: AC says "output is at CAS hash X" but X has been
evicted. Next client gets an AC hit pointing to missing data → build failure.

The fix (for our Go server later, not nginx): on every AC read, verify all
referenced CAS hashes still exist before returning the result. Return 404 if
any are missing. This forces Bazel to rebuild cleanly.

Nginx can't do this — it's a dumb proxy. Known v1 limitation.
