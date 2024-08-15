FROM ignitehq/cli:latest AS builder
# build
WORKDIR /app
COPY . .
USER root

RUN ignite chain build --output /app/build/inferenced

FROM alpine:latest
# Install ca-certificates
RUN apk add --update ca-certificates
WORKDIR /root

# Copy over binaries from the build-env
COPY --from=builder /app/build/inferenced /usr/bin/inferenced
COPY ./scripts/init-docker.sh /root/init-docker.sh
# TODO: copy genesis.json for a node?
# COPY ./gcp-state/genesis.json /root/genesis.json

EXPOSE 26656 26657 1317 9090

CMD ["inferenced"]
