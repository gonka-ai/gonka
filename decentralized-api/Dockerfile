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
    GOCACHE=/root/.cache/go-build

RUN apk add --no-cache make gcc musl-dev git

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
FROM alpine:edge AS final

RUN apk update && \
    apk add --no-cache sed ca-certificates && \
    rm -rf /var/cache/apk/*

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

ENV API_CONFIG_PATH=/root/api-config.yaml

RUN chmod +x /root/init-docker.sh

EXPOSE 8080

CMD ["sh", "./init-docker.sh"]
