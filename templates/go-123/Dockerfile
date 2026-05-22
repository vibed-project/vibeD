# vibed-template-go-123 — general-lane template for classifier rule 7
# (go.mod present). See refactor.md §6.
#
# A pooled SandboxClaim binds this image to an app; vibed-agent drops the
# user's tarball into /workspace and exec's /etc/vibed/entrypoint.sh, which
# either runs a pre-built binary or `go run .`. The Go toolchain stays in
# the runtime image (not just the build stage) so the warm pool can
# absorb-and-build small apps in <2 s without the cold "pull go" cost.
#
# Build context MUST be the repo root:
#   podman build -f templates/go-123/Dockerfile -t vibed-template-go-123 .

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

# Stage 2: Go 1.23 toolchain + agent.
FROM golang:1.23-bookworm

# tini reaps zombies orphaned by the user process; the agent supervises
# only its direct child so it must not run as literal PID 1.
RUN apt-get update \
 && apt-get install -y --no-install-recommends tini \
 && rm -rf /var/lib/apt/lists/*

# Pre-warm the build cache. The Go base image already has the toolchain;
# we pre-download a handful of stdlib-heavy packages so first-deploy
# compile time isn't waiting on the module proxy.
ENV GOMODCACHE=/opt/go/pkg/mod GOCACHE=/opt/go/cache
RUN mkdir -p $GOMODCACHE $GOCACHE \
 && cd /tmp && go mod init prewarm \
 && go get \
      github.com/gin-gonic/gin@latest \
      github.com/labstack/echo/v4@latest \
      github.com/gorilla/mux@latest \
 && rm -rf /tmp/prewarm /tmp/go.mod /tmp/go.sum

COPY --from=agent /vibed-agent /usr/local/bin/vibed-agent
COPY templates/go-123/entrypoint.sh /etc/vibed/entrypoint.sh
RUN chmod +x /etc/vibed/entrypoint.sh

# Non-root, UID 1000 — matches the runAsNonRoot podSecurityContext.
RUN useradd --uid 1000 --create-home --home-dir /home/runner runner \
 && mkdir -p /workspace \
 && chown runner:runner /workspace $GOCACHE $GOMODCACHE
USER 1000
WORKDIR /workspace

ENV VIBED_AGENT_WORKDIR=/workspace \
    VIBED_AGENT_CONTROL_ADDR=:9000 \
    VIBED_AGENT_APP_PORT=8080

# 9000: in-cluster control API (never exposed publicly). 8080: the user app.
EXPOSE 9000 8080

ENTRYPOINT ["tini", "--", "vibed-agent"]
