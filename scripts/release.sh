#!/usr/bin/env bash
# Tag one svcrt module. Multi-module repos need a per-module tag prefix, so
# this exists at R0 rather than R1: three modules already means three prefixes,
# and the script is much easier to write now than at six.
#
#   scripts/release.sh contract v0.1.0   ->  tag contract/v0.1.0
set -euo pipefail
cd "$(dirname "$0")/.."

# MODULES, COUNT_MODULES and count_for, derived from disk and shared with
# ci.sh. This list and ci.sh's used to be two hand-kept copies of the same
# thing, which is how a module added later ends up ungated -- or, here,
# untaggable -- with nobody noticing.
. scripts/lib.sh

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
# -count comes from the same policy ci.sh uses: a module whose bugs are
# "hangs one run in fifty" gets ten runs. Tagging is the point after which a
# version is permanent, so this is the last place that policy can still be
# applied -- it must not be weaker here than in CI.
echo "verifying $module in isolation..."
(cd "$module" && GOWORK=off go vet ./...)
(cd "$module" && GOWORK=off go test -race -count=$(count_for "$module") ./...)
# Assigned inside `if !` so a go list failure reports here rather than
# aborting through errexit with no explanation of which check failed.
if ! n=$(cd "$module" && GOWORK=off go list -m all | wc -l | tr -d ' '); then
  echo "$module: 'go list -m all' failed; refusing to tag" >&2
  exit 1
fi
# Exactly one line -- the module itself -- unless the module is named in
# DEP_EXEMPT (lib.sh), in which case the assertion flips: it must have MORE
# than one line, because an exemption for a module that has quietly gone
# dependency-free is a stale exemption, and a stale exemption is a lie in the
# gate. Same two-way shape as ci.sh, reading the same list through the same
# dep_exempt_reason -- a second copy of the exempt names here is how this
# check drifted from ci.sh's in the first place.
if reason=$(dep_exempt_reason "$module"); then
  [ "$n" -gt 1 ] || {
    echo "$module: DEP_EXEMPT says '$reason', but 'go list -m all' now returns $n line(s) -- $module is dependency-free again. Remove the now-false exemption from DEP_EXEMPT in scripts/lib.sh. Refusing to tag." >&2
    exit 1
  }
else
  [ "$n" -eq 1 ] || { echo "$module has module dependencies ($n lines from 'go list -m all'); refusing to tag" >&2; exit 1; }
fi

git tag -a "$tag" -m "$tag"
echo
echo "created $tag. Push it with:"
echo "    git push origin $tag"
