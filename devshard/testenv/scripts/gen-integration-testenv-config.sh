#!/usr/bin/env bash
# Generate an isolated citest workspace (config + compose) for manual integration runs.
# Usage: OUT_DIR=$(./scripts/gen-integration-testenv-config.sh) && cd "$OUT_DIR" && docker compose up -d
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTENV_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
OUT_DIR="${1:-$(mktemp -d -t testenv-citest-XXXXXX)}"

mkdir -p "$OUT_DIR"
cat >"$OUT_DIR/config.yaml" <<'EOF'
chain_id: gonka-test
block_height: 150
epoch:
  index: 1
  poc_start_block_height: 100
  epoch_length: 400
params:
  devshard_requests_enabled: true
mock_chain:
  grpc_port: 19090
  rpc_port: 26667
  testenv_port: 19191
mock_dapi:
  grpc_port: 19400
  http_port: 19100
mock_openai:
  http_port: 18088
versiond:
  mode: multi
  version_name: v2
  binary_version: 0.2.13-v2-r2
versiond_router:
  port: 18080
devshardctl:
  port: 18081
postgres:
  enabled: true
network:
  subnet: 172.31.0.0/24
  base_ip: 172.31.0
escrow:
  slots: 2
hosts:
  - id: versiond-0
    private_key_hex: TODO
  - id: versiond-1
    private_key_hex: TODO
user:
  private_key_hex: TODO
warm_grantee:
  private_key_hex: TODO
escrows:
  - id: 1
    model_id: test-model
grantees:
  - granter_address: ""
    message_type_url: /inference.inference.MsgStartInference
    grantees: [""]
EOF

(
  cd "$OUT_DIR"
  go run "$TESTENV_DIR/cmd/gencompose" -config config.yaml
)

echo "$OUT_DIR"
