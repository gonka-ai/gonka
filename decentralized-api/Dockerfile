# Stage 1: Build the decentralized-api
FROM golang:1.22.2-alpine AS build-env
WORKDIR /app

# Copy the entire context (including inference-chain)
COPY . .
COPY ../inference-chain /app/inference-chain

# Build decentralized-api
WORKDIR /app/decentralized-api
RUN go mod download
RUN go build -o /build/dapi

# Stage 2: Create the final runtime image
FROM alpine:latest
WORKDIR /root

# Copy the built decentralized-api binary from the build stage
COPY --from=build-env /build/dapi /usr/bin/decentralized-api

# Copy configuration files if needed
COPY /decentralized-api/config.yaml /root/config.yaml
COPY /decentralized-api/config-docker.yaml /root/config-docker.yaml

# Expose the application port
EXPOSE 8080

# Command to run the decentralized-api binary
CMD ["decentralized-api"]