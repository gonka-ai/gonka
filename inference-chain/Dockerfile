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
    GOCACHE=/root/.cache/go-build

RUN apk add --no-cache make gcc musl-dev git

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
    go build -mod=readonly -ldflags "${LDFLAGS}" -tags="${TAGS}"\
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
    apk add --no-cache sed ca-certificates && \
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
COPY ./cosmovisor/v1.7.2/${GOOS}-${GOARCH}/cosmovisor /usr/bin/cosmovisor
COPY inference-chain/denom.json /root/denom.json
COPY $GENESIS_OVERRIDES_FILE /root/genesis_overrides.json

RUN chmod +x /root/init-docker.sh /root/init-docker-genesis.sh

EXPOSE 26656 26657 1317 9090

CMD ["sh", "./init-docker.sh"]