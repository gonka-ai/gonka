#!/usr/bin/env bash
# §9 R1–R8. Overlay half uses current images. The no-proxy half pins 0.2.15-v5.
set -euo pipefail
cd "$(dirname "$0")/.."
exec env -u TESTENV_VERSIOND_IMAGE -u TESTENV_VERSIOND_ROUTER_IMAGE make citest-peerrpc-limits
