# syntax=docker/dockerfile:1.4

################################################################################
# Build — runs on the native platform, cross-compiles via GOOS/GOARCH
################################################################################
FROM --platform=$BUILDPLATFORM golang:1.24.2-alpine3.21 AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

COPY go.mod go.sum ./
RUN --mount=type=cache,id=go-build-cache-devshard,target=/root/.cache/go-build \
    --mount=type=cache,id=go-mod-cache-devshard,target=/go/pkg/mod \
    go mod download

COPY . .

RUN --mount=type=cache,id=go-build-cache-devshard,target=/root/.cache/go-build \
    --mount=type=cache,id=go-mod-cache-devshard,target=/go/pkg/mod \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -o /devshardctl ./cmd/devshardctl/

################################################################################
# Final image
################################################################################
FROM alpine:3.21

RUN apk add --no-cache ca-certificates curl && \
    rm -rf /var/cache/apk/*

RUN mkdir -p /root/.devshardctl

COPY --from=builder /devshardctl /usr/bin/devshardctl

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=5s \
    CMD curl -f http://localhost:${DEVSHARD_PORT:-8080}/v1/status || exit 1

ENTRYPOINT ["devshardctl"]
