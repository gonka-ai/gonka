#!/usr/bin/env bash
# §8.4 JSON / Connect HTTP/2 / native gRPC parity on one proxy overlay.
# Current versiond and versiond-router. Does not pin 0.2.15-v5.
set -euo pipefail
cd "$(dirname "$0")/.."
exec env -u TESTENV_VERSIOND_IMAGE -u TESTENV_VERSIOND_ROUTER_IMAGE make citest-peerrpc-parity
