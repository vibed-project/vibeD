# vibed-template-base-al2023 — kitchen-sink general-lane template for the
# classifier's "I don't know what this is" cases (rule 8: Dockerfile at
# root, treated as a runtime hint not a build; rule 9: unknown fallback).
# See refactor.md §6 / §6.1.
#
# Bundles Node 24, Python 3.13, Go 1.23, plus sudo + dnf so an in-pod
# user can `sudo dnf install` if they truly need something the classifier
# couldn't predict. Targets ≤1.5 GB compressed per refactor.md §6.2 — the
# largest template by design; keep the kitchen-sink instinct in check
# when expanding this list.
#
# Build context MUST be the repo root:
#   podman build -f templates/base-al2023/Dockerfile -t vibed-template-base-al2023 .

# Stage 1: build vibed-agent (cross-compile natively, no QEMU).
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS agent
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

# Stage 2: Amazon Linux 2023 with the major runtimes.
FROM amazonlinux:2023
ARG TARGETARCH

# Single dnf transaction so the image has one big layer instead of many.
# --setopt=install_weak_deps=False trims a few hundred MB. sudo, nodejs +
# python3 + golang are all in the AL2023 default repos. (tini is NOT packaged
# for AL2023 — we fetch the static binary below.)
RUN dnf install -y --setopt=install_weak_deps=False \
      sudo \
      ca-certificates \
      shadow-utils \
      tar gzip \
      nodejs npm \
      python3 python3-pip \
      golang \
 && dnf clean all \
 && rm -rf /var/cache/dnf

# tini reaps zombies orphaned by the user process. Not in AL2023 repos, so
# pull the static binary for the target arch from the upstream release.
ADD https://github.com/krallin/tini/releases/download/v0.19.0/tini-static-${TARGETARCH} /usr/bin/tini
RUN chmod +x /usr/bin/tini

# Common Python + Node packages so the most-asked-for stacks need no
# install. Lists are deliberately short — base-al2023's job is "works for
# anything weird", not "works for every framework"; the language-specific
# templates absorb the framework-bake load.
# AL2023 ships pip 21.x (Python 3.9), which neither enforces PEP 668 nor knows
# --break-system-packages, so install straight into the system site-packages.
RUN python3 -m pip install --no-cache-dir \
      flask fastapi "uvicorn[standard]" requests \
 && mkdir -p /opt/vibed \
 && cd /opt/vibed && npm install --no-audit --no-fund express
ENV NODE_PATH=/opt/vibed/node_modules

COPY --from=agent /vibed-agent /usr/local/bin/vibed-agent
COPY templates/base-al2023/entrypoint.sh /etc/vibed/entrypoint.sh
RUN chmod +x /etc/vibed/entrypoint.sh

# Non-root, UID 1000.
RUN useradd --uid 1000 --create-home --home-dir /home/runner runner \
 && mkdir -p /workspace \
 && chown runner:runner /workspace
USER 1000
WORKDIR /workspace

ENV VIBED_AGENT_WORKDIR=/workspace \
    VIBED_AGENT_CONTROL_ADDR=:9000 \
    VIBED_AGENT_APP_PORT=8080

# Bring-your-own base-image contract. base-al2023 is the kitchen-sink fallback,
# so the probe asserts all three bundled runtimes are present.
LABEL dev.vibed.template.language="base" \
      dev.vibed.agent.contract="1"
ENV VIBED_TEMPLATE_LANGUAGE=base \
    VIBED_RUNTIME_PROBE="node --version && python3 --version && go version"

EXPOSE 9000 8080

ENTRYPOINT ["tini", "--", "vibed-agent"]
