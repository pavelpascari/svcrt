#!/usr/bin/env bash
# Mutation-testing gate. Runs go-mutesting over one or more modules and fails
# if any module's mutation score falls below its floor.
#
# Usage: scripts/mutation.sh [module]
#   With no argument, runs every module in the repo (derived from the go.mod
#   files on disk, so a module added later cannot be silently ungated).
#   With an argument, runs only that module (it must exist).
set -euo pipefail
cd "$(dirname "$0")/.."
REPO=$(pwd)

GO_MUTESTING="${GO_MUTESTING:-$HOME/go/bin/go-mutesting}"

fail() { echo "FAIL: $*" >&2; exit 1; }

# --- what to run -------------------------------------------------------------

# Derived, not hand-maintained: */go.mod finds the library modules and
# examples/*/go.mod the exemplars. Two hand-kept lists is how a module added
# at R1 ends up ungated with nobody noticing.
ALL_MODULES=()
for f in */go.mod examples/*/go.mod; do
  [ -f "$f" ] || continue
  ALL_MODULES+=("${f%/go.mod}")
done

# Modules whose dependencies resolve only through go.work.
#
# go-mutesting needs the module to build on its own, and go.work is not in
# scope where it runs. Rather than add absolute-path `replace` directives to
# the committed go.mod -- a real consumer would never carry those, and the
# exemplar has to stay honest about what consuming these modules looks like --
# the run happens in a synthesized copy outside the repo with the replaces
# injected. A side benefit: go-mutesting rewrites sources in place, so a
# module run this way cannot leave the working tree corrupted at all.
WORKSPACE_MODULES=(examples/orders)

# Files excluded from a module's mutation run, as "<module>=<file> <file>...".
MUTATION_EXCLUDE=(
  # main.go binds a real port, installs no seam, and is untestable by design
  # -- that is the point of the exemplar's "you write main()" claim. Its ~15
  # survivors measure that decision, not the test suite.
  "examples/orders=main.go"
)

# --- per-module floor --------------------------------------------------------

# The global floor. A module listed in MUTATION_FLOORS overrides it, and the
# reason must be recorded on the same entry: a floor without a reason is a
# number nobody can audit, and nobody will ever dare raise.
#
# Either can be overridden for one run from the environment:
#   MUTATION_MIN                 applies to every module
#   MUTATION_MIN_<MODULE>        applies to one, e.g. MUTATION_MIN_LOGGING,
#                                MUTATION_MIN_EXAMPLES_ORDERS
# The per-module variable wins over MUTATION_MIN.
MUTATION_FLOOR_DEFAULT=0.85
MUTATION_FLOORS=(
  # logging has 5 provably equivalent mutants out of 25 -- each documented in
  # docs/mutation-survivors.md with WHY ITS GUARD EXISTS, not just an
  # equivalence proof -- which is a hard ceiling of 0.80. Three of them guard
  # the per-request path, and mutation testing measures behaviour, not cost,
  # so it is structurally blind to what they buy. Deleting them would raise
  # this number by sacrificing the goal to its proxy. The binding rule is that
  # every survivor is killed or justified in that file; this is a tripwire.
  "logging=0.80"
)

# env_name turns a module path into the suffix of its MUTATION_MIN_ variable:
# examples/orders -> EXAMPLES_ORDERS.
env_name() { printf '%s' "$1" | tr 'a-z' 'A-Z' | tr -c 'A-Z0-9' '_'; }

floor_for() {
  local m=$1 var entry
  var="MUTATION_MIN_$(env_name "$m")"
  if [ -n "${!var:-}" ]; then printf '%s' "${!var}"; return; fi
  if [ -n "${MUTATION_MIN:-}" ]; then printf '%s' "$MUTATION_MIN"; return; fi
  for entry in "${MUTATION_FLOORS[@]}"; do
    case "$entry" in "$m="*) printf '%s' "${entry#*=}"; return ;; esac
  done
  printf '%s' "$MUTATION_FLOOR_DEFAULT"
}

excluded_for() {
  local m=$1 entry
  for entry in "${MUTATION_EXCLUDE[@]}"; do
    case "$entry" in "$m="*) printf '%s' "${entry#*=}"; return ;; esac
  done
}

is_workspace_module() {
  local m=$1 w
  for w in "${WORKSPACE_MODULES[@]}"; do
    [ "$w" = "$m" ] && return 0
  done
  return 1
}

# targets_for prints the go-mutesting targets for a module, resolved inside
# that module's directory. With nothing excluded that is just ./...; with an
# exclusion it has to be an explicit file list, because go-mutesting has no
# exclude flag.
targets_for() {
  local m=$1 excl f base skip e
  excl=$(excluded_for "$m")
  if [ -z "$excl" ]; then
    printf './...'
    return
  fi
  for f in "$m"/*.go; do
    base=$(basename "$f")
    case "$base" in *_test.go) continue ;; esac
    skip=false
    for e in $excl; do
      [ "$base" = "$e" ] && skip=true
    done
    $skip || printf '%s ' "$base"
  done
}

# synthesize_module copies a workspace-resolved module to a scratch directory
# outside the repo and rewrites its go.mod with absolute-path replaces, so it
# builds with GOWORK=off. Prints the directory.
synthesize_module() {
  local m=$1 dir dep
  dir=$(mktemp -d "${TMPDIR:-/tmp}/svcrt-mutation.XXXXXX")
  cp "$m"/*.go "$dir"/
  cp "$m/go.mod" "$dir/go.mod"
  for dep in "${ALL_MODULES[@]}"; do
    case "$dep" in examples/*) continue ;; esac
    printf '\nreplace github.com/pavelpascari/svcrt/%s => %s/%s\n' \
      "$dep" "$REPO" "$dep" >> "$dir/go.mod"
  done
  printf '%s' "$dir"
}

# --- run ---------------------------------------------------------------------

if [ "$#" -gt 0 ]; then
  MODULES=("$1")
  [ -d "$1" ] || fail "$1: no such module directory"
else
  MODULES=("${ALL_MODULES[@]}")
fi

for m in "${MODULES[@]}"; do
  echo "== $m =="

  targets=$(targets_for "$m")
  rundir="$m"
  scratch=""
  if is_workspace_module "$m"; then
    scratch=$(synthesize_module "$m")
    rundir="$scratch"
  fi

  set +e
  out=$(cd "$rundir" && GOWORK=off "$GO_MUTESTING" $targets 2>&1)
  rc=$?
  set -e
  [ -n "$scratch" ] && rm -rf "$scratch"
  [ "$rc" -eq 0 ] || fail "$m: go-mutesting failed to run:
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

  min=$(floor_for "$m")
  excl=$(excluded_for "$m")
  echo "$m: mutation score $score, $total mutants total (threshold $min)${excl:+, excluding $excl}"

  below=$(awk -v s="$score" -v t="$min" 'BEGIN { print (s < t) ? "1" : "0" }')
  [ "$below" = "0" ] || fail "$m: mutation score $score is below threshold $min"
done

echo "OK"
