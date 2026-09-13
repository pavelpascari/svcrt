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
COUNT_MODULES=(lifecycle httpserver resilience)

# A module that looks concurrent by the heuristic below but is deliberately
# NOT in COUNT_MODULES, as "<module>=<why>". Empty is the healthy state.
#
# This is the escape hatch for the inverse assertion further down, and it is
# deliberately shaped as a sentence rather than a bare name: the assertion
# exists because "someone forgot" is indistinguishable from "someone decided"
# once the list is just names. An entry here is a decision on the record.
COUNT_EXEMPT=()

# Both directions of the COUNT_MODULES list are asserted against disk,
# because they fail in opposite and equally quiet ways.
#
# FORWARD -- a name matching nothing. A rename, a typo, or a module split
# leaves an entry matching no module, count_for quietly answers 1, and the
# only gate that catches concurrency bugs stops applying while CI still prints
# OK and conventions.md still says it applies.
#
# INVERSE -- a module missing from the list. This is the half the forward
# check cannot see, and it is the one that actually happened: `resilience`
# shipped a timer and a retry loop on the request path through a whole
# milestone at -count=1, past a guard written for exactly this failure, and
# past a spec that named the module in bold. A list that is only checked in
# the direction someone remembered to check is not a gate.
#
# The inverse check is a heuristic and says so: it greps non-test source for
# the constructs the gate exists for (timers, goroutine launches, sync,
# atomic) and demands either membership in COUNT_MODULES or an entry in
# COUNT_EXEMPT saying why not. It will occasionally fire on a comment
# mentioning sync.Once, and that is the intended trade: a false alarm costs
# one line of justification, a miss costs a milestone.
for c in "${COUNT_MODULES[@]}"; do
  found=false
  for m in "${MODULES[@]}"; do
    if [ "$m" = "$c" ]; then found=true; fi
  done
  $found || svcrt_fail "COUNT_MODULES names '$c', which is not a module on disk. The -count=10 concurrency gate would silently not apply (conventions.md §5). Fix the name or drop the entry."
done

for e in ${COUNT_EXEMPT[@]+"${COUNT_EXEMPT[@]}"}; do
  key=${e%%=*}
  found=false
  for m in "${MODULES[@]}"; do
    if [ "$m" = "$key" ]; then found=true; fi
  done
  $found || svcrt_fail "COUNT_EXEMPT names '$key', which is not a module on disk. That exemption is doing nothing. Fix the name or drop it."
  [ "$e" != "$key" ] || svcrt_fail "COUNT_EXEMPT entry '$e' carries no reason. Write it as '$key=<why this module needs no -count=10>'."
done

# The grep is deliberately greppable itself: one regex, listed here, so a
# reader can run it by hand against a module and get the same answer CI does.
COUNT_CONCURRENCY_RE='time\.NewTimer|time\.After|^[[:space:]]*go [a-zA-Z_(]|sync\.|atomic\.'

for m in "${MODULES[@]}"; do
  gated=false
  for c in "${COUNT_MODULES[@]}"; do
    if [ "$m" = "$c" ]; then gated=true; fi
  done
  for e in ${COUNT_EXEMPT[@]+"${COUNT_EXEMPT[@]}"}; do
    case "$e" in "$m="*) gated=true ;; esac
  done
  if $gated; then continue; fi

  hit=""
  for f in "$m"/*.go; do
    [ -f "$f" ] || continue
    case "$f" in *_test.go) continue ;; esac
    hit=$(grep -nE "$COUNT_CONCURRENCY_RE" "$f" | sed "s|^|$f:|" | head -3 || true)
    if [ -n "$hit" ]; then break; fi
  done
  [ -z "$hit" ] || svcrt_fail "$m is not in COUNT_MODULES, but its non-test source uses the concurrency the -count=10 gate exists for (conventions.md §5):
$hit
Add '$m' to COUNT_MODULES, or -- if this module genuinely needs no repeat runs -- add an entry to COUNT_EXEMPT saying why."
done

count_for() {
  local c
  for c in "${COUNT_MODULES[@]}"; do
    if [ "$c" = "$1" ]; then printf '10'; return; fi
  done
  printf '1'
}
