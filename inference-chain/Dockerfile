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

# ARG LDFLAGS
RUN --mount=type=cache,id=go-build-cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 CC=gcc \
    go build -mod=readonly -ldflags "$LDFLAGS" \
    -o ./build/inferenced \
    ./cmd/inferenced/main.go

################################################################################
# Final stage
################################################################################
FROM alpine:3.18 AS final

RUN apk update && \
    apk add --no-cache sed ca-certificates && \
    rm -rf /var/cache/apk/*
RUN apk update && apk add --no-cache jq

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
