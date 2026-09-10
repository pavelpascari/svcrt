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

  # go-mutesting rewrites a module's source files in place while it mutates
  # them, restoring the originals when it finishes cleanly -- but it is not
  # safely interruptible. A run killed mid-mutation (Ctrl-C, a timeout, a
  # crash) can leave tracked files mutated on disk, plus stray *.tmp files
  # behind. This has happened for real: an interrupted run once left
  # config/plan.go and config/source.go mutated with tests still passing
  # against the corrupted source, one git-add away from being committed.
  # So: sweep any leftover *.tmp files unconditionally, then verify the
  # module's tracked files still match HEAD before trusting anything the
  # run reported. Do not remove this thinking it's defensive paranoia --
  # it exists because of a real incident, not a hypothetical one.
  find "$m" -name '*.tmp' -delete
  if ! git diff --exit-code HEAD -- "$m" > /dev/null; then
    fail "$m: go-mutesting corrupted the working tree (it rewrites source in place and is not safely interruptible). Restore with: git checkout -- $m"
  fi

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

  # Per-module floor. A small module's equivalent-mutant ceiling can sit below the
  # global default: logging has 5 provably equivalent mutants out of 25 (documented
  # in docs/mutation-survivors.md), a hard ceiling of 0.80. The binding rule is that
  # every survivor is killed or justified there -- this number is only a tripwire.
  case "$m" in
    logging) min="${MUTATION_MIN:-0.80}" ;;
    *)       min="${MUTATION_MIN:-0.85}" ;;
  esac

  echo "$m: mutation score $score, $total mutants total (threshold $min)"

  below=$(awk -v s="$score" -v t="$min" 'BEGIN { print (s < t) ? "1" : "0" }')
  [ "$below" = "0" ] || fail "$m: mutation score $score is below threshold $min"
done

echo "OK"
