#!/bin/bash
set -e

# Activate the uv virtual environment
source /app/mlnode/packages/train/.venv/bin/activate

# Execute the command passed to the container
exec "$@"