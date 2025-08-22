# syntax=docker/dockerfile:1.4

################################################################################
# Build the application
################################################################################
FROM golang:1.23.2-alpine3.20 AS builder

ARG BUILD_FLAGS
ARG GOOS
ARG GOARCH

ENV GOOS=${GOOS} \
    GOARCH=${GOARCH} \
    CGO_ENABLED=1 \
    GO111MODULE=on \
    GOCACHE=/root/.cache/go-build \
    CGO_CFLAGS="-I/lib" \
    CGO_LDFLAGS="-L/lib" \
    # Override the wasmvm library path to use our musl version
    LD_LIBRARY_PATH=/lib

RUN apk add --no-cache make gcc musl-dev git patchelf

# Use the musl libc version of wasmvm and create a symlink for the linker
RUN if [ "$GOARCH" = "arm64" ]; then \
        wget -O /lib/libwasmvm_muslc.aarch64.a https://github.com/CosmWasm/wasmvm/releases/download/v1.5.9/libwasmvm_muslc.aarch64.a \
        && echo "a43cb22bf85e89bea45c9af04a229bc92f70ddd8216fee7db21d349d7579cff6 /lib/libwasmvm_muslc.aarch64.a" | sha256sum -c \
        && ln -s /lib/libwasmvm_muslc.aarch64.a /lib/libwasmvm_muslc.a \
        && rm -f /go/pkg/mod/github.com/\!cosm\!wasm/wasmvm@v1.5.9/internal/api/libwasmvm.aarch64.so; \
    else \
        wget -O /lib/libwasmvm_muslc.x86_64.a https://github.com/CosmWasm/wasmvm/releases/download/v1.5.9/libwasmvm_muslc.x86_64.a \
        && echo "797a235aefb5f8b2d60ecd0f3a430bab28d913b46b7661c0280dc7195a7e1144 /lib/libwasmvm_muslc.x86_64.a" | sha256sum -c \
        && ln -s /lib/libwasmvm_muslc.x86_64.a /lib/libwasmvm_muslc.a \
        && rm -f /go/pkg/mod/github.com/\!cosm\!wasm/wasmvm@v1.5.9/internal/api/libwasmvm.x86_64.so; \
    fi

COPY inference-chain/ /app/inference-chain

WORKDIR /app/decentralized-api
COPY decentralized-api/go.mod decentralized-api/go.sum ./

RUN --mount=type=cache,id=go-build-cache3,target=/root/.cache/go-build \
    CGO_ENABLED=1 CC=gcc \
    go mod download

COPY decentralized-api/. .

# ARG LDFLAGS
RUN --mount=type=cache,id=go-build-cache3,target=/root/.cache/go-build \
    CGO_ENABLED=1 CC=gcc \
    go build -mod=readonly -tags muslc -ldflags "$LDFLAGS" \
    -o ./build/dapi \
    && patchelf --set-rpath '$ORIGIN:$ORIGIN/current:/root/.dapi/cosmovisor/current' ./build/dapi

# TO FIX: temporary to have both binaries in api image
RUN --mount=type=cache,id=go-build-cache3,target=/root/.cache/go-build \
    cd /app/inference-chain && \
    CGO_ENABLED=1 CC=gcc \
    go build -mod=readonly -tags muslc -ldflags "-X github.com/cosmos/cosmos-sdk/version.Name=inference-chain -X github.com/cosmos/cosmos-sdk/version.AppName=inference-chaind" \
    -o ./build/inferenced ./cmd/inferenced/main.go \
    && patchelf --set-rpath '$ORIGIN:$ORIGIN/current:/root/.inference/cosmovisor/current' ./build/inferenced

RUN mkdir /build_output
################################################################################
# Binary Exporter stage (for --output optimization)
################################################################################
FROM scratch AS binary-exporter
# Copy all required files to a flat structure
COPY --from=builder /app/decentralized-api/build/dapi /build_output/decentralized-api
COPY --from=builder /app/inference-chain/build/inferenced /build_output/inferenced
COPY --from=builder /lib/libwasmvm_muslc.x86_64.a /build_output/libwasmvm_muslc.x86_64.a
COPY --from=builder /usr/lib/libgcc_s.so.1 /build_output/libgcc_s.so.1

################################################################################
# Final image
################################################################################
ARG TARGETPLATFORM
FROM --platform=$TARGETPLATFORM alpine:3.18 AS final

ARG GOOS
ARG GOARCH

RUN apk update && \
    apk add --no-cache sed ca-certificates jq libgcc musl-dev curl && \
    rm -rf /var/cache/apk/*

WORKDIR /root

ENV DAEMON_HOME=/root/.dapi
ENV DAEMON_NAME=decentralized-api
ENV DAEMON_ALLOW_DOWNLOAD_BINARIES=true
ENV DAEMON_RESTART_AFTER_UPGRADE=true
ENV DAEMON_DOWNLOAD_RETRIES=-1

# Copy over binaries from the build-env
COPY --from=builder /app/decentralized-api/build/dapi /usr/bin/decentralized-api
COPY --from=builder /app/inference-chain/build/inferenced /usr/bin/inferenced
COPY ./decentralized-api/scripts/init-docker.sh /root/init-docker.sh
COPY ./decentralized-api/config-prod.yaml /root/api-config.yaml
COPY ./cosmovisor/v1.7.2/${GOOS}-${GOARCH}/cosmovisor /usr/bin/cosmovisor
COPY --from=builder /lib/libwasmvm_muslc.*.a /lib/

ENV API_CONFIG_PATH=/root/.dapi/api-config.yaml

RUN chmod +x /root/init-docker.sh

EXPOSE 9000

CMD ["sh", "./init-docker.sh"]
