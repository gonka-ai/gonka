FROM golang:1.24-alpine AS builder

# Install build dependencies
RUN apk add --no-cache git ca-certificates tzdata

# Set working directory
WORKDIR /app

# ---- Runtime configuration (override via -e or compose) ----
# Non-secret defaults (safe to bake into image). Secrets like JWT or DNS API keys
# should be provided at runtime via environment variables or secrets.
ENV PORT=8080 \
    LOG_LEVEL=info \
    CERT_STORAGE_PATH=/app/certs \
    DATA_PATH=/app/data

# Required-at-runtime (leave unset here; provide via -e in prod):
# - ACME_ACCOUNT_EMAIL
# - ACME_DNS_PROVIDER (e.g., cloudflare|route53|gcloud|azure|digitalocean|hetzner)
# - CERT_ISSUER_DOMAIN (base domain, e.g., example.com)
# - CERT_ISSUER_ALLOWED_SUBDOMAINS (comma-separated, optional)
# - CERT_ISSUER_JWT_SECRET (HMAC key for the API)

# Copy go mod files
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy source code
COPY . .

# Build the application
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o proxy-ssl ./cmd/cert-issuer
# Final stage
FROM alpine:latest

# Install runtime dependencies and su-exec for dropping privileges
RUN apk --no-cache add ca-certificates tzdata curl su-exec

# Create non-root user
RUN addgroup -g 1001 -S proxy-ssl && \
    adduser -S certissuer -u 1001

# Set working directory
WORKDIR /app

# Copy binary from builder stage
COPY --from=builder /app/proxy-ssl .

# Create directories for certificates and data
RUN mkdir -p /app/certs /app/data && \
    chown -R certissuer:proxy-ssl /app
VOLUME ["/app/certs", "/app/data"]

# Add entrypoint that fixes volume ownership then drops privileges
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]

# Expose port
EXPOSE 8080

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:8080/health || exit 1

# Default command (run as non-root via entrypoint)
CMD ["./proxy-ssl"]
