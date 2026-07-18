#!/usr/bin/env bash
# Run numbered stack citests and versiond rolling-update tests.
set -euo pipefail
cd "$(dirname "$0")/.."
export TESTENV_CITEST=1
make citest-stack
