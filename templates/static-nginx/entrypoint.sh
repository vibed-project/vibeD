#!/bin/sh
# Per-template entrypoint invoked by vibed-agent after the user's source has
# been extracted into /workspace. The agent passes this script path as the
# inject Command for the static-nginx template; it exec's nginx in the
# foreground so the agent supervises a single child.
set -eu

exec nginx -g 'daemon off;' -c /etc/nginx/nginx.conf
