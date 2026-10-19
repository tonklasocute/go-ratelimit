#!/usr/bin/env bash
# Tag every published module of go-ratelimit at the same version.
#
#   scripts/release.sh v0.1.0            # bump requires, commit, tag (no push)
#   scripts/release.sh v0.1.0 --dry-run  # print what would happen
#
# Go finds a module in a subdirectory of a repository by a tag prefixed with
# that directory, so one release is several tags on the same commit:
#
#   v0.1.0                     github.com/tonklasocute/go-ratelimit
#   store/redis/v0.1.0         github.com/tonklasocute/go-ratelimit/store/redis
#   middleware/gin/v0.1.0      github.com/tonklasocute/go-ratelimit/middleware/gin
#   ...
#
# Before tagging, the script points each sub-module's require of the core
# (and of other modules of this repo) at the new version, so that users of a
# sub-module resolve a real core version. The local replace directives stay:
# Go ignores them in dependencies, and they keep development offline.
# examples and store/redis/integration are not published and are not tagged.
set -euo pipefail

VERSION=${1:-}
DRY_RUN=${2:-}
ROOT=github.com/tonklasocute/go-ratelimit
PUBLISHED=(store/redis middleware/chi middleware/gin middleware/echo middleware/fiber middleware/grpc)
DEPENDENTS=("${PUBLISHED[@]}" store/redis/integration examples)

die() { echo "release: $*" >&2; exit 1; }
run() {
  if [[ $DRY_RUN == --dry-run ]]; then echo "+ $*"; else "$@"; fi
}

[[ $VERSION =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "usage: $0 vMAJOR.MINOR.PATCH [--dry-run]"
[[ ${VERSION%%.*} == v0 || ${VERSION%%.*} == v1 ]] || die "v2+ needs /vN module paths first"
cd "$(git rev-parse --show-toplevel)"
[[ -z $(git status --porcelain) ]] || die "working tree is not clean"
[[ $(git branch --show-current) == main ]] || die "release from main"
for tag in "$VERSION" "${PUBLISHED[@]/%//$VERSION}"; do
  git rev-parse -q --verify "refs/tags/$tag" >/dev/null && die "tag $tag already exists"
done

echo "Checking every module..."
for m in . "${DEPENDENTS[@]}"; do
  (cd "$m" && GOWORK=off go vet ./... && GOWORK=off go test ./...) >/dev/null || die "tests failed in $m"
done

echo "Pointing sub-modules at $VERSION..."
for m in "${DEPENDENTS[@]}"; do
  # Direct requires of modules from this repository (not the replace lines).
  deps=$(grep -E "^(require )?[[:space:]]*$ROOT(/[^ ]+)? v" "$m/go.mod" | grep -v '=>' |
    sed -E 's/^(require )?[[:space:]]*//; s/ .*//')
  for dep in $deps; do
    run go -C "$m" mod edit -require="$dep@$VERSION"
  done
done

if [[ $DRY_RUN != --dry-run ]] && [[ -n $(git status --porcelain) ]]; then
  git commit -qam "chore(release): $VERSION"
fi

echo "Tagging..."
run git tag -a "$VERSION" -m "go-ratelimit $VERSION"
for m in "${PUBLISHED[@]}"; do
  run git tag -a "$m/$VERSION" -m "go-ratelimit $m $VERSION"
done

echo
echo "Done. Review, then push the commit and all tags:"
echo "  git push origin main $VERSION ${PUBLISHED[*]/%//$VERSION}"
