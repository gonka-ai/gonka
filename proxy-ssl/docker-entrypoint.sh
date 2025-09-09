#!/bin/sh

set -eu

# Ensure certs/data directories exist and are owned by certissuer (uid 1001)
mkdir -p /app/certs /app/data
chown -R 1001:1001 /app/certs /app/data || true

# Drop privileges and exec
exec su-exec 1001:1001 "$@"


