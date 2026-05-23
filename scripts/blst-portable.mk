# Auto-enable portable BLST on Apple Silicon Macs.
# Uses sysctl (not uname -m) so Rosetta/x86_64 shells on M-series still enable portable BLST.
# Portable builds avoid SIGILL from BLST under Docker/QEMU on Mac.
# Override: make build-docker BLST_PORTABLE=0
ifeq ($(origin BLST_PORTABLE),undefined)
  _BLST_APPLE_SILICON := $(shell \
    if [ "$$(uname -s 2>/dev/null)" = Darwin ] && [ "$$(sysctl -n hw.optional.arm64 2>/dev/null)" = 1 ]; then \
      echo 1; \
    fi)
  ifeq ($(_BLST_APPLE_SILICON),1)
    BLST_PORTABLE := 1
  else
    BLST_PORTABLE := 0
  endif
endif

ifeq ($(BLST_PORTABLE),1)
  BLST_PORTABLE_CGO_CFLAGS := -D__BLST_PORTABLE__
  # warning (stderr) — not $(info), which pollutes $(shell make ...) / export captures
  $(warning --> BLST_PORTABLE=1 (Apple Silicon: portable BLST))
endif
