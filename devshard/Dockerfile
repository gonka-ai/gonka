# syntax=docker/dockerfile:1.4

FROM golang:1.25.9-alpine3.23 AS devshardctl-builder

ARG DEVSHARD_VERSION=dev

RUN apk add --no-cache git

COPY inference-chain/ /app/inference-chain
COPY common/ /app/common

WORKDIR /app/devshard
COPY devshard/go.mod devshard/go.sum ./

RUN --mount=type=cache,id=devshard-go-build,target=/root/.cache/go-build \
    --mount=type=cache,id=devshard-go-mod,target=/go/pkg/mod \
    go mod download

COPY devshard/. .

# devshardctl: pure Go, no CGO needed
RUN --mount=type=cache,id=devshard-go-build,target=/root/.cache/go-build \
    --mount=type=cache,id=devshard-go-mod,target=/go/pkg/mod \
    CGO_ENABLED=0 go build \
    -ldflags "-X main.Version=${DEVSHARD_VERSION} -X devshard/types.buildStateRootProtocolVersion=${DEVSHARD_VERSION}" \
    -o /devshardctl ./cmd/devshardctl

# Testenv / gateway-only image (no CGO devshardd build).
FROM alpine:3.23 AS devshardctl-runtime
RUN apk add --no-cache ca-certificates wget
COPY --from=devshardctl-builder /devshardctl /usr/local/bin/devshardctl

FROM golang:1.25.9-alpine3.23 AS builder

ARG GOOS
ARG GOARCH=amd64
ARG BLST_PORTABLE=0
ARG DEVSHARD_VERSION=dev
ARG DEVSHARD_BINARY_VERSION=dev-log

ENV GOOS=${GOOS} \
    GOARCH=${GOARCH} \
    CGO_ENABLED=1 \
    GO111MODULE=on \
    GOCACHE=/root/.cache/go-build \
    GOMODCACHE=/go/pkg/mod \
    CGO_CFLAGS="-I/lib -O2" \
    CGO_CFLAGS_ALLOW=".*" \
    CGO_LDFLAGS="-L/lib" \
    BLST_PORTABLE=${BLST_PORTABLE}

RUN apk add --no-cache make gcc musl-dev git

# wasmvm musl static lib — same version and checksums as decentralized-api/Dockerfile.
RUN if [ "$GOARCH" = "arm64" ]; then \
        wget -O /lib/libwasmvm_muslc.aarch64.a https://github.com/CosmWasm/wasmvm/releases/download/v2.2.4/libwasmvm_muslc.aarch64.a \
        && echo "27fb13821dbc519119f4f98c30a42cb32429b111b0fdc883686c34a41777488f /lib/libwasmvm_muslc.aarch64.a" | sha256sum -c \
        && ln -s /lib/libwasmvm_muslc.aarch64.a /lib/libwasmvm_muslc.a; \
    else \
        wget -O /lib/libwasmvm_muslc.x86_64.a https://github.com/CosmWasm/wasmvm/releases/download/v2.2.4/libwasmvm_muslc.x86_64.a \
        && echo "70c989684d2b48ca17bbd55bb694bbb136d75c393c067ef3bdbca31d2b23b578 /lib/libwasmvm_muslc.x86_64.a" | sha256sum -c \
        && ln -s /lib/libwasmvm_muslc.x86_64.a /lib/libwasmvm_muslc.a; \
    fi

COPY inference-chain/ /app/inference-chain
COPY common/ /app/common

WORKDIR /app/devshard
COPY devshard/go.mod devshard/go.sum ./

RUN --mount=type=cache,id=devshard-go-build,target=/root/.cache/go-build \
    --mount=type=cache,id=devshard-go-mod,target=/go/pkg/mod \
    CGO_ENABLED=1 CC=gcc go mod download

COPY devshard/. .

# devshardctl: pure Go, no CGO needed
RUN --mount=type=cache,id=devshard-go-build,target=/root/.cache/go-build \
    --mount=type=cache,id=devshard-go-mod,target=/go/pkg/mod \
    CGO_ENABLED=0 go build \
    -ldflags "-X main.Version=${DEVSHARD_VERSION} -X devshard/types.buildStateRootProtocolVersion=${DEVSHARD_VERSION}" \
    -o ./build/devshardctl ./cmd/devshardctl

# devshardd: CGO + muslc required for blst
RUN --mount=type=cache,id=devshard-go-build,target=/root/.cache/go-build \
    --mount=type=cache,id=devshard-go-mod,target=/go/pkg/mod \
    if [ "$BLST_PORTABLE" = "1" ]; then export CGO_CFLAGS="$CGO_CFLAGS -D__BLST_PORTABLE__"; fi; \
    CGO_ENABLED=1 CC=gcc \
    go build -mod=readonly -tags muslc \
    -ldflags "-X main.Version=${DEVSHARD_VERSION} -X main.BinaryVersion=${DEVSHARD_BINARY_VERSION} -X devshard/types.buildStateRootProtocolVersion=${DEVSHARD_VERSION}" \
    -o ./build/devshardd ./cmd/devshardd

FROM alpine:3.23 AS runtime
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/devshard/build/devshardd /usr/local/bin/devshardd
COPY --from=builder /app/devshard/build/devshardctl /usr/local/bin/devshardctl
