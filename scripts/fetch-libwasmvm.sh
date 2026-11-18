#!/bin/sh
set -eu

LIBWASMVM_AARCH64_URL=https://github.com/gonka-ai/wasmvm/releases/download/v3.0.9-gonka.1/libwasmvm_muslc.aarch64.a
LIBWASMVM_AARCH64_SHA256=806f972d81079fe2c7d9b66d7142a04f03dfc5f6e47ffccc39b9e608afbdc507
LIBWASMVM_X64_URL=https://github.com/gonka-ai/wasmvm/releases/download/v3.0.9-gonka.1/libwasmvm_muslc.x86_64.a
LIBWASMVM_X64_SHA256=4ba7e08e01594949ad20251905ccb02f96231931b6ce61af54858ce89db9c842
LIBWASMVM_DARWIN_URL=https://github.com/gonka-ai/wasmvm/releases/download/v3.0.9-gonka.1/libwasmvmstatic_darwin.a
LIBWASMVM_DARWIN_SHA256=a08c5d234c0f19f98d403c30613ac3feccb1618c66329fe1928489c012dd6aa7

if [ "${1:-}" = "print" ]; then
  case "${2:-}" in
    LIBWASMVM_AARCH64_URL) printf '%s\n' "$LIBWASMVM_AARCH64_URL" ;;
    LIBWASMVM_AARCH64_SHA256) printf '%s\n' "$LIBWASMVM_AARCH64_SHA256" ;;
    LIBWASMVM_X64_URL) printf '%s\n' "$LIBWASMVM_X64_URL" ;;
    LIBWASMVM_X64_SHA256) printf '%s\n' "$LIBWASMVM_X64_SHA256" ;;
    LIBWASMVM_DARWIN_URL) printf '%s\n' "$LIBWASMVM_DARWIN_URL" ;;
    LIBWASMVM_DARWIN_SHA256) printf '%s\n' "$LIBWASMVM_DARWIN_SHA256" ;;
    *) echo "unknown libwasmvm pin: ${2:-}" >&2; exit 1 ;;
  esac
  exit 0
fi

if [ "${GOARCH:-}" = "arm64" ]; then
  wget -O /lib/libwasmvm_muslc.aarch64.a "$LIBWASMVM_AARCH64_URL"
  echo "$LIBWASMVM_AARCH64_SHA256 /lib/libwasmvm_muslc.aarch64.a" | sha256sum -c
  ln -sf /lib/libwasmvm_muslc.aarch64.a /lib/libwasmvm_muslc.a
  rm -f "/go/pkg/mod/github.com/!cosm!wasm/wasmvm/v3@v3.0.9/internal/api/libwasmvm.aarch64.so"
else
  wget -O /lib/libwasmvm_muslc.x86_64.a "$LIBWASMVM_X64_URL"
  echo "$LIBWASMVM_X64_SHA256 /lib/libwasmvm_muslc.x86_64.a" | sha256sum -c
  ln -sf /lib/libwasmvm_muslc.x86_64.a /lib/libwasmvm_muslc.a
  rm -f "/go/pkg/mod/github.com/!cosm!wasm/wasmvm/v3@v3.0.9/internal/api/libwasmvm.x86_64.so"
fi
