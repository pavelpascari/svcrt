#!/usr/bin/env bash
# Compile every complete Go program in README.md.
#
# The README's quickstart was broken for an unknown length of time: it named
# lifecycle.Options (the type is lifecycle.Config), treated OnDrain as a struct
# field (it is a method), and never started a server. Nothing caught it,
# because nothing in this repo had ever compiled a README snippet.
#
# A ```go block is treated as a complete program if it has a `package` clause.
# Blocks without one are illustrative fragments and are skipped -- see the
# Composition order section, which shows call shapes rather than a program.
set -euo pipefail
cd "$(dirname "$0")/.."

fail() { echo "FAIL: $*" >&2; exit 1; }

. scripts/lib.sh

# Captured before any subshell: $(pwd) inside `( cd "$mod" && ... )` is the temp
# module, not this checkout, which silently produced replace directives
# pointing at directories that do not exist.
root=$(pwd)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Extract each fenced go block to its own file.
awk -v dir="$work" '
  /^```go$/ { inb = 1; n++; next }
  /^```$/   { inb = 0; next }
  inb       { print > (dir "/block" n ".txt") }
' README.md

found=0
for src in "$work"/block*.txt; do
  [ -f "$src" ] || continue
  grep -qE '^package ' "$src" || continue    # fragment, not a program
  found=$((found + 1))

  mod="$work/m$found"
  mkdir -p "$mod"
  cp "$src" "$mod/main.go"
  ( cd "$mod" && go mod init readmeblock >/dev/null 2>&1 )
  # Point every svcrt import at this checkout rather than the proxy: the
  # modules are unpublished, and the README must describe THIS tree.
  for m in "${MODULES[@]}"; do
    ( cd "$mod" && go mod edit \
        -require="github.com/pavelpascari/svcrt/$m@v0.0.0" \
        -replace="github.com/pavelpascari/svcrt/$m=$root/$m" )
  done
  ( cd "$mod" && GOWORK=off go mod tidy 2>"$work/tidy$found.err" ) ||
    fail "README go block $found: 'go mod tidy' failed; the snippet imports something that does not resolve:
$(tail -5 "$work/tidy$found.err")"
  ( cd "$mod" && GOWORK=off go build ./... ) ||
    fail "README go block $found does not compile. The README documents this tree, so a snippet that will not build is wrong about the API."
done

# A gate that silently checks nothing is worse than no gate. If the README
# ever loses its complete example, say so rather than passing.
[ "$found" -gt 0 ] ||
  fail "README.md contains no complete Go program (a \`\`\`go block with a 'package' clause), so this check verified nothing"

echo "README: $found complete Go block(s) compile"
