#!/bin/sh
set -e

PROJECT_NAME="community_sale"
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"

echo "🔨 Building $PROJECT_NAME contract..."

# Clean previous build artifacts
rm -rf artifacts/ && mkdir -p artifacts/

# Build optimized WASM using cosmwasm rust-optimizer
docker run --rm \
    -v "$SCRIPT_DIR":/code \
    --mount type=volume,source="${PROJECT_NAME}_cache",target=/code/target \
    --mount type=volume,source=registry_cache,target=/usr/local/cargo/registry \
    cosmwasm/optimizer:0.16.1

echo "✅ Build complete: artifacts/${PROJECT_NAME}.wasm" 