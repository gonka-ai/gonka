# Stage 1: Build the binary
FROM golang:1.22.2-alpine AS builder
WORKDIR /app

ENV GOOS=linux
ENV GOARCH=amd64

# Use mounts for caching and source code access
# This assumes the build context is the root of your project directory
RUN --mount=type=bind,source=.,target=. \
    --mount=type=cache,target=/go/pkg/mod/ \
    go build -o /tmp/build/inferenced ./cmd/inferenced/main.go

# Stage 2: Create the final runtime image
FROM alpine:latest

# Install ca-certificates
RUN apk add --update ca-certificates
WORKDIR /root

# Copy over binaries from the build stage
COPY --from=builder /tmp/build/inferenced /usr/bin/inferenced
COPY ./scripts/init-docker.sh /root/init-docker.sh

# TODO: copy genesis.json for a node?
# COPY ./gcp-state/genesis.json /root/genesis.json

EXPOSE 26656 26657 1317 9090

CMD ["inferenced"]