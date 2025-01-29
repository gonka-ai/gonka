# syntax=docker/dockerfile:1.4

################################################################################
# Build stage
###############################################################

# Build wasmvm stage
FROM alpine:3.18 AS wasmvm-builder
RUN apk add --no-cache wget

WORKDIR /lib
# Download all available wasmvm libraries
RUN WASMVM_VERSION=v2.2.1 && \
    wget https://github.com/CosmWasm/wasmvm/releases/download/${WASMVM_VERSION}/libwasmvm.aarch64.so && \
    wget https://github.com/CosmWasm/wasmvm/releases/download/${WASMVM_VERSION}/libwasmvm.x86_64.so && \
    wget https://github.com/CosmWasm/wasmvm/releases/download/${WASMVM_VERSION}/libwasmvm_muslc.aarch64.a && \
    wget https://github.com/CosmWasm/wasmvm/releases/download/${WASMVM_VERSION}/libwasmvm_muslc.x86_64.a && \
    wget https://github.com/CosmWasm/wasmvm/releases/download/${WASMVM_VERSION}/libwasmvm.dylib && \
    wget https://github.com/CosmWasm/wasmvm/releases/download/${WASMVM_VERSION}/libwasmvmstatic_darwin.a && \
    wget https://github.com/CosmWasm/wasmvm/releases/download/${WASMVM_VERSION}/checksums.txt && \
    sha256sum -c checksums.txt

# Main build stage
FROM golang:alpine AS builder

ARG GOOS=linux
ARG GOARCH=amd64

ENV GOOS=${GOOS} \
    GOARCH=${GOARCH} \
    CGO_ENABLED=1 \
    GO111MODULE=on \
    GOCACHE=/root/.cache/go-build

RUN apk add --no-cache make gcc musl-dev git build-base

WORKDIR /app/inference-chain

# Download dependencies first
COPY inference-chain/go.mod inference-chain/go.sum inference-chain/go.work ./
COPY inference-chain/x/ibc/capability ./x/ibc/capability
RUN --mount=type=cache,id=go-build-cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 CC=gcc \
    go mod download

# Copy source code
COPY inference-chain/. .
COPY --from=wasmvm-builder /lib/libwasmvm* /usr/lib/

RUN --mount=type=cache,id=go-build-cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 CC=gcc \
    GODEBUG=netdns=go \
    CGO_LDFLAGS="-L/usr/lib" \
    go build -mod=readonly -ldflags "$LDFLAGS -extldflags '-static -L/usr/lib -lwasmvm_muslc.x86_64'" \
    -tags muslc,netgo,static_build \
    -o ./build/inferenced \
    ./cmd/inferenced/main.go

################################################################################
# Final stage
################################################################################
FROM alpine:3.18 AS final

RUN apk update && \
    apk add --no-cache sed ca-certificates jq wget && \
    rm -rf /var/cache/apk/*
RUN apk update && \
    apk add --no-cache libc6-compat gcompat && \
    rm -rf /var/cache/apk/*

WORKDIR /root

ENV DAEMON_HOME=/root/.inference
ENV DAEMON_NAME=inferenced
ENV DAEMON_ALLOW_DOWNLOAD_BINARIES=true
ENV DAEMON_RESTART_AFTER_UPGRADE=true

COPY --from=builder /app/inference-chain/build/inferenced /usr/bin/inferenced
COPY inference-chain/scripts/init-docker-genesis.sh /root/init-docker-genesis.sh
COPY inference-chain/scripts/prod/init-docker.sh /root/init-docker.sh
COPY ./cosmovisor/v1.7.0/linux-amd64/cosmovisor /usr/bin/cosmovisor

RUN chmod +x /root/init-docker.sh /root/init-docker-genesis.sh

EXPOSE 26656 26657 1317 9090

CMD ["sh", "./init-docker.sh"]
