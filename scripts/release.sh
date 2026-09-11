#!/usr/bin/env bash
# Tag one svcrt module. Multi-module repos need a per-module tag prefix, so
# this exists at R0 rather than R1: three modules already means three prefixes,
# and the script is much easier to write now than at six.
#
#   scripts/release.sh contract v0.1.0   ->  tag contract/v0.1.0
set -euo pipefail
cd "$(dirname "$0")/.."

# Derived from the go.mod files on disk, not hand-listed. This list and
# ci.sh's used to be two hand-kept copies of the same thing, which is how a
# module added later ends up ungated -- or, here, untaggable -- with nobody
# noticing. Exemplars under examples/ are excluded: they are not released.
MODULES=()
for f in */go.mod; do
  [ -f "$f" ] || continue
  m=${f%/go.mod}
  [ "$m" = "examples" ] && continue
  MODULES+=("$m")
done

usage() {
  echo "usage: $0 <module> <version>" >&2
  echo "  module:  ${MODULES[*]}" >&2
  echo "  version: vMAJOR.MINOR.PATCH, optionally with a -prerelease suffix" >&2
  exit 2
}

[ $# -eq 2 ] || usage
module=$1
version=$2

found=false
for m in "${MODULES[@]}"; do
  [ "$m" = "$module" ] && found=true
done
$found || { echo "unknown module: $module" >&2; usage; }

if ! [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "malformed version: $version" >&2
  usage
fi

tag="$module/$version"

if [ -n "$(git status --porcelain)" ]; then
  echo "working tree is dirty; commit or stash first" >&2
  exit 1
fi
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  echo "tag already exists: $tag" >&2
  exit 1
fi

# Verify the module stands alone at exactly the state being tagged. GOWORK=off
# because consumers will not have our workspace.
echo "verifying $module in isolation..."
(cd "$module" && GOWORK=off go vet ./...)
(cd "$module" && GOWORK=off go test -race ./...)
# Assigned inside `if !` so a go list failure reports here rather than
# aborting through errexit with no explanation of which check failed.
if ! n=$(cd "$module" && GOWORK=off go list -m all | wc -l | tr -d ' '); then
  echo "$module: 'go list -m all' failed; refusing to tag" >&2
  exit 1
fi
[ "$n" -eq 1 ] || { echo "$module has module dependencies; refusing to tag" >&2; exit 1; }

git tag -a "$tag" -m "$tag"
echo
echo "created $tag. Push it with:"
echo "    git push origin $tag"
