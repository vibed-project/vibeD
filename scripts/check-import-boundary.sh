#!/usr/bin/env bash
#
# check-import-boundary.sh — assert this module stays self-contained.
#
# The module must reference only its own packages
# (github.com/vibed-project/vibeD/...) and third-party dependencies — never a
# sibling github.com/vibed-project/* module. That keeps it independently
# buildable and stops an accidental cross-repository dependency from creeping in.
# External code integrates the other way around, through the public pkg/plugin +
# pkg/server extension points.
#
# Run locally: ./scripts/check-import-boundary.sh
set -euo pipefail

# Move to the repository root regardless of where the script is invoked from.
cd "$(dirname "$0")/.."

# The only github.com/vibed-project path this module may reference is its own.
SELF="github.com/vibed-project/vibeD"

fail=0

# 1. No Go source file may import a sibling vibed-project module.
if hits=$(grep -rEn "\"github.com/vibed-project/[^\"]+\"" --include='*.go' \
	--exclude-dir=vendor --exclude-dir=.git . | grep -v "\"${SELF}/" | grep -v "\"${SELF}\""); then
	echo "✖ imports a sibling github.com/vibed-project module (must stay self-contained):"
	echo "${hits}"
	fail=1
fi

# 2. go.mod must not require another vibed-project module (the module line itself
#    is excluded).
if reqs=$(grep -E "github.com/vibed-project/" go.mod | grep -v "^module "); then
	echo "✖ go.mod references a sibling github.com/vibed-project module:"
	echo "${reqs}"
	fail=1
fi

if [ "${fail}" -ne 0 ]; then
	cat <<'EOF'

This module must reference only its own packages and third-party dependencies,
not any sibling github.com/vibed-project module. Add an extension point here
(an interface + a Register* wrapper in pkg/plugin) and implement it externally,
instead of importing across modules.
EOF
	exit 1
fi

echo "✓ boundary intact: no sibling github.com/vibed-project module is referenced"
