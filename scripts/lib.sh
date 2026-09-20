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

# --- sibling imports ---------------------------------------------------------
#
# No svcrt library module imports another. That has always been true and,
# until R6, nothing checked it -- it held by habit, exactly the way the
# zero-requires rule did before R5 wrote it down. `kit` is the first
# deliberate violation (conventions.md §11), which makes this the moment to
# gate it rather than the moment to stop caring: a core module that starts
# requiring a sibling must fail here, by name.
#
# SIBLING_ALLOWED is shaped "<module>=<why>" for the same reason DEP_EXEMPT
# and COUNT_EXEMPT are: once a list is just names, "someone forgot" and
# "someone decided" become indistinguishable.
SIBLING_ALLOWED=(
  "kit=composes httpclient, resilience, telemetry and logging into the orderings R5 proved fail silently when inverted; a composition module cannot compose without importing what it composes. It is opt-in and nothing imports it, so the coupling is paid only by callers who asked for it"
  "testkit=ships test helpers for CONSUMERS of svcrt, and cannot assert on a contract error or capture a logging record without importing contract and logging. It is opt-in, nothing in svcrt imports it, and no core module may -- a test-only require is still a require, so a core module adopting testkit would fail the zero-requires gate above (conventions.md §11)"
)

# The greps are deliberately greppable themselves -- one regex, listed here,
# so a reader can run it by hand against a go.mod and get the same answer CI
# does. The `module` line cannot match (it carries no version) and `=>` lines
# are dropped so a replace directive is never read as a require.
SIBLING_REQUIRE_RE='^[[:space:]]*(require[[:space:]]+)?github\.com/pavelpascari/svcrt/[A-Za-z0-9_-]+[[:space:]]+v'
SIBLING_UNPUBLISHED_RE="$SIBLING_REQUIRE_RE"'0\.0\.0([[:space:]]|$)'

# sibling_requires prints module $1's require lines that name a sibling;
# sibling_unpublished narrows that to the ones at v0.0.0. Both print nothing
# and succeed when there are none.
sibling_requires()    { grep -E "$SIBLING_REQUIRE_RE" "$1/go.mod" | grep -v '=>' || true; }
sibling_unpublished() { grep -E "$SIBLING_UNPUBLISHED_RE" "$1/go.mod" | grep -v '=>' || true; }

# INVERSE direction: a module requiring a sibling without permission. This is
# the half that carries the invariant, and before R6 nothing checked it at
# all -- which is how an invariant that is merely true differs from one that
# is enforced.
for m in "${MODULES[@]}"; do
  reqs=$(sibling_requires "$m")
  [ -n "$reqs" ] || continue
  allowed=false
  for e in ${SIBLING_ALLOWED[@]+"${SIBLING_ALLOWED[@]}"}; do
    case "$e" in "$m="*) allowed=true ;; esac
  done
  $allowed || svcrt_fail "$m requires a sibling svcrt module:
$reqs
No core module imports another (conventions.md §11). Only kit and testkit may,
and each earned it: composition is kit's entire job, and testkit cannot assert
on a contract error or capture a logging record without those two. If $m
genuinely must, that is a design change and needs the argument written down in
conventions.md §11 plus an entry in SIBLING_ALLOWED in scripts/lib.sh."
done

# FORWARD direction, plus staleness, in one pass. An entry naming a module
# that does not exist -- or one that no longer requires any sibling -- permits
# nothing while still reading as a considered decision, which is the same lie
# a stale DEP_EXEMPT entry tells.
for e in ${SIBLING_ALLOWED[@]+"${SIBLING_ALLOWED[@]}"}; do
  key=${e%%=*}
  found=false
  for m in "${MODULES[@]}"; do
    if [ "$m" = "$key" ]; then found=true; fi
  done
  $found || svcrt_fail "SIBLING_ALLOWED names '$key', which is not a module on disk. That permission is doing nothing. Fix the name or drop it."
  [ "$e" != "$key" ] || svcrt_fail "SIBLING_ALLOWED entry '$e' carries no reason. Write it as '$key=<why this module may import siblings>'."
  [ -n "$(sibling_requires "$key")" ] || svcrt_fail "SIBLING_ALLOWED permits '$key' to import siblings, but $key/go.mod requires none. Drop the now-false permission from SIBLING_ALLOWED in scripts/lib.sh."
done

# WORKSPACE_MODULES / STANDALONE_MODULES: which bucket a module is tested in.
#
# ci.sh runs the libraries under GOWORK=off on purpose -- go.work masks
# version skew locally, so CI must run without it -- but that gate encodes an
# invariant a module requiring a sibling at v0.0.0 cannot satisfy: v0.0.0
# resolves to nothing, because no module in this repo is tagged yet. `kit` is
# not useful without other svcrt modules present; that is its whole purpose,
# and a gate asserting the opposite is asserting the wrong thing about it.
# The exemplars already have exactly this carve-out, for exactly this reason.
#
# Derived from disk rather than listed, for the same reason MODULES and
# EXEMPLARS are: a module added later must not be silently ungated. It is also
# self-healing -- once the siblings are tagged and these requires name a real
# version, the module resolves standalone, falls back into the STANDALONE
# bucket on its own, and there is no stale exemption for anyone to remember to
# remove.
WORKSPACE_MODULES=()
STANDALONE_MODULES=()
for m in "${MODULES[@]}"; do
  if [ -n "$(sibling_unpublished "$m")" ]; then
    WORKSPACE_MODULES+=("$m")
  else
    STANDALONE_MODULES+=("$m")
  fi
