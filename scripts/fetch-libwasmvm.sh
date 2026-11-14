#!/bin/sh
set -eu

LIBWASMVM_AARCH64_URL=https://github.com/gonka-ai/wasmvm/releases/download/v2.2.10-gonka.1/libwasmvm_muslc.aarch64.a
LIBWASMVM_AARCH64_SHA256=07dfa9bcd82939ce73f649fc7904271909d23d9b8f26ab84badc93fa3d6a8690
LIBWASMVM_X64_URL=https://github.com/gonka-ai/wasmvm/releases/download/v2.2.10-gonka.1/libwasmvm_muslc.x86_64.a
LIBWASMVM_X64_SHA256=2939be2ccee110b796491233b546e75d145ea91efffbe4008b8b72bfe7ad105f
LIBWASMVM_DARWIN_URL=https://github.com/gonka-ai/wasmvm/releases/download/v2.2.10-gonka.1/libwasmvmstatic_darwin.a

if [ "${1:-}" = "print" ]; then
  case "${2:-}" in
    LIBWASMVM_AARCH64_URL) printf '%s\n' "$LIBWASMVM_AARCH64_URL" ;;
    LIBWASMVM_AARCH64_SHA256) printf '%s\n' "$LIBWASMVM_AARCH64_SHA256" ;;
    LIBWASMVM_X64_URL) printf '%s\n' "$LIBWASMVM_X64_URL" ;;
    LIBWASMVM_X64_SHA256) printf '%s\n' "$LIBWASMVM_X64_SHA256" ;;
    LIBWASMVM_DARWIN_URL) printf '%s\n' "$LIBWASMVM_DARWIN_URL" ;;
    *) echo "unknown libwasmvm pin: ${2:-}" >&2; exit 1 ;;
  esac
  exit 0
fi

if [ "${GOARCH:-}" = "arm64" ]; then
  wget -O /lib/libwasmvm_muslc.aarch64.a "$LIBWASMVM_AARCH64_URL"
  echo "$LIBWASMVM_AARCH64_SHA256 /lib/libwasmvm_muslc.aarch64.a" | sha256sum -c
  ln -sf /lib/libwasmvm_muslc.aarch64.a /lib/libwasmvm_muslc.a
else
  wget -O /lib/libwasmvm_muslc.x86_64.a "$LIBWASMVM_X64_URL"
  echo "$LIBWASMVM_X64_SHA256 /lib/libwasmvm_muslc.x86_64.a" | sha256sum -c
  ln -sf /lib/libwasmvm_muslc.x86_64.a /lib/libwasmvm_muslc.a
fi
