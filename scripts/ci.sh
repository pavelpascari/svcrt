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
# for non-emptiness, not the exit status. Assigned inside `if !`, same as the
# `go list` check below: a bare `unformatted=$(...)` under set -e would abort
# silently if gofmt itself failed to run (missing binary, unparseable file),
# and the gate would report nothing rather than reporting that it never ran.
if ! unformatted=$(gofmt -l .); then
  fail "gofmt -l failed to run"
fi
[ -z "$unformatted" ] || fail "gofmt -l found unformatted files:
$unformatted"

for m in "${STANDALONE_MODULES[@]}"; do
  echo "== $m =="

  # Spec P2: useful with no other svcrt module present. GOWORK=off is the
  # point -- go.work masks version skew locally, so CI must run without it.
  (cd "$m" && GOWORK=off go vet ./...) || fail "$m: go vet"
  (cd "$m" && GOWORK=off go test -race -count=$(count_for "$m") ./...) || fail "$m: go test"

  # Spec §8.2: zero requires. Exactly one line -- the module itself -- unless
  # the module is named in DEP_EXEMPT (lib.sh), in which case the assertion
  # flips: it must have MORE than one line, because an exemption for a module
  # that has quietly gone dependency-free is a stale exemption, and a stale
  # exemption is a lie in the gate.
  # Assigned inside `if !` so a go list failure reports through fail() with
  # its label; a bare `n=$(...)` under set -e aborts before we get here and
  # the operator is left guessing which module and which check broke.
  if ! n=$(cd "$m" && GOWORK=off go list -m all | wc -l | tr -d ' '); then
    fail "$m: go list -m all"
  fi
  if reason=$(dep_exempt_reason "$m"); then
    [ "$n" -gt 1 ] || fail "$m: DEP_EXEMPT says '$reason', but 'go list -m all' now returns $n line(s) -- $m is dependency-free again. Remove the now-false exemption from DEP_EXEMPT in scripts/lib.sh."
  else
    [ "$n" -eq 1 ] || fail "$m: has module dependencies ($n lines from 'go list -m all')"
  fi
done

# Modules that require a sibling at v0.0.0 cannot be built standalone at all:
# v0.0.0 resolves to nothing, because nothing in this repo is tagged. They run
# WITH the workspace, exactly as the exemplars below do and for the same
# reason, and lib.sh derives which ones those are from disk. The label says
# "workspace" out loud so a reader of CI output can see which modules did not
# get the GOWORK=off treatment rather than having to infer it.
#
# The zero-requires assertion above does not apply here -- these modules have
# requires by construction, and SIBLING_ALLOWED in lib.sh records which module
# is permitted to and why. What replaces it is the check below: an import that
# is neither a sibling nor something a sibling already brings is a dependency
# this module invented, and "adopting a core module costs you nothing" is the
# property that would break (conventions.md §11).
for m in ${WORKSPACE_MODULES[@]+"${WORKSPACE_MODULES[@]}"}; do
  echo "== $m (workspace; requires an untagged sibling) =="

  (cd "$m" && go vet ./...) || fail "$m: go vet"
  (cd "$m" && go test -race -count=$(count_for "$m") ./...) || fail "$m: go test"

  # Every non-sibling require must already be required by one of the siblings
  # this module composes. That keeps the composition free: a service adopting
  # it inherits only what the pieces it asked for already cost.
  #
  # Stated plainly, this is weaker than `go list -m all` under GOWORK=off and
  # in one specific way: it matches on module path, not version, so it does
  # not see the version skew that GOWORK=off exists to expose. There is no
  # cheap way to recover that for a module whose requires cannot resolve;
  # it comes back on its own when the siblings are tagged and this module
  # returns to the standalone loop.
  sibling_mods=$(sibling_requires "$m" |
    sed -E 's|^[[:space:]]*(require[[:space:]]+)?github\.com/pavelpascari/svcrt/||; s|[[:space:]].*||')
  externals=$(grep -E '^[[:space:]]*(require[[:space:]]+)?[^[:space:]]+\.[^[:space:]/]+/[^[:space:]]+[[:space:]]+v' "$m/go.mod" |
    grep -v '=>' | grep -v '// indirect' |
    sed -E 's|^[[:space:]]*(require[[:space:]]+)?||; s|[[:space:]].*||' |
    grep -v '^github\.com/pavelpascari/svcrt/' || true)
  for path in $externals; do
    found=false
    for s in $sibling_mods; do
      if grep -qF "$path v" "$s/go.mod"; then found=true; fi
    done
    $found || fail "$m requires $path, which none of the siblings it composes requires. A composition module must not invent dependencies of its own -- adopting it would then cost more than adopting the pieces it composes (conventions.md §11)."
  done
