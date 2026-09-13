#!/usr/bin/env bash
# Mutation-testing gate. Runs go-mutesting over one or more modules and fails
# if any module's mutation score falls below its floor.
#
# Usage: scripts/mutation.sh [module]
#   With no argument, runs every module in the repo (derived from the go.mod
#   files on disk, so a module added later cannot be silently ungated).
#   With an argument, runs only that module (it must exist).
#
# Runs at HEAD inside a throwaway `git worktree`, never in the repo -- see the
# isolation section below for why. Two consequences worth knowing before you
# read a score:
#   - uncommitted changes are NOT mutated. The script warns when the tree is
#     dirty; commit to include them.
#   - the repo working tree cannot be corrupted by an interrupted run, so this
#     is safe to kill, and safe to run while something else is using the repo.
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

# Files excluded from a module's mutation run, as "<module>=<file> <file>...".
#
# Unlike the lists above, this one is deliberately hand-kept: it is a policy
# statement about which files are untestable by design, not an inventory of
# what exists on disk, and a new exemplar's main.go should get its own
# considered entry rather than being swept in implicitly. A new exemplar
# under examples/ needs one added here.
#
# The scope of an entry is deliberately narrow: main() itself -- config load,
# logger, SignalContext, Run, os.Exit -- is untestable, because go test never
# calls main. The WIRING main() calls is not, and must not be excluded. That
# distinction was lost once already: buildStack was extracted from main.go
# specifically so the mutation gate would cover it, and then left in main.go,
# the one file the gate refuses to look at -- so the function whose whole
# purpose was to be gated was the only one outside the gate. A real survivor
# (a dropped `OnServeError: lc.Fatal`, which is the entire mechanism turning
# a dead listener into an ordered shutdown) sat there undetected until a
# review found it by hand. The wiring now lives in each exemplar's stack.go
# and is mutated like everything else. If you find yourself adding a file
# here, or moving code into main.go to quiet a survivor, that is the bug.
MUTATION_EXCLUDE=(
  # main.go holds func main() and nothing else: it loads config, builds a
  # logger, installs a real OS signal handler, calls Run, and exits. No test
  # binary can reach any of it. Its survivors measure that fact, not the test
  # suite. Everything these mains wire up lives in stack.go and is mutated.
  "examples/orders=main.go"
  "examples/worker=main.go"
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
  # logging has 5 provably equivalent mutants out of 31 -- each documented in
  # docs/mutation-survivors.md with WHY ITS GUARD EXISTS, not just an
  # equivalence proof -- which is a hard ceiling of 0.8387 (26/31). Three of
  # them guard the per-request path, and mutation testing measures behaviour,
  # not cost, so it is structurally blind to what they buy. Deleting them
  # would raise this number by sacrificing the goal to its proxy. The binding
  # rule is that every survivor is killed or justified in that file; this is
  # a tripwire.
  "logging=0.80"
)

