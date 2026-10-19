# Changelog

All notable changes are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/). All modules are released together
with the same version (see [Releasing](#releasing)).

## [Unreleased]

## [0.1.0] - 2026-10-19

First release of every module: the core (`v0.1.0`), `store/redis`, and the
`chi`, `gin`, `echo`, `fiber` and `grpc` middleware.

### Added

- Core `Limiter` interface with `Allow`, `AllowN` (n = 0 peeks) and `Reset`.
  Rates are `PerSecond`/`PerMinute`/`PerHour`/`Per`, configured with
  functional options. Includes an injectable `Clock`, fail-open and
  fail-closed policies with an `OnError` hook, and composite rules applied
  atomically.
- Six algorithms in package `algorithm`: fixed window, sliding window log,
  sliding window counter, token bucket, leaky bucket (meter) and GCRA (the
  default). Each is a pure, allocation-free state machine.
- `store/memory`: a sharded in-memory store with a TTL cleanup goroutine,
  `Close`, and a key cap with LRU eviction.
- `store/redis` module: one Lua script with all algorithms (EVALSHA with
  EVAL fallback), Redis server time, hash-tagged keys for Redis Cluster, and
  a key prefix.
- Middleware: `middleware/http` (stdlib), and the `chi`, `gin` (Gin),
  `echo` (Echo v5), `fiber` (Fiber v3) and `grpc` modules. Key extractors
  cover IP with a trusted-proxy list, header, context value and route + IP.
  Responses support the IETF `RateLimit`/`RateLimit-Policy` headers (draft
  11), legacy `RateLimit-*` and `X-RateLimit-*` headers, `Retry-After`, a JSON
  429 body, and skip, deny and error handlers.
- `ratelimittest`: a fake clock and a conformance suite for `Store`
  implementations.
- `cmd/visualize`: SVG charts of every algorithm under the same bursty
  traffic.
- Benchmarks, examples (net/http, Gin per plan, distributed with Docker
  Compose and k6), and CI.

## Releasing

This repository holds several Go modules. Go finds a module that lives in a
subdirectory through a tag prefixed with that directory, so one release is
several tags on the same commit:

| Module | Tag for v0.1.0 |
|---|---|
| `github.com/tonklasocute/go-ratelimit` | `v0.1.0` |
| `github.com/tonklasocute/go-ratelimit/store/redis` | `store/redis/v0.1.0` |
| `github.com/tonklasocute/go-ratelimit/middleware/chi` | `middleware/chi/v0.1.0` |
| `github.com/tonklasocute/go-ratelimit/middleware/gin` | `middleware/gin/v0.1.0` |
| `github.com/tonklasocute/go-ratelimit/middleware/echo` | `middleware/echo/v0.1.0` |
| `github.com/tonklasocute/go-ratelimit/middleware/fiber` | `middleware/fiber/v0.1.0` |
| `github.com/tonklasocute/go-ratelimit/middleware/grpc` | `middleware/grpc/v0.1.0` |

`examples` and `store/redis/integration` are modules too, but they are not
published and get no tags.

Sub-modules use `replace` directives that point at the local core, so
development needs no published versions. Go ignores `replace` in
dependencies, so before tagging, every sub-module's `require` of the core
must name the version being released. `scripts/release.sh` does this. It
checks that the tree is clean and on `main`, runs every module's tests,
updates those `require` lines, commits `chore(release): vX.Y.Z`, and creates
all the tags. It never pushes.

```sh
scripts/release.sh v0.1.0 --dry-run   # show what would happen
scripts/release.sh v0.1.0             # commit and tag locally
git push origin main v0.1.0 store/redis/v0.1.0 middleware/chi/v0.1.0 \
  middleware/gin/v0.1.0 middleware/echo/v0.1.0 middleware/fiber/v0.1.0 middleware/grpc/v0.1.0
```

Once the tags are pushed, `go get github.com/tonklasocute/go-ratelimit/middleware/gin@v0.1.0`
resolves the core at `v0.1.0` as well. Move the "Unreleased" entries under a
new version heading in the same release commit.

[Unreleased]: https://github.com/tonklasocute/go-ratelimit/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/tonklasocute/go-ratelimit/releases/tag/v0.1.0
