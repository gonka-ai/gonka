FROM alpine:edge

WORKDIR /root

# Copy over binaries from the build-env
COPY ./build/decentralized-api /usr/bin/decentralized-api
# TODO: VERIFY IF NEEDED
COPY ./config.yaml /root/config.yaml

EXPOSE 8080

CMD ["decentralized-api"]
