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

RUN apk add --no-cache make gcc musl-dev musl-tools git

COPY inference-chain/ /app/inference-chain

WORKDIR /app/inference-chain
RUN go mod download

RUN make build BUILD_FLAGS="${BUILD_FLAGS}"

################################################################################
# Final image
################################################################################
FROM alpine:edge AS final

# Install ca-certificates
RUN apk add --update ca-certificates
WORKDIR /root

# Copy over binaries from the build-env
COPY --from=builder /app/inference-chain/build/inferenced /usr/bin/inferenced
COPY ./inference-chain/scripts/init-docker-genesis.sh /root/init-docker-genesis.sh
COPY ./inference-chain/scripts/prod/init-docker.sh /root/init-docker.sh

RUN chmod +x /root/init-docker.sh
RUN chmod +x /root/init-docker-genesis.sh

EXPOSE 26656 26657 1317 9090

CMD ["sh", "./init-docker.sh"]
