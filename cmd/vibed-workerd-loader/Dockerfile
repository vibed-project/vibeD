# vibed-workerd-loader sidecar image. Build context MUST be the repo root:
#   podman build -f cmd/vibed-workerd-loader/Dockerfile -t vibed-workerd-loader:dev .
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder
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
    go build -o /vibed-workerd-loader ./cmd/vibed-workerd-loader

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /vibed-workerd-loader /vibed-workerd-loader
ENTRYPOINT ["/vibed-workerd-loader"]
