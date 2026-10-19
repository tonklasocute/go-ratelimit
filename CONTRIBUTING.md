# Contributing

Thanks for helping. Bug reports, docs fixes and new adapters are all
welcome. For larger changes, such as a new algorithm, a new store or an API
change, please open an issue first so we can agree on the design.

## Layout

| Path | What | Module |
|---|---|---|
| `/` | `Limiter`, options, `Store` interface | core (stdlib only) |
| `algorithm/` | the six algorithms as pure functions | core |
| `store/memory/` | in-memory store | core |
| `middleware/`, `middleware/http/` | shared middleware pieces, net/http adapter | core |
| `ratelimittest/` | fake clock, store conformance suite | core |
| `cmd/visualize/` | chart generator | core |
| `store/redis/` | Redis store and its Lua script | separate |
| `store/redis/integration/` | tests against real Redis | separate, unpublished |
| `middleware/{chi,gin,echo,fiber,grpc}/` | framework adapters | separate each |
| `examples/` | runnable examples | separate, unpublished |

The core must stay free of dependencies outside the standard library and
compatible with Go 1.22. A `go.work` file ties the modules together for
local development; CI tests each module alone with `GOWORK=off`.

## Workflow

```sh
make race        # tests with the race detector in every module
make cover       # core coverage; keep it at or above 85%
make lint        # golangci-lint v2 in every module (no install needed)
make integration # Redis tests: REDIS_ADDR=host:port, or Docker running
```

- **Algorithms:** write table tests with a fixed timeline first (see
  `algorithm/*_test.go`). The property tests in `algorithm/property_test.go`
  must keep passing. Mirror every change in `store/redis/ratelimit.lua`;
  `TestMatchesMemoryStore` in `store/redis` compares the two decision by
  decision. Run `make visualize` and commit the updated charts.
- **Stores:** run `ratelimittest.TestStore` against the new store.
- **Adapters:** keep the same option names and behavior as `rlhttp`, and
  test with the framework's in-memory test server.
- Keep changes focused, and match the surrounding style and comment density.

## Commits and pull requests

Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/)
with the module as the scope, for example `feat(store/redis): ...`,
`fix(algorithm): ...`, or `docs: ...`. Add a line under "Unreleased" in
`CHANGELOG.md` for user-visible changes. The pull request template lists the
checks.

## Releases

Maintainers release with `scripts/release.sh`, which tags every module at
the same version. See [CHANGELOG.md](CHANGELOG.md#releasing).

## Security

Please report vulnerabilities privately through GitHub's "Report a
vulnerability" (Security tab) rather than in a public issue.
