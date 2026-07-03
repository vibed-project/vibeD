#!/usr/bin/env bash
#
# check-import-boundary.sh — enforce the vibeD open-core boundary.
#
# The Apache-2.0 OSS core (this module, github.com/vibed-project/vibeD) must never
# depend on the closed commercial modules. Go already forbids a separate module
# from importing THIS module's internal/ packages; this script guards the reverse
# direction: that nothing in the core imports (or requires) the closed modules,
# which would make the OSS repo un-buildable without private code and quietly
# erode the boundary.
#
# Enterprise/cloud code plugs into the core through the public pkg/plugin +
# pkg/server surface — never the other way around.
#
# Run locally: ./scripts/check-import-boundary.sh
set -euo pipefail

# Move to the repository root regardless of where the script is invoked from.
cd "$(dirname "$0")/.."

# Closed module paths the OSS core must not import or require.
FORBIDDEN=(
	"github.com/vibed-project/vibed-enterprise"
	"github.com/vibed-project/vibed-cloud"
)

fail=0

for mod in "${FORBIDDEN[@]}"; do
	# 1. No Go source file may import the module (match the quoted import path,
	#    with or without a sub-package). Skip vendor/ and the git dir.
	if hits=$(grep -rEn "\"${mod}(/[^\"]*)?\"" --include='*.go' \
		--exclude-dir=vendor --exclude-dir=.git .); then
		echo "✖ OSS core imports forbidden module ${mod}:"
		echo "${hits}"
		fail=1
	fi

	# 2. go.mod must not require the module.
	if grep -Eq "^[[:space:]]*${mod}([[:space:]]|/|\$)" go.mod; then
		echo "✖ go.mod requires forbidden module ${mod}"
		fail=1
	fi
done

if [ "${fail}" -ne 0 ]; then
	cat <<'EOF'

Open-core boundary violated.

The Apache-2.0 core must stay independent of the closed commercial modules
(vibed-enterprise, vibed-cloud). Instead of importing a closed module, expose an
extension point in the core (an interface + a Register* wrapper in pkg/plugin)
and implement it in the closed module. See pkg/plugin and PLAN.md.
EOF
	exit 1
fi

echo "✓ open-core boundary intact: no OSS package imports vibed-enterprise / vibed-cloud"