# Both lists above are keyed by module path and hand-kept, so both can go
# stale the moment a module is renamed or split -- and they fail in opposite,
# equally quiet directions. A stale MUTATION_EXCLUDE key fails OPEN: nothing
# matches, so nothing is excluded and the entry is simply dead text that
# still reads like policy. A stale MUTATION_FLOORS key fails the other way:
# the module silently reverts to MUTATION_FLOOR_DEFAULT, so a floor lowered
# for a documented reason is quietly raised, or one raised on purpose is
# quietly lowered. ci.sh checks COUNT_MODULES against disk for exactly this
# reason; do the same here rather than letting a rename rot a gate.
check_keys() {
  local label=$1 entry key m found
  shift
  for entry in "$@"; do
    key=${entry%%=*}
    found=false
    for m in "${ALL_MODULES[@]}"; do
      if [ "$m" = "$key" ]; then found=true; fi
    done
    $found || fail "$label names '$key', which is not a module on disk. That entry is doing nothing. Fix the name or drop it."
  done
}
# The same argument one level down, for MUTATION_EXCLUDE only: its values are
# filenames, and a filename that matches nothing excludes nothing while still
# reading like a considered policy decision. Rename or delete an exemplar's
# main.go and the entry becomes a comment with a syntax.
# Validated against the WORKTREE, not the repo, for the same reason targets are
# globbed there: the worktree at HEAD is what actually gets mutated, so it is
# what an exclusion has to match. A main.go that exists only as an uncommitted
# file in the repo would otherwise validate an entry that excludes nothing.
check_excluded_files() {
  local root=$1 entry m f
  shift
  for entry in "$@"; do
    m=${entry%%=*}
    for f in ${entry#*=}; do
      [ -f "$root/$m/$f" ] || fail "MUTATION_EXCLUDE says '$m=$f', but $m/$f does not exist at HEAD. That exclusion is doing nothing. Fix the name or drop it."
    done
  done
}

if [ ${#MUTATION_EXCLUDE[@]} -gt 0 ]; then
  check_keys MUTATION_EXCLUDE "${MUTATION_EXCLUDE[@]}"
fi
[ ${#MUTATION_FLOORS[@]} -eq 0 ] || check_keys MUTATION_FLOORS "${MUTATION_FLOORS[@]}"

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

# targets_for prints the go-mutesting targets for a module, resolved inside
# that module's directory in the WORKTREE (see below) rather than in the repo.
# With nothing excluded that is just ./...; with an exclusion it has to be an
# explicit file list, because go-mutesting has no exclude flag.
#
# Globbing the worktree and not the repo matters: the worktree is checked out
# at HEAD, so an untracked .go file sitting in the repo must not become a
# target that does not exist where the run happens.
targets_for() {
  local m=$1 root=$2 excl f base skip e
  excl=$(excluded_for "$m")
  if [ -z "$excl" ]; then
    printf './...'
    return
  fi
  for f in "$root/$m"/*.go; do
    base=$(basename "$f")
    case "$base" in *_test.go) continue ;; esac
    skip=false
    for e in $excl; do
      [ "$base" = "$e" ] && skip=true
    done
    $skip || printf '%s ' "$base"
  done
}

# --- isolation ---------------------------------------------------------------

# Every module is mutated inside a throwaway `git worktree` at HEAD, never in
# the repo.
#
# go-mutesting rewrites source files IN PLACE and restores them only on a clean
# exit, which makes it unsafe to interrupt. Run against the repo, an interrupted
# run leaves tracked files mutated on disk. That is not hypothetical: it has
# happened five times in this repo's short life -- once leaving config/plan.go
# and config/source.go mutated with the suite passing against the corrupted
# source, one `git add` away from being committed, and once mutating
# lifecycle/lifecycle.go while a reviewer was reading that same file and could
# have reported the injected fault as a real concurrency defect.
#
# Each of those was met with a better detector (a *.tmp sweep, a `git diff`
# check, a dirty-tree guard) and each detector worked, and the incidents kept
# happening -- because detection is not prevention and all of it depended on
# nobody running two things at once. A worktree removes the failure mode
# instead of catching it: the mutated files are in a directory git will discard,
# the repo is untouchable by construction, concurrent runs stop conflicting,
# and a killed run is recovered with `git worktree prune` rather than by
# restoring source from backups.
#
# The worktree also replaces the synthesized-module machinery this script used
# to carry for the exemplars. go.work is tracked, so it comes along with the
# checkout and resolves the unpublished sibling modules natively -- which is
# why the run below does NOT set GOWORK=off. That flag was the whole reason the
# `replace` directives had to be injected: with the workspace out of scope, an
# exemplar cannot find its siblings. Proving each module builds standalone is
# ci.sh's job and it still does it; mutation testing only needs the module to
# build, so it uses the workspace and keeps the committed go.mod honest.
# module_fingerprint prints a content hash of the repo's copy of a module, used
# to prove a run did not touch it. It covers tracked content (via the diff
# against HEAD, which changes if a tracked file is rewritten) and the names and
# contents of every untracked file in the module -- not just .go, which is
# broader than strictly needed and deliberately so: the cost of over-including
# is a false alarm nobody has ever seen, and the cost of under-including is a
# silent miss.
module_fingerprint() {
  local m=$1
  {
    git -C "$REPO" diff HEAD -- "$m"
    git -C "$REPO" ls-files --others --exclude-standard -- "$m" | sort |
      while IFS= read -r f; do
        printf '%s ' "$f"
        cat "$REPO/$f" 2>/dev/null || true
      done
  } | shasum | cut -d' ' -f1
}

WORKTREE=""
cleanup_worktree() {
  [ -n "$WORKTREE" ] || return 0
  git -C "$REPO" worktree remove --force "$WORKTREE" >/dev/null 2>&1 || rm -rf "$WORKTREE"
  git -C "$REPO" worktree prune >/dev/null 2>&1 || true
  WORKTREE=""
}
# EXIT covers the ordinary ends, including `set -e` aborting through a failed
# step and `fail` exiting. INT and TERM are trapped because this script gets
# killed by timeouts and by Ctrl-C.
#
# One honest caveat: bash defers a trap until the running foreground command
# returns, so a TERM aimed at this script's pid alone does not clean up until
# the in-flight go-mutesting finishes -- which for a large module is minutes. A
# signal to the process GROUP (what a terminal Ctrl-C and most harness timeouts
# actually send) reaches go-mutesting too, and cleanup is then prompt. The repo
# is safe either way, since nothing in it is ever mutated; the worst case is an
# orphaned worktree, which `git worktree prune` clears.
trap cleanup_worktree EXIT INT TERM

make_worktree() {
  local dir
  dir=$(mktemp -d "${TMPDIR:-/tmp}/svcrt-mutation.XXXXXX")
  rm -rf "$dir"
  git -C "$REPO" worktree add --detach "$dir" HEAD >/dev/null 2>&1 ||
    fail "could not create a git worktree at $dir"
  printf '%s' "$dir"
}

# --- run ---------------------------------------------------------------------

if [ "$#" -gt 0 ]; then
  MODULES=("$1")
  [ -d "$1" ] || fail "$1: no such module directory"
else
  MODULES=("${ALL_MODULES[@]}")
fi

# The run happens at HEAD, so uncommitted work is NOT what gets mutated. Say so
# rather than let someone read a score as covering an edit it never saw. This is
# a warning and not a failure: "score my committed state" is a legitimate thing
# to ask for with a dirty tree, and the old in-place behaviour made it
# impossible.
#
# Untracked files count. A brand-new .go file is the likeliest thing to be
# missing from a run and the easiest to overlook, since `git diff` says nothing
# about it -- so list those too rather than warn about half the problem.
DIRTY=$(git -C "$REPO" status --porcelain -- . | sed 's/^...//')
if [ -n "$DIRTY" ]; then
  echo "WARNING: the working tree has uncommitted changes. Mutation runs against"
  echo "         HEAD ($(git -C "$REPO" rev-parse --short HEAD)) in an isolated worktree, so those"
  echo "         changes are NOT being mutated. Commit them to include them:"
  printf '%s\n' "$DIRTY" | sed 's/^/           /'
  echo
fi

WORKTREE=$(make_worktree)

if [ ${#MUTATION_EXCLUDE[@]} -gt 0 ]; then
  check_excluded_files "$WORKTREE" "${MUTATION_EXCLUDE[@]}"
fi

for m in "${MODULES[@]}"; do
  echo "== $m =="

  targets=$(targets_for "$m" "$WORKTREE")
  before=$(module_fingerprint "$m")

  set +e
  out=$(cd "$WORKTREE/$m" && "$GO_MUTESTING" $targets 2>&1)
  rc=$?
  set -e
  [ "$rc" -eq 0 ] || fail "$m: go-mutesting failed to run:
$out"

  # Isolation regression check: the repo copy of $m must be unchanged BY THIS RUN.
  #
  # Compare a before/after fingerprint, not the tree against HEAD. Comparing to
  # HEAD conflates "this run corrupted the repo" with "the tree was already
  # dirty when you started", which is a state this script explicitly supports --
  # it warns about it a few lines up and carries on. Getting that wrong is not a
  # cosmetic mislabel: the failure told the user to run `git checkout -- $m`,
  # which would have destroyed the uncommitted work that triggered it.
  #
  # Be clear about what this does NOT prove. go-mutesting restores every file
  # byte-identically when it completes cleanly, so an isolation regression that
  # ran to completion leaves no trace here -- this check firing means isolation
  # broke, but staying silent does not mean it held. The case it actually adds
  # cover for is narrow, since an interrupted run is already caught by the rc
  # check above. The real guarantee is structural: the run happens in $WORKTREE,
  # so there is nothing in the repo for it to damage. This is a tripwire behind
  # that guarantee, not the guarantee itself -- if you are relying on it to tell
  # you isolation is intact, read the run directory instead.
  if [ "$(module_fingerprint "$m")" != "$before" ]; then
    fail "$m: the repo copy of $m changed during a run that should have been confined to $WORKTREE. Something has reintroduced an in-repo mutation run; fix that rather than deleting this check. Your working tree has NOT been restored -- inspect it with: git diff -- $m"
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
