## What and why

## Checklist

- [ ] Tests added or updated (algorithms: table test with a fake clock; stores: `ratelimittest.TestStore`)
- [ ] `make lint race` passes
- [ ] Algorithm changes are mirrored in `store/redis/ratelimit.lua` (`TestMatchesMemoryStore` passes)
- [ ] Charts regenerated with `make visualize` if algorithm behavior changed
- [ ] CHANGELOG.md updated under "Unreleased"
- [ ] Commit messages follow Conventional Commits
