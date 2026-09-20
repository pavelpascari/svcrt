#!/usr/bin/env bash
# Coverage ratchet: a module may never drop below its recorded floor.
#
# Above the floor PASSES and prints the gap. Coverage improving must not break
# a build. The gap is printed on every run because the honest weakness of a
# minimum-only ratchet is drift: a floor left far below actual stops catching
# regressions above it, and printing is the only mitigation that costs nothing.
#
# Four behaviours, all four demonstrated by breaking them rather than argued:
#   below floor                     -> FAIL
#   above floor                     -> PASS, and the gap is printed
#   module on disk with no entry    -> FAIL (a new module silently ungated)
#   entry naming no module on disk  -> FAIL (a rename leaves the gate inert)
set -euo pipefail
cd "$(dirname "$0")/.."

fail() { echo "FAIL: $*" >&2; exit 1; }

# MODULES and EXEMPLARS are derived from disk in lib.sh and shared with ci.sh
# and release.sh. Deriving them here instead would be a second list to keep in
# step, which is the duplication lib.sh's own header exists to end.
. scripts/lib.sh

FLOORS=coverage-floors.txt
[ -f "$FLOORS" ] || fail "$FLOORS is missing; every module must have a recorded floor"

floor_for() {
  awk -v m="$1" '$1 == m { print $2; found=1 } END { if (!found) exit 1 }' "$FLOORS"
}

ALL=("${MODULES[@]}" "${EXEMPLARS[@]}")

# FORWARD: every module on disk must have a floor. A new module with no entry
# is a module whose coverage nothing checks -- the R3 failure repeated.
for m in "${ALL[@]}"; do
  floor_for "$m" >/dev/null || fail "$m has no entry in $FLOORS; add one or it is ungated"
done

# INVERSE: every floor must name a module that exists. A rename leaves an entry
# matching nothing, and the gate quietly stops applying while CI still says OK.
while read -r name _; do
  case "$name" in ''|\#*) continue ;; esac
  found=false
  for m in "${ALL[@]}"; do [ "$m" = "$name" ] && found=true; done
  $found || fail "$FLOORS names '$name', which is not a module on disk"
done < "$FLOORS"

status=0
for m in "${ALL[@]}"; do
  floor=$(floor_for "$m")
  # Assigned inside `if !` so a go test failure reports through fail() with its
  # label rather than aborting under errexit with no indication of which module.
  if ! out=$(cd "$m" && go test -coverprofile=/tmp/cov.$$.out ./... 2>&1); then
    fail "$m: go test
$out"
  fi
  actual=$(cd "$m" && go tool cover -func=/tmp/cov.$$.out | tail -1 | awk '{print $NF}' | tr -d '%')
  rm -f /tmp/cov.$$.out

  if awk -v a="$actual" -v f="$floor" 'BEGIN { exit !(a < f) }'; then
    echo "FAIL: $m coverage $actual% is below its floor of $floor%" >&2
    status=1
    continue
  fi
  gap=$(awk -v a="$actual" -v f="$floor" 'BEGIN { printf "%.1f", a - f }')
  printf '  %-18s %6s%%  (floor %s%%, gap +%s)\n' "$m" "$actual" "$floor" "$gap"
done

[ "$status" -eq 0 ] || fail "coverage dropped below floor in at least one module"
echo "coverage OK"
