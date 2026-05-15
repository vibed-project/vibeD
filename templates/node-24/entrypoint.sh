#!/bin/sh
# Per-template entrypoint invoked by vibed-agent after the user's source
# has been extracted into /workspace. Mirrors the autodetect logic in
# internal/appspec.RunCommand but lives in the image so the controller can
# default every inject's Command to /etc/vibed/entrypoint.sh and stay
# template-agnostic.
#
# Precedence:
#   1. VIBED_USER_COMMAND env  — explicit override; run as-is via `sh -c`.
#   2. package.json scripts.start — the canonical Node convention.
#   3. First existing well-known entry file: index.js, server.js, app.js, main.js.
#   4. Hard failure with a clear message.
set -eu
cd /workspace

if [ -n "${VIBED_USER_COMMAND:-}" ]; then
    exec sh -c "$VIBED_USER_COMMAND"
fi

# scripts.start: grep is fragile, but we only need a yes/no signal and npm
# itself reports the real error if the script is missing. Avoids pulling
# jq into the runtime image just for this check.
if [ -f package.json ] && grep -q '"start"[[:space:]]*:' package.json; then
    exec npm start
fi

for entry in index.js server.js app.js main.js; do
    if [ -f "$entry" ]; then
        exec node "$entry"
    fi
done

echo "vibed-agent: no Node entrypoint found under /workspace (looked for package.json scripts.start, then index.js, server.js, app.js, main.js)" >&2
exit 1
