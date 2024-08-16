# Stage 1: Build the decentralized-api
FROM golang:1.22.2-alpine AS build-env
WORKDIR /app/decentralized-api

# Build flags
ENV BUILD_FLAGS="-Xgithub.com/cosmos/cosmos-sdk/version.Name=decentralized-api \
     -Xgithub.com/cosmos/cosmos-sdk/version.AppName=decentralized-api"

ENV GOOS=linux
ENV GOARCH=amd64

# Use mounts for caching and source code access
# Mount the decentralized-api and inference-chain directories
RUN --mount=type=cache,target=/go/pkg/mod/ \
        --mount=type=bind,source=/decentralized-api,target=. \
        --mount=type=bind,source=/inference-chain,target=../inference-chain \
    go mod download

RUN --mount=type=cache,target=/go/pkg/mod/ \
        --mount=type=bind,source=/decentralized-api,target=. \
        --mount=type=bind,source=/inference-chain,target=../inference-chain \
    go build -o /tmp/build/dapi

# Stage 2: Create the final runtime image
FROM alpine:latest
WORKDIR /root

# Copy the built decentralized-api binary from the build stage
COPY --from=build-env /tmp/build/dapi /usr/bin/decentralized-api

# Copy configuration files from the build context
COPY decentralized-api/config.yaml /root/config.yaml
COPY decentralized-api/config-docker.yaml /root/config-docker.yaml

# Expose the application port
EXPOSE 8080

# Command to run the decentralized-api binary
CMD ["decentralized-api"]