# vibed-controller image. Build context MUST be the repo root:
#   podman build -f cmd/vibed-controller/Dockerfile -t vibed-controller:dev .
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
    go build -o /vibed-controller ./cmd/vibed-controller

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=builder /vibed-controller /vibed-controller
ENTRYPOINT ["/vibed-controller"]
