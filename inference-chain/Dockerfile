# syntax=docker/dockerfile:1.4

################################################################################
# Build stage
################################################################################
FROM golang:alpine AS builder

ARG GOOS=linux
ARG GOARCH=amd64

ENV GOOS=${GOOS}
ENV GOARCH=${GOARCH}
ENV GO111MODULE=on
ENV GOCACHE=/root/.cache/go-build

RUN apk add --no-cache make gcc musl-dev git

WORKDIR /app/inference-chain

COPY inference-chain/go.mod inference-chain/go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 CC=gcc \
    go mod download

COPY inference-chain/. .

ARG LDFLAGS
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=1 CC=gcc \
    go build -mod=readonly -ldflags "$LDFLAGS" \
    -o ./build/inferenced \
    ./cmd/inferenced/main.go

################################################################################
# Final stage
################################################################################
FROM alpine:3.18 AS final

RUN apk add --no-cache ca-certificates

WORKDIR /root

COPY --from=builder /app/inference-chain/build/inferenced /usr/bin/inferenced
COPY inference-chain/scripts/init-docker-genesis.sh /root/init-docker-genesis.sh
COPY inference-chain/scripts/prod/init-docker.sh /root/init-docker.sh

RUN chmod +x /root/init-docker.sh /root/init-docker-genesis.sh

EXPOSE 26656 26657 1317 9090

CMD ["sh", "./init-docker.sh"]
