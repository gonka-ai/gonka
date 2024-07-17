FROM alpine:edge

# Install ca-certificates
RUN apk add --update ca-certificates
WORKDIR /root

# Copy over binaries from the build-env
COPY ./build/inferenced /usr/bin/inferenced
# TODO: VERIFY IF NEEDED
COPY ./app/config.yaml /root/config.yaml
COPY ./scripts/init-docker.sh /root/init-docker.sh
RUN chmod +x /root/init-docker.sh
# TODO: VERIFY IF NEEDED
COPY ./gcp-state/genesis.json /root/genesis.json

EXPOSE 26656 26657 1317 9090

CMD ["inferenced"]
