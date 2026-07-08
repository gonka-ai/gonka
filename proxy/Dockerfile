# Build Stage for Sidecar
FROM golang:1.24.2-alpine3.21 AS builder
WORKDIR /app
COPY sidecar/main.go .
# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o sidecar main.go

# Final Stage
FROM nginx:1.28-alpine3.21

# Install curl, gettext (for envsubst), jq, bash (for setup-ssl.sh), and
# apache2-utils (htpasswd for observability UI basic auth).
RUN apk add --no-cache curl gettext jq bash apache2-utils

# Copy sidecar binary
COPY --from=builder /app/sidecar /usr/local/bin/sidecar
RUN chmod +x /usr/local/bin/sidecar

# Copy nginx configuration template (unified)
COPY nginx.unified.conf.template /etc/nginx/nginx.unified.conf.template

# Copy scripts
COPY entrypoint.sh /entrypoint.sh
COPY setup-ssl.sh /setup-ssl.sh
RUN chmod +x /entrypoint.sh /setup-ssl.sh

# Create directories
RUN mkdir -p /var/log/nginx /etc/nginx/ssl

# Expose ports
EXPOSE 80 443

# Use custom entrypoint
ENTRYPOINT ["/entrypoint.sh"]

# Start nginx
CMD ["nginx", "-g", "daemon off;"] 