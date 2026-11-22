#!/bin/bash
set -e

PROJECT_NAME="reward_splitter"
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
cd "$SCRIPT_DIR"

echo "🔨 Building $PROJECT_NAME contract..."

# Clean previous build artifacts
rm -rf artifacts/ && mkdir -p artifacts/

# Build optimized WASM using the cosmwasm optimizer.
#
# The pinned toolchain is not a convenience: a modern local Rust enables
# post-MVP wasm features for wasm32-unknown-unknown, and wasmvm's static
# validation rejects the result at `tx wasm store` with
#   "bulk memory support is not enabled"
# RUSTFLAGS="-C target-cpu=mvp" does not help — those opcodes come from the
# precompiled core/alloc, not from this crate.
#
# Note the image is amd64. cosmwasm/optimizer-arm64 produces different bytes,
# and therefore a different checksum, from identical input.
docker run --rm \
    -v "$SCRIPT_DIR":/code \
    --mount type=volume,source="${PROJECT_NAME}_cache",target=/code/target \
    --mount type=volume,source=registry_cache,target=/usr/local/cargo/registry \
    cosmwasm/optimizer:0.16.1

echo "✅ Build complete: artifacts/${PROJECT_NAME}.wasm"
sha256sum "artifacts/${PROJECT_NAME}.wasm" 2>/dev/null || shasum -a 256 "artifacts/${PROJECT_NAME}.wasm"
