#!/bin/sh
# Per-template entrypoint for the kitchen-sink template — used when the
# classifier saw a Dockerfile-only deploy (rule 8) or anything it didn't
# recognize (rule 9). Cascades through the per-language autodetects in
# priority order; first hit wins.
#
# Precedence:
#   1. VIBED_USER_COMMAND env  — explicit override; run as-is via `sh -c`.
#   2. Pre-built binary at /workspace/app — Go's convention, but anyone
#      can drop a binary here.
#   3. Node: package.json scripts.start, then well-known entry files.
#   4. Python: app.py / main.py / server.py / run.py.
#   5. Go: go.mod present → `go run .`.
#   6. Hard failure with a clear message.
set -eu
cd /workspace

if [ -n "${VIBED_USER_COMMAND:-}" ]; then
    exec sh -c "$VIBED_USER_COMMAND"
fi

if [ -x ./app ]; then
    exec ./app
fi

# Node detection.
if [ -f package.json ] && grep -q '"start"[[:space:]]*:' package.json; then
    exec npm start
fi
for entry in index.js server.js app.js main.js; do
    if [ -f "$entry" ]; then
        exec node "$entry"
    fi
done

# Python detection.
for entry in app.py main.py server.py run.py; do
    if [ -f "$entry" ]; then
        exec python3 "$entry"
    fi
done

# Go detection.
if [ -f go.mod ]; then
    exec go run .
fi

echo "vibed-agent: kitchen-sink autodetect found nothing under /workspace; set VIBED_USER_COMMAND or include one of the standard entrypoints (app, package.json scripts.start, index.js / app.py / main.go, …)" >&2
exit 1