done

# Spec §4.2: kit composes the client side and deliberately does NOT import
# httpserver -- the asymmetry is a design decision (a service builds one
# handler chain in one visible place, but a client per upstream), and a
# deliberate omission that nothing enforces is a comment. Asserted
# structurally, here, alongside contract's import assertion below.
#
# TestImports counts as much as Imports: a test that reached for httpserver
# would put it in kit/go.mod, which is the coupling being prevented.
kitimports=$(cd kit && go list -f '{{join .Imports "\n"}}
{{join .TestImports "\n"}}' ./... | sort -u)
for i in $kitimports; do
  case "$i" in
    github.com/pavelpascari/svcrt/httpserver*)
      fail "kit imports $i. kit ships no server-side helper and must not import httpserver (spec §4.2): the server ordering stays the caller's, documented on telemetry.Server. If that decision is being reversed, reverse it in the spec first." ;;
  esac
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

# The README's Go programs must compile against this tree.
#
# Delegated to its own script because it builds throwaway modules; run it
# directly with ./scripts/readme.sh while editing the README.
./scripts/readme.sh || fail "README go blocks"

# No tracked file may be an executable.
#
# R5 committed a 10MB Mach-O binary at the repo root and it survived coverage,
# mutation, race detection and nine milestones of review -- because every gate
# this repo has inspects Go code, and nothing looked at what was in the tree.
#
# Checked by content rather than by name: a .gitignore entry only catches the
# build output someone already thought of, and the next one will have a
# different name.
command -v file >/dev/null ||
  fail "the 'file' command is unavailable, so the tracked-binary check cannot run; install it rather than skipping the check"

while IFS= read -r tracked; do
  [ -f "$tracked" ] || continue
  case "$(file -b --mime-type "$tracked")" in
    application/x-mach-binary|application/x-executable|application/x-sharedlib|application/x-pie-executable)
      allowed=false
      for e in ${BINARY_ALLOWED[@]+"${BINARY_ALLOWED[@]}"}; do
        [ "${e%%=*}" = "$tracked" ] && allowed=true
      done
      $allowed ||
        fail "$tracked is a tracked executable ($(ls -lh "$tracked" | awk '{print $5}')). Build output does not belong in git: 'git rm --cached $tracked' and add it to .gitignore. If it genuinely must be tracked, add it to BINARY_ALLOWED in scripts/lib.sh with the reason."
      ;;
  esac
done < <(git ls-files)

# Every Example function must carry an "// Output:" (or "// Unordered output:")
# comment.
#
# Without one, go test COMPILES the example and never RUNS it. The result sits
# in a _test.go file, is counted by `go build`, appears in the docs, and
# asserts nothing -- a gate that looks present and does not apply, which is the
# failure mode this repo has found eight times (docs/conventions.md §12).
#
# Counted per file rather than repo-wide so the message names where to look,
# and greps are anchored so an "// Output:" inside a string literal or a
# comment about outputs cannot satisfy the check.
for f in $(find . -name '*_test.go' -not -path './.git/*' | sort); do
  fns=$(grep -c '^func Example' "$f" || true)
  [ "$fns" -eq 0 ] && continue
  outs=$(grep -cE '^[[:space:]]*// (Output|Unordered output):' "$f" || true)
  [ "$fns" -eq "$outs" ] ||
    fail "$f has $fns Example function(s) but $outs '// Output:' comment(s); an example without one is compiled and never run, so it asserts nothing"
done

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
