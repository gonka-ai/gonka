#!/bin/bash
set -e

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

exec "$@"
