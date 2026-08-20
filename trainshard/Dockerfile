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

WORKDIR /src
COPY . .

RUN --mount=type=cache,id=trainshard-go-build,target=/root/.cache/go-build \
    --mount=type=cache,id=trainshard-go-mod,target=/go/pkg/mod \
    go build -mod=readonly -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/trainshardd ./cmd/trainshardd

FROM alpine:3.21

RUN apk add --no-cache ca-certificates xfsprogs

COPY --from=builder /out/trainshardd /usr/local/bin/trainshardd

ENTRYPOINT ["/usr/local/bin/trainshardd"]
