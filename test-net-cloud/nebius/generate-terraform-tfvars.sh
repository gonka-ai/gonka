#!/usr/bin/env bash
set -euo pipefail

# Generate terraform.auto.tfvars from .env so Terraform and scripts share one source of truth.

if [ ! -f ".env" ]; then
  echo "Missing .env. Create it from .env.example first." >&2
  exit 1
fi

set -a
# shellcheck disable=SC1091
source ./.env
set +a

cat > terraform.auto.tfvars <<EOF
ssh_private_key_path = "${SSH_KEY_PATH:-~/.ssh/id_ed25519_engenious}"
ssh_user             = "${SSH_USER:-decentai}"

public_domain        = "${PUBLIC_DOMAIN:-xj7-5.s.filfox.io}"
chain_id             = "${CHAIN_ID:-gonka-testnet-3}"
repo_branch          = "${REPO_BRANCH:-origin/testnet/main}"
deploy_dir           = "${DEPLOY_DIR:-/srv/dai}"
hf_home              = "${HF_HOME:-/srv/dai/cache/}"
keyring_password     = "${KEYRING_PASSWORD:-12345678}"
genesis_key_name     = "${GENESIS_KEY_NAME:-${KEY_NAME:-gonka-account-key}}"

genesis_internal_ip  = "${GENESIS_INTERNAL_IP:-172.18.114.113}"
join_1_internal_ip   = "${JOIN1_INTERNAL_IP:-172.18.114.125}"
join_2_internal_ip   = "${JOIN2_INTERNAL_IP:-172.18.114.105}"

genesis_ssh_port     = ${GENESIS_SSH_PORT:-18220}
join_1_ssh_port      = ${JOIN1_SSH_PORT:-18225}
join_2_ssh_port      = ${JOIN2_SSH_PORT:-18226}

genesis_p2p_port     = ${GENESIS_P2P_PORT:-19239}
genesis_api_port     = ${GENESIS_API_PORT:-19240}
join_1_p2p_port      = ${JOIN1_P2P_PORT:-19249}
join_1_api_port      = ${JOIN1_API_PORT:-19250}
join_2_p2p_port      = ${JOIN2_P2P_PORT:-19251}
join_2_api_port      = ${JOIN2_API_PORT:-19252}
genesis_internal_api_port = ${GENESIS_INTERNAL_API_PORT:-8000}
join_1_internal_api_port  = ${JOIN1_INTERNAL_API_PORT:-8000}
join_2_internal_api_port  = ${JOIN2_INTERNAL_API_PORT:-8000}
EOF

echo "Generated terraform.auto.tfvars from .env"
