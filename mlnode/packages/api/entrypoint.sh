#!/bin/bash
set -e

# libcuda.so.1 is mounted at runtime by nvidia-docker; create the .so symlink
# that Triton's linker expects (common issue on Vast.ai / cloud containers).
if [ -f /lib/x86_64-linux-gnu/libcuda.so.1 ] && [ ! -e /lib/x86_64-linux-gnu/libcuda.so ]; then
  ln -sf /lib/x86_64-linux-gnu/libcuda.so.1 /lib/x86_64-linux-gnu/libcuda.so
fi

source /app/packages/api/.venv/bin/activate

exec "$@"
