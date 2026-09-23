FROM golang:1.25.9-alpine3.23 AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /versiond ./cmd/versiond

FROM alpine:3.21
RUN apk add --no-cache tini ca-certificates libgcc
COPY --from=builder /versiond /usr/bin/versiond
ENTRYPOINT ["tini", "--"]
CMD ["versiond"]
