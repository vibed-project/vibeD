# vibed-egress-proxy: a Squid forward proxy bundled with the vibed-egress-authz
# binary, which Squid runs as its external_acl helper (squid-helper mode) to
# authorize each outbound connection against the per-app allow-list. squid.conf
# is mounted from a ConfigMap at runtime.
#
# Build context MUST be the repo root:
#   podman build -f deploy/egress-proxy/Dockerfile -t vibed-egress-proxy:dev .
FROM --platform=$BUILDPLATFORM golang:1.23-bookworm AS builder
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
    go build -o /vibed-egress-authz ./cmd/vibed-egress-authz

FROM ubuntu/squid:latest
COPY --from=builder /vibed-egress-authz /usr/local/bin/vibed-egress-authz
