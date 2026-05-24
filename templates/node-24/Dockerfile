# vibed-template-node-24 — general-lane template for classifier rule 5
# (Node app with deps). See refactor.md §6.
#
# A pooled SandboxClaim binds this image to an app; vibed-agent then drops
# the user's tarball into /workspace and exec's /etc/vibed/entrypoint.sh,
# which autodetects how to start the Node process. Pre-baked common
# packages live on NODE_PATH so simple Express/CORS apps run with no
# per-deploy `npm install`.
#
# Build context MUST be the repo root:
#   podman build -f templates/node-24/Dockerfile -t vibed-template-node-24 .

# Stage 1: build vibed-agent (cross-compile natively, no QEMU).
FROM --platform=$BUILDPLATFORM golang:1.23-bookworm AS agent
ARG TARGETOS=linux
ARG TARGETARCH
ENV GOTOOLCHAIN=auto GO111MODULE=on
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY pkg/ ./pkg/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -o /vibed-agent ./cmd/vibed-agent

# Stage 2: Node 24 runtime + pre-baked deps + agent.
FROM node:24-bookworm-slim

# tini reaps zombies orphaned by the user process; the agent only supervises
# its own direct child, so it must not run as literal PID 1.
RUN apt-get update \
 && apt-get install -y --no-install-recommends tini \
 && rm -rf /var/lib/apt/lists/*

# Pre-bake common packages into a fixed location and put it on NODE_PATH, so
# `require('express')` resolves even though /workspace is wiped on every inject.
# This list MUST stay in sync with internal/prebaked/manifests/nodejs.yaml —
# the single source of truth the fast-path dependency gate trusts.
RUN mkdir -p /opt/vibed \
 && cd /opt/vibed \
 && npm install --no-audit --no-fund \
      express \
      cors
ENV NODE_PATH=/opt/vibed/node_modules
# Ship that same manifest in the image for reference / introspection.
COPY internal/prebaked/manifests/nodejs.yaml /etc/vibed/prebaked.yaml

COPY --from=agent /vibed-agent /usr/local/bin/vibed-agent
COPY templates/node-24/entrypoint.sh /etc/vibed/entrypoint.sh
RUN chmod +x /etc/vibed/entrypoint.sh

# Non-root, UID 1000 — matches the runAsNonRoot podSecurityContext vibeD sets
# on every workload. The node base image already ships a `node` user at UID
# 1000, so reuse it rather than creating a colliding account. /workspace is
# where the agent writes injected source.
RUN mkdir -p /workspace && chown node:node /workspace
USER 1000
WORKDIR /workspace

ENV VIBED_AGENT_WORKDIR=/workspace \
    VIBED_AGENT_CONTROL_ADDR=:9000 \
    VIBED_AGENT_APP_PORT=8080

# Bring-your-own base-image contract: the language vibeD routes here, plus a
# probe it runs via GET /info to confirm the runtime is actually present.
LABEL dev.vibed.template.language="node" \
      dev.vibed.agent.contract="1"
ENV VIBED_TEMPLATE_LANGUAGE=node \
    VIBED_RUNTIME_PROBE="node --version"

# 9000: in-cluster control API (never exposed publicly). 8080: the user app.
EXPOSE 9000 8080

ENTRYPOINT ["tini", "--", "vibed-agent"]
