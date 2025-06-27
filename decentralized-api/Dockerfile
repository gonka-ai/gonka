# syntax=docker/dockerfile:1.4

################################################################################
# Build the application
################################################################################
FROM golang:alpine AS builder

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

RUN apk add --no-cache make gcc musl-dev git

# Use the musl libc version of wasmvm and create a symlink for the linker
RUN wget -O /lib/libwasmvm_muslc.x86_64.a https://github.com/CosmWasm/wasmvm/releases/download/v1.5.2/libwasmvm_muslc.x86_64.a \
    && echo "e660a38efb2930b34ee6f6b0bb12730adccb040b6ab701b8f82f34453a426ae7 /lib/libwasmvm_muslc.x86_64.a" | sha256sum -c \
    && ln -s /lib/libwasmvm_muslc.x86_64.a /lib/libwasmvm_muslc.a \
    # Remove the glibc version from the Go module cache
    && rm -f /go/pkg/mod/github.com/\!cosm\!wasm/wasmvm@v1.5.2/internal/api/libwasmvm.x86_64.so

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
    -o ./build/dapi

################################################################################
# Binary Exporter stage (for --output optimization)
################################################################################
FROM scratch AS binary-exporter
COPY --from=builder /app/decentralized-api/build/dapi /decentralized-api

################################################################################
# Final image
################################################################################
ARG TARGETPLATFORM
FROM --platform=$TARGETPLATFORM alpine:3.18 AS final

ARG GOOS
ARG GOARCH

RUN apk update && \
    apk add --no-cache sed ca-certificates jq libgcc musl-dev && \
    rm -rf /var/cache/apk/*

WORKDIR /root

ENV DAEMON_HOME=/root/.dapi
ENV DAEMON_NAME=decentralized-api
ENV DAEMON_ALLOW_DOWNLOAD_BINARIES=true
ENV DAEMON_RESTART_AFTER_UPGRADE=true
ENV DAEMON_DOWNLOAD_RETRIES=-1

# Copy over binaries from the build-env
COPY --from=builder /app/decentralized-api/build/dapi /usr/bin/decentralized-api
COPY ./decentralized-api/scripts/init-docker.sh /root/init-docker.sh
COPY ./decentralized-api/config-prod.yaml /root/api-config.yaml
COPY ./cosmovisor/v1.7.2/${GOOS}-${GOARCH}/cosmovisor /usr/bin/cosmovisor
COPY --from=builder /lib/libwasmvm_muslc.x86_64.a /lib/libwasmvm_muslc.x86_64.a

ENV API_CONFIG_PATH=/root/api-config.yaml

RUN chmod +x /root/init-docker.sh

EXPOSE 9000

CMD ["sh", "./init-docker.sh"]