done

# A module must land in exactly one bucket, and the buckets together must
# account for every module. A partition that silently loses a module is the
# failure this whole split exists to avoid: the module would be tested by
# neither loop while ci.sh still printed OK.
[ $(( ${#STANDALONE_MODULES[@]} + ${#WORKSPACE_MODULES[@]} )) -eq ${#MODULES[@]} ] ||
  svcrt_fail "the standalone/workspace split lost a module: ${#STANDALONE_MODULES[@]} + ${#WORKSPACE_MODULES[@]} != ${#MODULES[@]}. Some module would be tested by neither loop."
[ ${#STANDALONE_MODULES[@]} -gt 0 ] || svcrt_fail "no module builds standalone; the GOWORK=off gate is testing nothing"

# A module that legitimately keeps real dependencies, as "<module>=<why>".
# Zero requires is the default (spec §8.2) because a library that costs
# nothing extra to pull in is the whole point of shipping it separately -- an
# entry here is a documented, deliberate exception to that default, not an
# oversight. Shaped as a sentence rather than a bare name for the same reason
# as COUNT_EXEMPT below: once a list is just names, "someone forgot" and
# "someone decided" are indistinguishable.
DEP_EXEMPT=(
  "telemetry=depends on the OpenTelemetry API -- otel, otel/trace, otel/metric; a tracing library that refused to depend on a tracing API would reimplement W3C tracecontext, which is not independence but a second, worse implementation of a standard"
)

# Both directions of DEP_EXEMPT are asserted against disk, for the same
# reason COUNT_MODULES/COUNT_EXEMPT are asserted in both directions below.
#
# FORWARD (here) -- every entry must name a module that exists on disk. A
# rename or a typo leaves an entry exempting nothing while ci.sh's zero-deps
# loop believes it is skipped, and the module would fail loudly there instead
# of here where the mistake actually is.
#
# INVERSE -- an exempted module whose dependencies have since gone to zero.
# That needs `go list -m all`, which is the expensive-enough-to-not-duplicate
# call ci.sh's own loop already makes per module, so the inverse half lives
# there (via dep_exempt_reason below) rather than here.
for e in ${DEP_EXEMPT[@]+"${DEP_EXEMPT[@]}"}; do
  key=${e%%=*}
  found=false
  for m in "${MODULES[@]}"; do
    if [ "$m" = "$key" ]; then found=true; fi
  done
  $found || svcrt_fail "DEP_EXEMPT names '$key', which is not a module on disk. That exemption is doing nothing. Fix the name or drop it."
  [ "$e" != "$key" ] || svcrt_fail "DEP_EXEMPT entry '$e' carries no reason. Write it as '$key=<why this module needs real dependencies>'."
done

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
#
# telemetry joined at R5 by judgement, NOT by the heuristic below: its
# non-test source matches none of COUNT_CONCURRENCY_RE (no timer, no `go`,
# no sync, no atomic -- `time.Now`/`time.Since` are deliberately not in that
# regex). The heuristic would have stayed silent while Server and Client sat
# on the request path, their middleware closures capturing one histogram and
# one tracer shared by every concurrent request, and the recording stubs
# shared across parallel subtests. That is the R3 failure shape exactly, and
# it is the reason this line is written by hand rather than derived.
#
# kit joined at R6 by the same judgement and against the same silence: its
# non-test source is three constructors and a slice literal, matches none of
# COUNT_CONCURRENCY_RE, and holds no shared mutable state of its own -- so the
# heuristic would have said nothing and kit would have been silently absent,
# which is the R3 failure exactly. It is here anyway because what kit produces
# is one *http.Client that a service shares across every goroutine it has, and
# kit is the ONLY place the composed chain runs as a whole: resilience and
# telemetry each get ten runs in their own module, unwired, and a race that
# exists only between them -- Retry replaying a request the span middleware
# still holds -- is visible nowhere else. The composition is the artifact, so
# the composition gets the gate. It costs about two seconds.
#
# testkit joined at R8, and is the first module the inverse heuristic below
# would have caught on its own: Records and Server both hold mutex-guarded
# state, so `sync.` matches its non-test source and CI demands either this
# membership or a COUNT_EXEMPT entry. It belongs here on the merits anyway.
# Records is written by whichever goroutine logged and read by the test
# goroutine; Server.n is written by handler goroutines and read by the test.
# A test helper that races is worse than a racing library, because the flake
# it produces is attributed to the code under test.
COUNT_MODULES=(lifecycle httpserver resilience telemetry kit testkit)

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

# dep_exempt_reason prints the DEP_EXEMPT reason for module $1 and succeeds,
# or fails (prints nothing) if $1 is not exempt. This is the inverse half of
# the DEP_EXEMPT forward check above: ci.sh's zero-deps loop calls this per
# module, and when it succeeds must additionally confirm 'go list -m all'
# still returns more than one line -- an exempted module that has quietly
# gone dependency-free is a stale exemption, which is a lie in the gate.
dep_exempt_reason() {
  local e
  for e in ${DEP_EXEMPT[@]+"${DEP_EXEMPT[@]}"}; do
    case "$e" in "$1="*) printf '%s' "${e#*=}"; return 0 ;; esac
  done
  return 1
}
