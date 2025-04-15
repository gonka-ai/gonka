# syntax=docker/dockerfile:1.4

################################################################################
# Build the application
################################################################################
FROM golang:1.23.5-bookworm AS builder

ARG BUILD_FLAGS
ARG GOOS
ARG GOARCH

ENV GOOS=${GOOS} \
    GOARCH=${GOARCH} \
    CGO_ENABLED=1 \
    GO111MODULE=on \
    GOCACHE=/root/.cache/go-build

RUN apt-get update && \
    apt-get install -y make gcc build-essential git && \
    rm -rf /var/lib/apt/lists/*

RUN wget -O /lib/libwasmvm.x86_64.so https://github.com/CosmWasm/wasmvm/releases/download/v1.5.2/libwasmvm.x86_64.so \
    && echo "b0c3b761e5f00e45bdafebcfe9c03bd703b88b3f535c944ca8e27ef9b891cd10 /lib/libwasmvm.x86_64.so" | sha256sum -c

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
    go build -mod=readonly -ldflags "$LDFLAGS" \
    -o ./build/dapi

################################################################################
# Final image
################################################################################
FROM debian:bookworm-slim AS final

RUN apt-get update && \
    apt-get install -y sed ca-certificates jq && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /root

ENV DAEMON_HOME=/root/.dapi
ENV DAEMON_NAME=decentralized-api
ENV DAEMON_ALLOW_DOWNLOAD_BINARIES=true
ENV DAEMON_RESTART_AFTER_UPGRADE=true

# Copy over binaries from the build-env
COPY --from=builder /app/decentralized-api/build/dapi /usr/bin/decentralized-api
COPY ./decentralized-api/scripts/init-docker.sh /root/init-docker.sh
COPY ./decentralized-api/config-prod.yaml /root/api-config.yaml
COPY ./cosmovisor/v1.7.0/linux-amd64/cosmovisor /usr/bin/cosmovisor
COPY --from=builder /lib/libwasmvm.x86_64.so /lib/libwasmvm.x86_64.so

ENV API_CONFIG_PATH=/root/api-config.yaml

RUN chmod +x /root/init-docker.sh

EXPOSE 8080

CMD ["sh", "./init-docker.sh"]
