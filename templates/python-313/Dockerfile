# vibed-template-python-313 — general-lane template for classifier rule 6
# (Python app). See refactor.md §6.
#
# A pooled SandboxClaim binds this image to an app; vibed-agent then drops
# the user's tarball into /workspace and exec's /etc/vibed/entrypoint.sh,
# which autodetects how to start the Python process. Pre-baked web
# frameworks live as site-packages so common Flask/FastAPI/Uvicorn apps
# run with no per-deploy `pip install`.
#
# Build context MUST be the repo root:
#   podman build -f templates/python-313/Dockerfile -t vibed-template-python-313 .

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

# Stage 2: Python 3.13 runtime + pre-baked deps + agent.
FROM python:3.13-slim

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

COPY --from=agent /vibed-agent /usr/local/bin/vibed-agent
COPY templates/python-313/entrypoint.sh /etc/vibed/entrypoint.sh
RUN chmod +x /etc/vibed/entrypoint.sh

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

# Bring-your-own base-image contract: the language vibeD routes here, plus a
# probe it runs via GET /info to confirm the runtime is actually present.
LABEL dev.vibed.template.language="python" \
      dev.vibed.agent.contract="1"
ENV VIBED_TEMPLATE_LANGUAGE=python \
    VIBED_RUNTIME_PROBE="python3 --version"

# 9000: in-cluster control API (never exposed publicly). 8080: the user app.
EXPOSE 9000 8080

ENTRYPOINT ["tini", "--", "vibed-agent"]
