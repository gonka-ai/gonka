#!/usr/bin/env bash
set -euo pipefail

if [ ! -f ".env" ]; then
  echo "Missing .env. Create it from .env.example first." >&2
  exit 1
fi

set -a
# shellcheck disable=SC1091
source ./.env
set +a

SSH_KEY_PATH="${SSH_KEY_PATH:-$HOME/.ssh/id_ed25519_engenious}"
SSH_USER="${SSH_USER:-decentai}"
PUBLIC_DOMAIN="${PUBLIC_DOMAIN:-xj7-5.s.filfox.io}"
DEPLOY_DIR="${DEPLOY_DIR:-/srv/dai}"

GENESIS_SSH_PORT="${GENESIS_SSH_PORT:-18220}"
JOIN1_SSH_PORT="${JOIN1_SSH_PORT:-18225}"
JOIN2_SSH_PORT="${JOIN2_SSH_PORT:-18226}"

SSH_COMMON=(-i "$SSH_KEY_PATH" -o StrictHostKeyChecking=accept-new -o BatchMode=yes)
REMOTE="${SSH_USER}@${PUBLIC_DOMAIN}"
MODE="${1:-safe}"

run_safe_down() {
  local name="$1"
  local port="$2"
  echo "==> [$name] stopping stack"
  ssh "${SSH_COMMON[@]}" -p "$port" "$REMOTE" \
    "cd '$DEPLOY_DIR/gonka/deploy/join' 2>/dev/null && docker compose down --remove-orphans || true" || \
    echo "  [warn] could not reach $name (server may be down), skipping"
}

run_full_cleanup() {
  local name="$1"
  local port="$2"
  echo "==> [$name] full cleanup"
  ssh "${SSH_COMMON[@]}" -p "$port" "$REMOTE" "
    set -e
    cd '$DEPLOY_DIR/gonka/deploy/join' 2>/dev/null || true
    docker compose down -v --rmi all || true
    docker system prune -a -f --volumes || true
    docker rm -f join-mlnode-308-1 join-inference-1 || true
    docker network rm join_default || true
    cd '$DEPLOY_DIR'
    sudo rm -rf -- *
  "
}

case "$MODE" in
  safe)
    run_safe_down "genesis" "$GENESIS_SSH_PORT"
    run_safe_down "join-1" "$JOIN1_SSH_PORT"
    run_safe_down "join-2" "$JOIN2_SSH_PORT"
    echo "All node stacks stopped (safe mode)."
    ;;
  nuke)
    echo "Running DESTRUCTIVE cleanup mode (Docker prune + wipe $DEPLOY_DIR)."
    run_full_cleanup "genesis" "$GENESIS_SSH_PORT"
    run_full_cleanup "join-1" "$JOIN1_SSH_PORT"
    run_full_cleanup "join-2" "$JOIN2_SSH_PORT"
    echo "Full cleanup complete on all nodes."
    ;;
  *)
    echo "Usage: $0 [safe|nuke]" >&2
    exit 1
    ;;
esac
