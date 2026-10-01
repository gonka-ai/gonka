#!/usr/bin/env bash
# §8.3 proxy overlay hop completeness.
# Current versiond and versiond-router, peer RPC on, HTTP/2 via proxy:8443.
# Does not pin 0.2.15-v5 and does not change the no-proxy citest compose.
set -euo pipefail
cd "$(dirname "$0")/.."
exec env -u TESTENV_VERSIOND_IMAGE -u TESTENV_VERSIOND_ROUTER_IMAGE make citest-peerrpc-overlay
