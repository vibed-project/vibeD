#!/usr/bin/env bash
# Create a kind cluster for the vibeD testbed using the bundled kind-config.yaml.

set -euo pipefail

CLUSTER="vibed-dev"
RUNTIME="podman"
CONFIG="$(cd "$(dirname "$0")" && pwd)/kind-config.yaml"

usage() {
  cat <<EOF
Usage: $(basename "$0") [flags]

Flags:
  --cluster NAME           kind cluster name (default: ${CLUSTER})
  --runtime podman|docker  container runtime kind should use (default: ${RUNTIME})
  --config PATH            kind config file (default: ${CONFIG})
  -h, --help               show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cluster) CLUSTER="$2"; shift 2 ;;
    --runtime) RUNTIME="$2"; shift 2 ;;
    --config)  CONFIG="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage; exit 2 ;;
  esac
done

if [[ "$RUNTIME" != "podman" && "$RUNTIME" != "docker" ]]; then
  echo "--runtime must be 'podman' or 'docker'" >&2
  exit 2
fi

if ! command -v kind >/dev/null 2>&1; then
  echo "kind not found on PATH" >&2
  exit 1
fi

if KIND_EXPERIMENTAL_PROVIDER="${RUNTIME}" kind get clusters 2>/dev/null | grep -qx "${CLUSTER}"; then
  echo "kind cluster '${CLUSTER}' already exists. Skipping create."
  echo "  (To recreate: ./teardown.sh --cluster ${CLUSTER} --runtime ${RUNTIME} && rerun)"
  exit 0
fi

echo "Creating kind cluster '${CLUSTER}' (${RUNTIME}) from ${CONFIG}"
KIND_EXPERIMENTAL_PROVIDER="${RUNTIME}" kind create cluster \
  --name "${CLUSTER}" \
  --config "${CONFIG}"

echo
echo "Cluster ready. Suggested next steps:"
echo "  helm install kind-registry  testbed/kind-registry/"
echo "  testbed/kind-registry/scripts/configure-containerd.sh --cluster ${CLUSTER} --runtime ${RUNTIME}"
echo "  helm install observability  testbed/observability/  -n monitoring       --create-namespace"
echo "  helm install keycloak       testbed/keycloak/       -n vibed-system     --create-namespace"
echo "  helm install agent-sandbox  testbed/agent-sandbox/  -n agent-sandbox-system --create-namespace"
