# Extract Geth binary from prebuilt container
FROM ghcr.io/product-science/bridge-geth:0.2.12@sha256:57f8e07499b4798337e1a224741657aa7b06dd2669e5411854731139bb2a072c AS geth-source

# Extract Prysm binaries from prebuilt container  
FROM ghcr.io/product-science/bridge-prysm:0.2.12@sha256:bdace2b516a5c502aa2e5575433f3c35986002ea3bc18e5cb54a307bf4aafa7a AS prysm-source

# Final image
FROM alpine:latest
# coreutils provides stdbuf for line-buffering Prysm logs
RUN apk add --no-cache ca-certificates openssl bash curl coreutils

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