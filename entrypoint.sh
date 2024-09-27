#!/bin/bash
set -e

if [ -n "$HOST_UID" ] && [ -n "$HOST_GID" ]; then
    echo "Creating user with UID: $HOST_UID and GID: $HOST_GID"
    groupadd -g "$HOST_GID" appgroup
    useradd -u "$HOST_UID" -g appgroup appuser
fi

exec "$@"
