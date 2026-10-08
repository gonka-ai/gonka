#!/bin/bash
set -e

source /app/packages/pow/.venv/bin/activate

exec "$@"
