# syntax=docker/dockerfile:1.4

FROM golang:1.25.9-alpine3.23 AS builder

ARG GOOS
ARG GOARCH=amd64
ARG BLST_PORTABLE=0

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

WORKDIR /app/edge-api
COPY edge-api/go.mod edge-api/go.sum ./

RUN --mount=type=cache,id=edge-api-go-build,target=/root/.cache/go-build \
    --mount=type=cache,id=edge-api-go-mod,target=/go/pkg/mod \
    CGO_ENABLED=1 CC=gcc go mod download

COPY edge-api/. .

RUN --mount=type=cache,id=edge-api-go-build,target=/root/.cache/go-build \
    --mount=type=cache,id=edge-api-go-mod,target=/go/pkg/mod \
    if [ "$BLST_PORTABLE" = "1" ]; then export CGO_CFLAGS="$CGO_CFLAGS -D__BLST_PORTABLE__"; fi; \
    CGO_ENABLED=1 CC=gcc \
    go build -mod=readonly -tags muslc \
    -o ./build/edge-api ./cmd/edge-api

################################################################################
# Binary Exporter stage (for --output / build-for-upgrade)
################################################################################
FROM scratch AS binary-exporter
COPY --from=builder /app/edge-api/build/edge-api /build_output/edge-api
COPY --from=builder /usr/lib/libgcc_s.so.1 /build_output/libgcc_s.so.1

FROM alpine:3.23 AS runtime
RUN apk add --no-cache ca-certificates libgcc
COPY --from=builder /app/edge-api/build/edge-api /usr/local/bin/edge-api
EXPOSE 18080
ENTRYPOINT ["edge-api"]
