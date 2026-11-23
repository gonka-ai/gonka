#!/bin/bash
set -e

# libcuda.so.1 is mounted at runtime by nvidia-docker; create the .so symlink
# that Triton's linker expects (common issue on Vast.ai / cloud containers).
if [ -f /lib/x86_64-linux-gnu/libcuda.so.1 ] && [ ! -e /lib/x86_64-linux-gnu/libcuda.so ]; then
  ln -sf /lib/x86_64-linux-gnu/libcuda.so.1 /lib/x86_64-linux-gnu/libcuda.so
fi

# Docker starts containers with a soft limit of 1024 open files. The chat proxy
# holds two sockets per request, so under load it runs out (Errno 24), the vLLM
# heartbeat fails and the watcher exits MLNode. Raise the soft limit to the hard one.
ulimit -n "$(ulimit -Hn)" 2>/dev/null || true

source /app/packages/api/.venv/bin/activate

exec "$@"
