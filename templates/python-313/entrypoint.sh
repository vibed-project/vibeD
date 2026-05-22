#!/bin/sh
# Per-template entrypoint invoked by vibed-agent after the user's source
# has been extracted into /workspace. Mirrors the autodetect logic in
# internal/appspec.RunCommand but lives in the image so the controller can
# default every inject's Command to /etc/vibed/entrypoint.sh and stay
# template-agnostic.
#
# Precedence:
#   1. VIBED_USER_COMMAND env — explicit override; run as-is via `sh -c`.
#   2. First existing well-known entry file: app.py, main.py, server.py, run.py.
#   3. Hard failure with a clear message.
set -eu
cd /workspace

if [ -n "${VIBED_USER_COMMAND:-}" ]; then
    exec sh -c "$VIBED_USER_COMMAND"
fi

for entry in app.py main.py server.py run.py; do
    if [ -f "$entry" ]; then
        exec python "$entry"
    fi
done

echo "vibed-agent: no Python entrypoint found under /workspace (looked for app.py, main.py, server.py, run.py)" >&2
exit 1
