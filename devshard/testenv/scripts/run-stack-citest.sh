#!/usr/bin/env bash
# Run Phase 8 stack citest (S1–S4) from devshard/testenv.
set -euo pipefail
cd "$(dirname "$0")/.."
export TESTENV_CITEST=1
make citest-stack
