# vibed-egress-authz image. Build context MUST be the repo root:
#   podman build -f cmd/vibed-egress-authz/Dockerfile -t vibed-egress-authz:dev .
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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /vibed-egress-authz /vibed-egress-authz
ENTRYPOINT ["/vibed-egress-authz"]
