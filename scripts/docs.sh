#!/usr/bin/env bash
# Serve this repo's documentation offline.
#
# go doc needs nothing and is the right tool for a single lookup:
#   go doc ./resilience Breaker
#
# pkgsite renders the whole workspace the way pkg.go.dev does. Verified to
# discover every module here through go.work, including ones not named on the
# command line.
set -euo pipefail
cd "$(dirname "$0")/.."

addr=${1:-localhost:8080}
bin="$(go env GOPATH)/bin/pkgsite"

# Degrade with instructions rather than "command not found". A docs script
# that fails opaquely is how people conclude the docs do not exist.
if ! command -v pkgsite >/dev/null && [ ! -x "$bin" ]; then
  cat >&2 <<'EOF'
pkgsite is not installed. Install it once:

    go install golang.org/x/pkgsite/cmd/pkgsite@latest

Or read the docs without it:

    go doc ./resilience            # package synopsis
    go doc ./resilience Breaker    # one symbol
EOF
  exit 1
fi

command -v pkgsite >/dev/null && bin=pkgsite

echo "Serving svcrt docs at http://$addr — Ctrl-C to stop" >&2
exec "$bin" -http "$addr" .
