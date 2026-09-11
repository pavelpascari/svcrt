#!/usr/bin/env bash
# CI assertions that carry the svcrt architecture. See spec §8.
set -euo pipefail
cd "$(dirname "$0")/.."

# Modules that must each stand alone with zero external dependencies.
# Add each new library module here as it is created.
MODULES=(contract config)

fail() { echo "FAIL: $*" >&2; exit 1; }

for m in "${MODULES[@]}"; do
  echo "== $m =="

  # Spec P2: useful with no other svcrt module present. GOWORK=off is the
  # point -- go.work masks version skew locally, so CI must run without it.
  (cd "$m" && GOWORK=off go vet ./...) || fail "$m: go vet"
  (cd "$m" && GOWORK=off go test -race ./...) || fail "$m: go test"

  # Spec §8.2: zero requires. Exactly one line -- the module itself.
  n=$(cd "$m" && GOWORK=off go list -m all | wc -l | tr -d ' ')
  [ "$n" -eq 1 ] || fail "$m: has module dependencies ($n lines from 'go list -m all')"
done

# Spec §4.1: contract imports "context" and nothing else.
imports=$(cd contract && GOWORK=off go list -f '{{join .Imports "\n"}}' ./... | sort -u)
for i in $imports; do
  [ "$i" = "context" ] || fail "contract imports $i; only \"context\" is permitted"
done

echo "OK"
