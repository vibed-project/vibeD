#!/usr/bin/env bash
# Configure containerd inside a kind control-plane node so it resolves
# "kind-registry:5000" to the in-cluster registry Service's ClusterIP.
#
# Prereq: the kind cluster was created with `containerdConfigPatches` enabling
# `config_path = "/etc/containerd/certs.d"` (see deploy/kind/kind-config.yaml).

set -euo pipefail

CLUSTER="vibed-dev"
RUNTIME="podman"
REGISTRY_NAMESPACE="default"
REGISTRY_NAME="kind-registry"
REGISTRY_PORT="5000"

usage() {
  cat <<EOF
Usage: $(basename "$0") [flags]

Flags:
  --cluster NAME           kind cluster name (default: ${CLUSTER})
  --runtime podman|docker  container runtime kind uses (default: ${RUNTIME})
  --namespace NS           registry Service namespace (default: ${REGISTRY_NAMESPACE})
  --name NAME              registry Service name (default: ${REGISTRY_NAME})
  --port PORT              registry Service port (default: ${REGISTRY_PORT})
  -h, --help               show this help
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --cluster)   CLUSTER="$2"; shift 2 ;;
    --runtime)   RUNTIME="$2"; shift 2 ;;
    --namespace) REGISTRY_NAMESPACE="$2"; shift 2 ;;
    --name)      REGISTRY_NAME="$2"; shift 2 ;;
    --port)      REGISTRY_PORT="$2"; shift 2 ;;
    -h|--help)   usage; exit 0 ;;
    *) echo "unknown flag: $1" >&2; usage; exit 2 ;;
  esac
done

if [[ "$RUNTIME" != "podman" && "$RUNTIME" != "docker" ]]; then
  echo "--runtime must be 'podman' or 'docker'" >&2
  exit 2
fi

NODE="${CLUSTER}-control-plane"
HOST_DIR="/etc/containerd/certs.d/${REGISTRY_NAME}:${REGISTRY_PORT}"

REGISTRY_IP=$(kubectl get svc "${REGISTRY_NAME}" -n "${REGISTRY_NAMESPACE}" -o jsonpath='{.spec.clusterIP}')
if [[ -z "${REGISTRY_IP}" ]]; then
  echo "registry service ${REGISTRY_NAMESPACE}/${REGISTRY_NAME} has no ClusterIP" >&2
  exit 1
fi

echo "Configuring ${NODE} (${RUNTIME}) → ${REGISTRY_NAME}:${REGISTRY_PORT} → ${REGISTRY_IP}:${REGISTRY_PORT}"

"${RUNTIME}" exec "${NODE}" mkdir -p "${HOST_DIR}"
"${RUNTIME}" exec "${NODE}" sh -c "cat > ${HOST_DIR}/hosts.toml <<EOF
[host.\"http://${REGISTRY_IP}:${REGISTRY_PORT}\"]
  capabilities = [\"pull\", \"resolve\"]
  skip_verify = true
EOF"

echo "Done. containerd will pick up the new hosts.toml on next pull."
