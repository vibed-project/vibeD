# vibed-router image. Build context MUST be the repo root:
#   podman build -f cmd/vibed-router/Dockerfile -t vibed-router:dev .
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
    go build -o /vibed-router ./cmd/vibed-router

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /vibed-router /vibed-router
ENTRYPOINT ["/vibed-router"]
