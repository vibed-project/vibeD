# Stage 1: Build React frontend (architecture-independent)
FROM --platform=$BUILDPLATFORM node:22-bookworm-slim AS frontend

WORKDIR /app/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ .
COPY internal/frontend/static/ /app/internal/frontend/static/
RUN npm run build

# Stage 2: Build Go binary (cross-compile natively, no QEMU needed)
FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS builder

ARG TARGETOS=linux
ARG TARGETARCH

ENV GOTOOLCHAIN=auto
ENV GO111MODULE=on

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Copy built frontend assets from Stage 1
COPY --from=frontend /app/internal/frontend/static/ /app/internal/frontend/static/

# VERSION is injected by the release build so the binary reports the version it
# actually shipped as; unset locally, leaving the "dev" default.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -ldflags "-X github.com/vibed-project/vibeD/internal/version.Version=${VERSION}" \
    -o /vibed ./cmd/vibed

# Stage 3: Runtime (multi-arch via manifest)
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /vibed /vibed
COPY vibed.yaml /etc/vibed/vibed.yaml

EXPOSE 8080

ENTRYPOINT ["/vibed"]
CMD ["--config", "/etc/vibed/vibed.yaml", "--transport", "http"]
