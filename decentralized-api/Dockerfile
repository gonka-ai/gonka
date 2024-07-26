FROM alpine:edge

WORKDIR /root

# Copy over binaries from the build-env
COPY ./build/dapi /usr/bin/decentralized-api
# TODO: VERIFY IF NEEDED
COPY ./config.yaml /root/config.yaml
COPY ./config-docker.yaml /root/config-docker.yaml

EXPOSE 8080

CMD ["decentralized-api"]
