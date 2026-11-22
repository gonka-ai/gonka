# BLST_PORTABLE=1 by default on every host: blst checks the CPU at startup and keeps the
# ADX/MULX code path where it exists, so binaries and images also start on x86-64 CPUs
# without ADX (pre-Broadwell Intel, pre-Zen AMD, VMs with generic CPU models) and under
# Docker on Apple Silicon. Non-portable blst exits there with
# "Caught SIGILL in blst_cgo_init".
#
# Apple Silicon auto-detection (Darwin + hw.optional.arm64 via sysctl, not uname -m).
# Rosetta/x86_64 shells on M-series Macs still match the host.
#
# When detected:
#   DOCKER_PLATFORM=linux/arm64 — native arm64 images (CometBFT P2P on Docker Desktop)
#
# Override: make build-docker BLST_PORTABLE=0
# Override: make build-docker DOCKER_PLATFORM=linux/amd64
_APPLE_SILICON := $(shell \
  if [ "$$(uname -s 2>/dev/null)" = Darwin ] && [ "$$(sysctl -n hw.optional.arm64 2>/dev/null)" = 1 ]; then \
    echo 1; \
  fi)

ifeq ($(origin BLST_PORTABLE),undefined)
  BLST_PORTABLE := 1
endif

ifeq ($(origin DOCKER_PLATFORM),undefined)
  ifeq ($(_APPLE_SILICON),1)
    DOCKER_PLATFORM := linux/arm64
    DOCKER_GOOS := linux
    DOCKER_GOARCH := arm64
  else
    DOCKER_PLATFORM := linux/amd64
    DOCKER_GOOS := linux
    DOCKER_GOARCH := amd64
  endif
endif

ifeq ($(BLST_PORTABLE),1)
  BLST_PORTABLE_CGO_CFLAGS := -D__BLST_PORTABLE__
endif

ifeq ($(_APPLE_SILICON),1)
  ifeq ($(DOCKER_PLATFORM),linux/arm64)
    $(warning --> DOCKER_PLATFORM=linux/arm64 (Apple Silicon: native arm64 Docker images))
  endif
endif
