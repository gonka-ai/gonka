################################################################################
# Build the application
################################################################################
FROM golang:alpine AS builder

ARG BUILD_FLAGS
ARG GOOS
ARG GOARCH

ENV BUILD_FLAGS=${BUILD_FLAGS}
ENV GOOS=${GOOS}
ENV GOARCH=${GOARCH}

RUN apk add --no-cache make gcc musl-dev git

COPY decentralized-api/ /app/decentralized-api
COPY inference-chain/ /app/inference-chain

WORKDIR /app/decentralized-api
RUN go mod download

RUN make build BUILD_FLAGS="${BUILD_FLAGS}"

################################################################################
# Final image
################################################################################
FROM alpine:edge AS final

WORKDIR /root

# Copy over binaries from the build-env
COPY --from=builder /app/decentralized-api/build/dapi /usr/bin/decentralized-api
COPY ./decentralized-api/scripts/init-docker.sh /root/init-docker.sh
ENV API_CONFIG_PATH=/root/api-config.yaml
COPY ./decentralized-api/config-prod.yaml /root/api-config.yaml

RUN chmod +x /root/init-docker.sh
RUN apk update && \
    apk add --no-cache sed

EXPOSE 8080

CMD ["sh", "./init-docker.sh"]
