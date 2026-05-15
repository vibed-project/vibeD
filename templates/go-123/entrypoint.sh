#!/bin/sh
# Per-template entrypoint invoked by vibed-agent after the user's source
# has been extracted into /workspace. Refactor.md §5.4 step 4 calls out the
# Go autodetect: prefer a pre-built binary, otherwise `go run .`.
#
# Precedence:
#   1. VIBED_USER_COMMAND env — explicit override; run as-is via `sh -c`.
#   2. A pre-built binary at /workspace/app (the convention vibeD ships).
#   3. `go run .` — works for module-aware single-package apps.
#   4. Hard failure with a clear message.
set -eu
cd /workspace

if [ -n "${VIBED_USER_COMMAND:-}" ]; then
    exec sh -c "$VIBED_USER_COMMAND"
fi

if [ -x ./app ]; then
    exec ./app
fi

if [ -f go.mod ]; then
    exec go run .
fi

echo "vibed-agent: no Go entrypoint found under /workspace (looked for /workspace/app binary or go.mod for 'go run .')" >&2
exit 1
