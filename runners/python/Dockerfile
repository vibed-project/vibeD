# vibed-runner-python — a warm-pool runner image for the Instant Preview fast path.
#
# It bundles a Python interpreter, a curated set of pre-baked web frameworks,
# and the vibeD runner agent (PID-supervised by tini). It is built once and
# pushed to the cluster registry — never per request. A pooled pod from this
# image sits idle until vibeD injects a user's source over the agent's control
# API, at which point the user process starts with no build step.
#
# Build context MUST be the repo root:
#   podman build -f runners/python/Dockerfile -t vibed-runner-python .

# Stage 1: build the runner agent (cross-compile natively, no QEMU).
FROM --platform=$BUILDPLATFORM golang:1.23-bookworm AS agent
ARG TARGETOS=linux
ARG TARGETARCH
ENV GOTOOLCHAIN=auto GO111MODULE=on
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
# Copy only the Go trees so the agent layer doesn't rebuild on web/docs changes.
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -o /vibed-runner-agent ./cmd/vibed-runner-agent

# Stage 2: Python runtime + pre-baked deps + agent.
FROM python:3.12-slim

# tini reaps zombies orphaned by the user process; the agent only supervises
# its own direct child, so it must not run as literal PID 1.
RUN apt-get update \
 && apt-get install -y --no-install-recommends tini \
 && rm -rf /var/lib/apt/lists/*

# Pre-bake common web frameworks so zero-dependency / pre-baked apps run with
# no install step. This list MUST stay in sync with
# internal/prebaked/manifests/python.yaml — the single source of truth the
# fast-path dependency gate trusts.
RUN pip install --no-cache-dir --root-user-action=ignore \
      flask \
      fastapi \
      "uvicorn[standard]" \
      gunicorn \
      requests \
      jinja2 \
      pydantic
# Ship that same manifest in the image for reference / introspection.
COPY internal/prebaked/manifests/python.yaml /etc/vibed/prebaked.yaml

COPY --from=agent /vibed-runner-agent /usr/local/bin/vibed-runner-agent

# Non-root, UID 1000 — matches the runAsNonRoot podSecurityContext vibeD sets
# on every workload. /workspace is where the agent writes injected source.
RUN useradd --uid 1000 --create-home --home-dir /home/runner runner \
 && mkdir -p /workspace \
 && chown runner:runner /workspace
USER 1000
WORKDIR /workspace

ENV VIBED_AGENT_WORKDIR=/workspace \
    VIBED_AGENT_CONTROL_ADDR=:9000 \
    VIBED_AGENT_APP_PORT=8080

# 9000: in-cluster control API (never exposed publicly). 8080: the user app.
EXPOSE 9000 8080

ENTRYPOINT ["tini", "--", "vibed-runner-agent"]
