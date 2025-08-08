# Extract Geth binary from prebuilt container
FROM ghcr.io/product-science/bridge-geth:latest AS geth-source

# Extract Prysm binaries from prebuilt container  
FROM ghcr.io/product-science/bridge-prysm:latest AS prysm-source

# Final image
FROM alpine:latest
RUN apk add --no-cache ca-certificates openssl bash curl

# Copy Geth binary from prebuilt container
COPY --from=geth-source /usr/local/bin/geth /usr/local/bin/

# Copy Prysm binaries from prebuilt container
COPY --from=prysm-source /usr/local/bin/beacon-chain /usr/local/bin/
COPY --from=prysm-source /usr/local/bin/validator /usr/local/bin/

# Create necessary directories
RUN mkdir -p /data/geth /data/prysm /data/jwt

# Set environment variables
ENV GETH_DATA_DIR=/data/geth
ENV PRYSM_DATA_DIR=/data/prysm
ENV JWT_SECRET_PATH=/data/jwt/jwt.hex

# Copy startup script
COPY script.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/script.sh

ENTRYPOINT ["/usr/local/bin/script.sh"] 