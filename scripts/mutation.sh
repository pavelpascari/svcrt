#!/usr/bin/env bash
# Mutation-testing gate. Runs go-mutesting over one or more library modules
# and fails if any module's mutation score falls below MUTATION_MIN.
#
# Usage: scripts/mutation.sh [module]
#   With no argument, runs every library module that exists on disk yet
#   (later tasks add more; missing directories are skipped silently).
#   With an argument, runs only that module (it must exist).
set -euo pipefail
cd "$(dirname "$0")/.."

GO_MUTESTING="${GO_MUTESTING:-$HOME/go/bin/go-mutesting}"
THRESHOLD="${MUTATION_MIN:-0.85}"

# Library modules in dependency order. Add each new one here as it is
# created; modules not yet present on disk are skipped silently.
ALL_MODULES=(contract config logging)

fail() { echo "FAIL: $*" >&2; exit 1; }

if [ "$#" -gt 0 ]; then
  MODULES=("$1")
  [ -d "$1" ] || fail "$1: no such module directory"
else
  MODULES=()
  for m in "${ALL_MODULES[@]}"; do
    [ -d "$m" ] && MODULES+=("$m")
  done
fi

for m in "${MODULES[@]}"; do
  echo "== $m =="

  out=$(cd "$m" && "$GO_MUTESTING" ./... 2>&1) || fail "$m: go-mutesting failed to run:
$out"

  line=$(echo "$out" | grep "The mutation score is" || true)
  [ -n "$line" ] || fail "$m: could not find a mutation score in go-mutesting output:
$out"

  # Example line:
  # The mutation score is 0.250000 (1 passed, 3 failed, 0 duplicated, 0 skipped, total is 4)
  # NB: "passed" means the mutant was KILLED (good); "failed" means it
  # SURVIVED (tests missed it, bad). Score is killed/total.
  score=$(echo "$line" | sed -E 's/.*mutation score is ([0-9.]+).*/\1/')
  total=$(echo "$line" | sed -E 's/.*total is ([0-9]+).*/\1/')

  if [ "$total" -eq 0 ]; then
    echo "$m: no mutants found (nothing executable to mutate) -- skipping threshold check"
    continue
  fi

  echo "$m: mutation score $score (threshold $THRESHOLD)"

  below=$(awk -v s="$score" -v t="$THRESHOLD" 'BEGIN { print (s < t) ? "1" : "0" }')
  [ "$below" = "0" ] || fail "$m: mutation score $score is below threshold $THRESHOLD"
done

echo "OK"
