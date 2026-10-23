# syntax=docker/dockerfile:1.4

FROM golang:1.25.9-alpine3.23 AS builder

ARG GOOS
ARG GOARCH
ARG VERSION=dev

ENV GOOS=${GOOS} \
    GOARCH=${GOARCH} \
    CGO_ENABLED=0 \
    GOCACHE=/root/.cache/go-build \
    GOMODCACHE=/go/pkg/mod

# the chain's own types come from the module next door, so the build starts a directory up
WORKDIR /src
COPY inference-chain ./inference-chain
COPY trainshard ./trainshard

WORKDIR /src/trainshard
RUN --mount=type=cache,id=trainshard-go-build,target=/root/.cache/go-build \
    --mount=type=cache,id=trainshard-go-mod,target=/go/pkg/mod \
    go build -mod=mod -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/trainshardd ./cmd/trainshardd

# the driver tool this daemon reads the cards with is injected from the host and is linked against
# glibc, so a musl base leaves it unable to run at all
FROM debian:12-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates xfsprogs \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/trainshardd /usr/local/bin/trainshardd

ENTRYPOINT ["/usr/local/bin/trainshardd"]
