#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

"${SCRIPT_DIR}/stop.sh"

# This path is consumed by the Docker build context, so it must stay repo-relative.
export GENESIS_OVERRIDES_FILE="inference-chain/test_genesis_overrides.json"
# shellcheck source=../scripts/blst-portable.sh
# Apple Silicon: BLST_PORTABLE + DOCKER_PLATFORM=linux/arm64 (see scripts/blst-portable.sh)
source "${REPO_ROOT}/scripts/blst-portable.sh"
export SET_LATEST=1
# Align with root Makefile DEVSHARD_VERSION (same value Testermint uses when env is unset).
export DEVSHARD_VERSION="${DEVSHARD_VERSION:-$(make -C "${REPO_ROOT}" -s --no-print-directory print-devshard-version 2>/dev/null)}"
make -C "${REPO_ROOT}" build-docker

make -C "${REPO_ROOT}" versiond-build-docker devshardd-build
