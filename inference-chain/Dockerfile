# syntax=docker/dockerfile:1.4

################################################################################
# Build stage
################################################################################
FROM golang:alpine AS builder

ARG GOOS=linux
ARG GOARCH=amd64

ENV GOOS=${GOOS} \
    GOARCH=${GOARCH} \
    CGO_ENABLED=1 \
    GO111MODULE=on \
    GOCACHE=/root/.cache/go-build \
    CGO_CFLAGS="-I/lib" \
    CGO_LDFLAGS="-L/lib" \
    # Override the wasmvm library path to use our musl version
    LD_LIBRARY_PATH=/lib

RUN apk add --no-cache make gcc musl-dev git

# Use the musl libc version of wasmvm and create a symlink for the linker
RUN if [ "$GOARCH" = "arm64" ]; then \
        wget -O /lib/libwasmvm_muslc.aarch64.a https://github.com/CosmWasm/wasmvm/releases/download/v1.5.2/libwasmvm_muslc.aarch64.a \
        && echo "e78b224c15964817a3b75a40e59882b4d0e06fd055b39514d61646689cef8c6e /lib/libwasmvm_muslc.aarch64.a" | sha256sum -c \
        && ln -s /lib/libwasmvm_muslc.aarch64.a /lib/libwasmvm_muslc.a \
        && rm -f /go/pkg/mod/github.com/\!cosm\!wasm/wasmvm@v1.5.2/internal/api/libwasmvm.aarch64.so; \
    else \
        wget -O /lib/libwasmvm_muslc.x86_64.a https://github.com/CosmWasm/wasmvm/releases/download/v1.5.2/libwasmvm_muslc.x86_64.a \
        && echo "e660a38efb2930b34ee6f6b0bb12730adccb040b6ab701b8f82f34453a426ae7 /lib/libwasmvm_muslc.x86_64.a" | sha256sum -c \
        && ln -s /lib/libwasmvm_muslc.x86_64.a /lib/libwasmvm_muslc.a \
        && rm -f /go/pkg/mod/github.com/\!cosm\!wasm/wasmvm@v1.5.2/internal/api/libwasmvm.x86_64.so; \
    fi

RUN wget -O /root/cw20_base.wasm \
    https://github.com/CosmWasm/cw-plus/releases/download/v2.0.0/cw20_base.wasm \
    && echo "4d8e90dd340993033f1b9e8e3a3ee7f8673c582ca9bcdd8c8cf3c7470d6537d5 /root/cw20_base.wasm" | sha256sum -c

WORKDIR /app/inference-chain

COPY inference-chain/go.mod inference-chain/go.sum ./
RUN --mount=type=cache,id=go-build-cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 CC=gcc \
    go mod download

COPY inference-chain/. .

ARG LDFLAGS
ARG TAGS=""
RUN --mount=type=cache,id=go-build-cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 CC=gcc \
    go build -mod=readonly -tags="muslc ${TAGS}" -ldflags "${LDFLAGS}" \
    -o ./build/inferenced \
    ./cmd/inferenced/main.go

################################################################################
# Binary Exporter stage (for --output optimization)
################################################################################
FROM scratch AS binary-exporter
COPY --from=builder /app/inference-chain/build/inferenced /inferenced

################################################################################
# Final stage
################################################################################
ARG TARGETPLATFORM
FROM --platform=$TARGETPLATFORM alpine:3.18 AS final

ARG GOOS
ARG GOARCH

RUN apk update && \
    apk add --no-cache sed ca-certificates gcc musl-dev && \
    rm -rf /var/cache/apk/*
RUN apk update && apk add --no-cache jq

ARG GENESIS_OVERRIDES_FILE=inference-chain/prod_genesis_overrides.json

WORKDIR /root

ENV DAEMON_HOME=/root/.inference
ENV DAEMON_NAME=inferenced
ENV DAEMON_ALLOW_DOWNLOAD_BINARIES=true
ENV DAEMON_RESTART_AFTER_UPGRADE=true
ENV DAEMON_DOWNLOAD_RETRIES=-1

COPY --from=builder /app/inference-chain/build/inferenced /usr/bin/inferenced
COPY inference-chain/scripts/init-docker-genesis.sh /root/init-docker-genesis.sh
COPY inference-chain/scripts/init-docker.sh /root/init-docker.sh
COPY inference-chain/scripts/reset-state.sh /root/reset-state.sh
COPY inference-chain/tgbot_private_key.json /root/tgbot_private_key.json
COPY inference-chain/app_overrides.toml /root/app_overrides.toml
COPY ./cosmovisor/v1.7.2/${GOOS}-${GOARCH}/cosmovisor /usr/bin/cosmovisor
COPY inference-chain/denom.json /root/denom.json
COPY $GENESIS_OVERRIDES_FILE /root/genesis_overrides.json
COPY --from=builder /lib/libwasmvm_muslc.*.a /lib/
COPY --from=builder /root/cw20_base.wasm /root/cw20_base.wasm

RUN chmod +x /root/init-docker.sh /root/init-docker-genesis.sh

EXPOSE 26656 26657 1317 9090

CMD ["sh", "./init-docker.sh"]