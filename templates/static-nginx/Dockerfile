# vibed-template-static-nginx — fast-lane template for pure-static deploys
# (classifier rule 1, refactor.md §5.2).
#
# nginx serves /workspace. vibed-agent supervises the nginx process so the
# same /inject control API works as for language templates: vibeD pushes a
# tarball, the agent extracts into /workspace, then runs the per-template
# entrypoint.sh which exec's nginx.
#
# Build context MUST be the repo root:
#   podman build -f templates/static-nginx/Dockerfile -t vibed-template-static-nginx .

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

# Stage 2: nginx + agent.
#
# nginxinc/nginx-unprivileged ships nginx configured to listen on :8080 and
# run as UID 101 so we don't need a setcap dance or root for binding.
FROM nginxinc/nginx-unprivileged:alpine

USER root

# tini reaps zombies orphaned by the nginx worker tree. The agent only
# supervises its own direct child, so it must not be literal PID 1.
RUN apk add --no-cache tini

COPY --from=agent /vibed-agent /usr/local/bin/vibed-agent
COPY templates/static-nginx/entrypoint.sh /etc/vibed/entrypoint.sh
RUN chmod +x /etc/vibed/entrypoint.sh

# nginx config: serve everything under /workspace, fall back to index.html so
# client-side routing works. The site is rewritten by vibed-agent's /inject;
# nginx itself never touches user data outside /workspace.
COPY templates/static-nginx/nginx.conf /etc/nginx/nginx.conf

# Workspace is where the agent writes injected source. Pre-create with the
# right ownership so the agent (UID 1000, matching the PodSecurityStandards
# vibeD sets) and nginx (UID 101) can read it.
RUN mkdir -p /workspace \
 && chown 1000:1000 /workspace \
 && chmod 0755 /workspace

USER 1000
WORKDIR /workspace

ENV VIBED_AGENT_WORKDIR=/workspace \
    VIBED_AGENT_CONTROL_ADDR=:9000 \
    VIBED_AGENT_APP_PORT=8080

# 9000: in-cluster control API (never exposed publicly). 8080: nginx.
EXPOSE 9000 8080

ENTRYPOINT ["tini", "--", "vibed-agent"]
