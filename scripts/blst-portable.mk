# Apple Silicon auto-detection (Darwin + hw.optional.arm64 via sysctl, not uname -m).
# Rosetta/x86_64 shells on M-series Macs still match the host.
#
# When detected:
#   BLST_PORTABLE=1     — portable BLST (avoids SIGILL under Docker on Mac)
#   DOCKER_PLATFORM=linux/arm64 — native arm64 images (CometBFT P2P on Docker Desktop)
#
# Override: make build-docker BLST_PORTABLE=0
# Override: make build-docker DOCKER_PLATFORM=linux/amd64
_APPLE_SILICON := $(shell \
  if [ "$$(uname -s 2>/dev/null)" = Darwin ] && [ "$$(sysctl -n hw.optional.arm64 2>/dev/null)" = 1 ]; then \
    echo 1; \
  fi)

ifeq ($(origin BLST_PORTABLE),undefined)
  ifeq ($(_APPLE_SILICON),1)
    BLST_PORTABLE := 1
  else
    BLST_PORTABLE := 0
  endif
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
  # warning (stderr) — not $(info), which pollutes $(shell make ...) / export captures
  $(warning --> BLST_PORTABLE=1 (Apple Silicon: portable BLST))
endif

ifeq ($(_APPLE_SILICON),1)
  ifeq ($(DOCKER_PLATFORM),linux/arm64)
    $(warning --> DOCKER_PLATFORM=linux/arm64 (Apple Silicon: native arm64 Docker images))
  endif
endif
