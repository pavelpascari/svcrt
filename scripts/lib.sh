# Shared derivations for the svcrt scripts. Sourced from the repo root by
# ci.sh and release.sh; it is not executable on its own.
#
# Everything here was duplicated per script before, which is how a module
# added at R1 ends up ungated -- or, in release.sh's case, untaggable -- with
# nobody noticing. The -count policy joined it for the same reason: ci.sh
# applied it and release.sh, the last gate before a version becomes
# permanent, did not.
#
# Written for bash 3.2 (macOS's default) under `set -u`: "${EMPTY[@]}" is an
# error there, so every array is built first and its length checked before
# anything iterates it.

svcrt_fail() { echo "FAIL: $*" >&2; exit 1; }

# Library modules that must each stand alone with zero external dependencies.
#
# Derived from the go.mod files on disk rather than hand-listed. Exemplars
# live under examples/ and are excluded: they depend on the libraries, are
# never released, and are handled separately by their own callers.
MODULES=()
for f in */go.mod; do
  [ -f "$f" ] || continue
  m=${f%/go.mod}
  if [ "$m" != "examples" ]; then MODULES+=("$m"); fi
done
[ ${#MODULES[@]} -gt 0 ] || svcrt_fail "no library modules found (expected */go.mod)"

# The exemplars, derived the same way and for the same reason: a second
# exemplar added later must not be silently ungated.
EXEMPLARS=()
for f in examples/*/go.mod; do
  [ -f "$f" ] || continue
  EXEMPLARS+=("${f%/go.mod}")
done

# Modules whose bugs are likelier to be "hangs one run in fifty" than "returns
# the wrong value": goroutines, timers, shared state. A single green -race run
# says very little about those -- coverage and mutation testing are both blind
# to concurrency, so this is the one gate that catches it (conventions.md §5).
# Everything else runs at -count=1; these run at -count=10.
COUNT_MODULES=(lifecycle httpserver)

# COUNT_MODULES is a policy statement, so unlike MODULES it has to be
# hand-kept -- and a hand-kept list of module names is exactly the failure
# mode MODULES exists to avoid. A rename, a typo, or a module split leaves an
# entry matching nothing, count_for quietly answers 1, and the only gate that
# catches concurrency bugs stops applying while CI still prints OK and
# conventions.md still says it applies. A gate that can silently stop applying
# is worse than no gate. So check the list against what is on disk.
for c in "${COUNT_MODULES[@]}"; do
  found=false
  for m in "${MODULES[@]}"; do
    if [ "$m" = "$c" ]; then found=true; fi
  done
  $found || svcrt_fail "COUNT_MODULES names '$c', which is not a module on disk. The -count=10 concurrency gate would silently not apply (conventions.md §5). Fix the name or drop the entry."
done

count_for() {
  local c
  for c in "${COUNT_MODULES[@]}"; do
    if [ "$c" = "$1" ]; then printf '10'; return; fi
  done
  printf '1'
}
