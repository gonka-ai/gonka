# syntax=docker/dockerfile:1.4

################################################################################
# Build stage
################################################################################
FROM golang:1.24.2-alpine3.20 AS builder

ARG GOOS=linux
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
    BLST_PORTABLE=${BLST_PORTABLE} \
    LD_LIBRARY_PATH=/lib

RUN apk add --no-cache make gcc musl-dev git patchelf

# Use the musl libc version of wasmvm and create a symlink for the linker
RUN if [ "$GOARCH" = "arm64" ]; then \
        wget -O /lib/libwasmvm_muslc.aarch64.a https://github.com/CosmWasm/wasmvm/releases/download/v2.2.4/libwasmvm_muslc.aarch64.a \
        && echo "27fb13821dbc519119f4f98c30a42cb32429b111b0fdc883686c34a41777488f /lib/libwasmvm_muslc.aarch64.a" | sha256sum -c \
        && ln -s /lib/libwasmvm_muslc.aarch64.a /lib/libwasmvm_muslc.a \
        && rm -f /go/pkg/mod/github.com/\!cosm\!wasm/wasmvm@v2.2.4/internal/api/libwasmvm.aarch64.so; \
    else \
        wget -O /lib/libwasmvm_muslc.x86_64.a https://github.com/CosmWasm/wasmvm/releases/download/v2.2.4/libwasmvm_muslc.x86_64.a \
        && echo "70c989684d2b48ca17bbd55bb694bbb136d75c393c067ef3bdbca31d2b23b578 /lib/libwasmvm_muslc.x86_64.a" | sha256sum -c \
        && ln -s /lib/libwasmvm_muslc.x86_64.a /lib/libwasmvm_muslc.a \
        && rm -f /go/pkg/mod/github.com/\!cosm\!wasm/wasmvm@v2.2.4/internal/api/libwasmvm.x86_64.so; \
    fi

COPY inference-chain/contracts/wrapped-token/artifacts/wrapped_token.wasm /root/wrapped_token.wasm

WORKDIR /app/inference-chain

COPY inference-chain/go.mod inference-chain/go.sum ./

RUN --mount=type=cache,id=go-build-cache,target=/root/.cache/go-build \
    --mount=type=cache,id=go-mod-cache,target=/go/pkg/mod \
    CGO_ENABLED=1 CC=gcc \
    go mod download

COPY inference-chain/. .

ARG LDFLAGS
ARG TAGS=""
RUN --mount=type=cache,id=go-build-cache,target=/root/.cache/go-build \
    --mount=type=cache,id=go-mod-cache,target=/go/pkg/mod \
    if [ "$BLST_PORTABLE" = "1" ]; then export CGO_CFLAGS="$CGO_CFLAGS -D__BLST_PORTABLE__"; fi; \
    CGO_ENABLED=1 CC=gcc \
    go build -mod=readonly -tags="muslc ${TAGS}" -ldflags "${LDFLAGS}" \
    -o ./build/inferenced \
    ./cmd/inferenced/main.go \
    && patchelf --set-rpath '$ORIGIN:$ORIGIN/current:/root/.inference/cosmovisor/current' ./build/inferenced
RUN mkdir /build_output

################################################################################
# Binary Exporter stage (for --output optimization)
################################################################################
FROM scratch AS binary-exporter
# Copy all required files to a flat structure
COPY --from=builder /app/inference-chain/build/inferenced /build_output/inferenced
COPY --from=builder /lib/libwasmvm_muslc.x86_64.a /build_output/libwasmvm_muslc.x86_64.a
COPY --from=builder /root/wrapped_token.wasm /build_output/wrapped_token.wasm
COPY --from=builder /usr/lib/libgcc_s.so.1 /build_output/libgcc_s.so.1

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
COPY inference-chain/contracts /root/contracts
COPY inference-chain/tgbot_private_key.json /root/tgbot_private_key.json
COPY inference-chain/app_overrides.toml /root/app_overrides.toml
COPY ./cosmovisor/v1.7.2/${GOOS}-${GOARCH}/cosmovisor /usr/bin/cosmovisor
COPY inference-chain/denom.json /root/denom.json
COPY $GENESIS_OVERRIDES_FILE /root/genesis_overrides.json
COPY --from=builder /lib/libwasmvm_muslc.*.a /lib/
COPY --from=builder /root/wrapped_token.wasm /root/wrapped_token.wasm

RUN chmod +x /root/init-docker.sh /root/init-docker-genesis.sh \
    && find /root/contracts -type f -name "*.sh" -exec chmod +x {} +

EXPOSE 26656 26657 1317 9090

CMD ["sh", "./init-docker.sh"]
