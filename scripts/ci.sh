#!/usr/bin/env bash
# CI assertions that carry the svcrt architecture. See spec §8.
set -euo pipefail
cd "$(dirname "$0")/.."

fail() { echo "FAIL: $*" >&2; exit 1; }

# Library modules that must each stand alone with zero external dependencies.
#
# Derived from the go.mod files on disk rather than hand-listed: this list
# used to be duplicated here and in release.sh, and two hand-kept copies is
# how a module added at R1 ends up ungated with nobody noticing. Exemplars
# live under examples/ and are deliberately excluded -- they depend on the
# libraries and are handled separately below.
MODULES=()
for f in */go.mod; do
  [ -f "$f" ] || continue
  m=${f%/go.mod}
  [ "$m" = "examples" ] && continue
  MODULES+=("$m")
done
[ ${#MODULES[@]} -gt 0 ] || fail "no library modules found (expected */go.mod)"

# Modules whose bugs are likelier to be "hangs one run in fifty" than "returns
# the wrong value": goroutines, timers, shared state. A single green -race run
# says very little about those -- coverage and mutation testing are both
# blind to concurrency, so this is the one gate that catches it. Everything
# else runs at -count=1; these run at -count=10.
COUNT_MODULES=(lifecycle httpserver)

count_for() {
  for c in "${COUNT_MODULES[@]}"; do
    [ "$c" = "$1" ] && { printf '10'; return; }
  done
  printf '1'
}

for m in "${MODULES[@]}"; do
  echo "== $m =="

  # Spec P2: useful with no other svcrt module present. GOWORK=off is the
  # point -- go.work masks version skew locally, so CI must run without it.
  (cd "$m" && GOWORK=off go vet ./...) || fail "$m: go vet"
  (cd "$m" && GOWORK=off go test -race -count=$(count_for "$m") ./...) || fail "$m: go test"

  # Spec §8.2: zero requires. Exactly one line -- the module itself.
  # Assigned inside `if !` so a go list failure reports through fail() with
  # its label; a bare `n=$(...)` under set -e aborts before we get here and
  # the operator is left guessing which module and which check broke.
  if ! n=$(cd "$m" && GOWORK=off go list -m all | wc -l | tr -d ' '); then
    fail "$m: go list -m all"
  fi
  [ "$n" -eq 1 ] || fail "$m: has module dependencies ($n lines from 'go list -m all')"
done

# Spec §4.1: contract imports "context" and nothing else.
imports=$(cd contract && GOWORK=off go list -f '{{join .Imports "\n"}}' ./... | sort -u)
for i in $imports; do
  [ "$i" = "context" ] || fail "contract imports $i; only \"context\" is permitted"
done

# contract's low `go` directive is a compatibility commitment, not an
# accident: it is the module intended to freeze at v1 and be imported by every
# generated service, so it must keep building on the oldest toolchain those
# services might be on. `go mod tidy` or `go get` under a newer toolchain
# rewrites that line silently, and nothing else in the repo would notice --
# the workspace and CI both run a newer Go. Raising it is a deliberate,
# breaking decision; make it here, on purpose.
grep -q '^go 1\.22$' contract/go.mod ||
  fail "contract/go.mod no longer declares 'go 1.22'; that floor is a deliberate compatibility commitment for the one module that freezes (spec §4). If raising it is intended, change it here too."

# Exemplars depend on unpublished modules, so unlike the libraries they run
# WITH the workspace -- this is the one place go.work is load-bearing.
# Derived rather than listed, same reasoning as MODULES above: a second
# exemplar added later must not be silently ungated.
for f in examples/*/go.mod; do
  [ -f "$f" ] || continue
  e=${f%/go.mod}
  echo "== $e =="
  (cd "$e" && go vet ./...) || fail "$e: go vet"
  (cd "$e" && go test -race ./...) || fail "$e: go test"
done

echo "OK"
