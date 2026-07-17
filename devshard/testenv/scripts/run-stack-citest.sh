#!/usr/bin/env bash
# Run stack citest (S1–S9) from devshard/testenv.
set -euo pipefail
cd "$(dirname "$0")/.."
export TESTENV_CITEST=1
make citest-stack
