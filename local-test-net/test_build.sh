#!/bin/sh
set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Don't need to make path relative to ./local-test-net, because make is run with root as workdir
export GENESIS_OVERRIDES_FILE="inference-chain/test_genesis_overrides.json"
# shellcheck source=../scripts/blst-portable.sh
. "${REPO_ROOT}/scripts/blst-portable.sh"

make -C "${REPO_ROOT}" build-docker
