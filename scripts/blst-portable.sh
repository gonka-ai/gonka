#!/usr/bin/env bash
# Source before `make build-docker` (or export for child processes).
# On Apple Silicon (Darwin + hw.optional.arm64):
#   BLST_PORTABLE=1, DOCKER_PLATFORM=linux/arm64, DOCKER_GOOS=linux, DOCKER_GOARCH=arm64
# Uses sysctl so Rosetta/x86_64 shells on M-series still detect the host.
#
# Override: BLST_PORTABLE=0 ./local-test-net/stop-rebuild.sh
# Override: DOCKER_PLATFORM=linux/amd64 ./local-test-net/stop-rebuild.sh

_apple_silicon() {
  [[ "$(uname -s)" == "Darwin" && "$(sysctl -n hw.optional.arm64 2>/dev/null)" == "1" ]]
}

if [[ -z "${BLST_PORTABLE:-}" ]]; then
  if _apple_silicon; then
    export BLST_PORTABLE=1
  fi
fi

if [[ -z "${DOCKER_PLATFORM:-}" ]]; then
  if _apple_silicon; then
    export DOCKER_PLATFORM=linux/arm64
    export DOCKER_GOOS=linux
    export DOCKER_GOARCH=arm64
  else
    export DOCKER_PLATFORM=linux/amd64
    export DOCKER_GOOS=linux
    export DOCKER_GOARCH=amd64
  fi
fi

export DOCKER_GOOS="${DOCKER_GOOS:-linux}"
export DOCKER_GOARCH="${DOCKER_GOARCH:-amd64}"

if _apple_silicon && [[ "${DOCKER_PLATFORM}" == "linux/arm64" ]]; then
  echo "Apple Silicon: BLST_PORTABLE=${BLST_PORTABLE:-0}, DOCKER_PLATFORM=${DOCKER_PLATFORM} (native arm64 images)"
fi
