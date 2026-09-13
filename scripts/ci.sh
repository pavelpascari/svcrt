#!/usr/bin/env bash
# CI assertions that carry the svcrt architecture. See spec §8.
set -euo pipefail
cd "$(dirname "$0")/.."

fail() { echo "FAIL: $*" >&2; exit 1; }

# MODULES, EXEMPLARS, COUNT_MODULES and count_for -- derived from disk and
# shared with release.sh, which applies the same -count policy before it tags.
# lib.sh also asserts every COUNT_MODULES name is a module that exists.
. scripts/lib.sh

# Global Constraints, every milestone spec: "gofmt -l . silent". Checked here,
# first, because a formatting break is cheap to catch before the test matrix
# runs. `gofmt -l` exits 0 whether or not it prints filenames -- a bare
# `gofmt -l . || fail` would never fire -- so the condition tests the output
# for non-emptiness, not the exit status.
unformatted=$(gofmt -l .)
[ -z "$unformatted" ] || fail "gofmt -l found unformatted files:
$unformatted"

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
#
# Derived in lib.sh rather than listed, same reasoning as MODULES: a second
# exemplar added later must not be silently ungated. Guarded here: an unmatched glob would leave the loop
# body unrun and this script would print OK having tested zero exemplars --
# every acceptance suite silently skipped. That is not hypothetical: fold the
# two exemplars into a single examples/go.mod (orders/ and worker/ as
# packages) and examples/*/go.mod matches nothing, while the library loop
# above already skips "examples" by name.
[ ${#EXEMPLARS[@]} -gt 0 ] || fail "no exemplars found under examples/*/go.mod; the acceptance suites are not running"

for e in "${EXEMPLARS[@]}"; do
  echo "== $e =="
  (cd "$e" && go vet ./...) || fail "$e: go vet"
  (cd "$e" && go test -race ./...) || fail "$e: go test"
done

echo "OK"
