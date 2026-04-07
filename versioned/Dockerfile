FROM golang:1.24.2-alpine3.20 AS builder
WORKDIR /app
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /versiond ./cmd/versiond

FROM alpine:3.20
RUN apk add --no-cache tini ca-certificates
COPY --from=builder /versiond /usr/bin/versiond
ENTRYPOINT ["tini", "--"]
CMD ["versiond"]
