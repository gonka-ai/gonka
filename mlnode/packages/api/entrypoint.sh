#!/bin/bash
set -e

HOST_UID=${HOST_UID:-1000}
HOST_GID=${HOST_GID:-1001}

if ! getent group appgroup >/dev/null; then
  echo "Creating group 'appgroup'"
  groupadd -g "$HOST_GID" appgroup
else
  echo "Group 'appgroup' already exists"
fi

if ! id -u appuser >/dev/null 2>&1; then
  echo "Creating user 'appuser'"
  useradd -m -u "$HOST_UID" -g appgroup appuser
else
  echo "User 'appuser' already exists"
fi

source /app/packages/api/.venv/bin/activate

exec "$@"
