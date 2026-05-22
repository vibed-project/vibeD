#!/usr/bin/env bash
# Delete the testbed kind cluster.

set -euo pipefail

CLUSTER="vibed-dev"
RUNTIME="podman"

usage() {
  cat <<EOF
Usage: $(basename "$0") [flags]

Flags:
  --cluster NAME           kind cluster name (default: ${CLUSTER})
  --runtime podman|docker  container runtime kind uses (default: ${RUNTIME})
  -h, --help               show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cluster) CLUSTER="$2"; shift 2 ;;
    --runtime) RUNTIME="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage; exit 2 ;;
  esac
done

if [[ "$RUNTIME" != "podman" && "$RUNTIME" != "docker" ]]; then
  echo "--runtime must be 'podman' or 'docker'" >&2
  exit 2
fi

if ! KIND_EXPERIMENTAL_PROVIDER="${RUNTIME}" kind get clusters 2>/dev/null | grep -qx "${CLUSTER}"; then
  echo "kind cluster '${CLUSTER}' does not exist. Nothing to do."
  exit 0
fi

echo "Deleting kind cluster '${CLUSTER}' (${RUNTIME})"
KIND_EXPERIMENTAL_PROVIDER="${RUNTIME}" kind delete cluster --name "${CLUSTER}"
