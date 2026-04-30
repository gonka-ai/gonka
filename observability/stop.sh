#!/bin/sh
set -e

echo "Stopping observability stack..."
docker compose down
echo "Stopped."
