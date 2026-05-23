#!/usr/bin/env bash
# Source before `make build-docker` (or export for child processes).
# Sets BLST_PORTABLE=1 on Apple Silicon unless already set in the environment.
# Uses sysctl so Rosetta/x86_64 shells on M-series still enable portable BLST.
# Override: BLST_PORTABLE=0 ./local-test-net/stop-rebuild.sh

if [[ -z "${BLST_PORTABLE:-}" ]]; then
  if [[ "$(uname -s)" == "Darwin" && "$(sysctl -n hw.optional.arm64 2>/dev/null)" == "1" ]]; then
    export BLST_PORTABLE=1
  fi
fi
