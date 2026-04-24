#!/usr/bin/env bash
set -euo pipefail

# Resolve paths from this script's directory so .env and scp sources work when
# invoked from another cwd (e.g. `bash test-net-cloud/nebius/prepare.sh`).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

if [ -f "$SCRIPT_DIR/.env" ]; then
  set -a
  # shellcheck disable=SC1091
  source "$SCRIPT_DIR/.env"
  set +a
fi

if [ -n "${SSH_KEY_PATH:-}" ]; then
  SSH_KEY_ARG=(-i "$SSH_KEY_PATH" -o ConnectTimeout=30 -o StrictHostKeyChecking=accept-new -o BatchMode=yes)
else
  SSH_KEY_ARG=(-o ConnectTimeout=30 -o StrictHostKeyChecking=accept-new -o BatchMode=yes)
fi

SSH_USER="${SSH_USER:-decentai}"
PUBLIC_DOMAIN="${PUBLIC_DOMAIN:-xj7-5.s.filfox.io}"

GENESIS_SSH="${SSH_USER}@${PUBLIC_DOMAIN}"
GENESIS_SSH_PORT="${GENESIS_SSH_PORT:-18220}"
JOIN_1_SSH="${SSH_USER}@${PUBLIC_DOMAIN}"
JOIN_1_SSH_PORT="${JOIN1_SSH_PORT:-${JOIN_1_SSH_PORT:-18225}}"
JOIN_2_SSH="${SSH_USER}@${PUBLIC_DOMAIN}"
JOIN_2_SSH_PORT="${JOIN2_SSH_PORT:-${JOIN_2_SSH_PORT:-18226}}"
REMOTE_DIR="${DEPLOY_DIR:-/srv/dai}"
REMOTE_TMP_DIR="/tmp/gonka-prepare"

# Run a block of commands against a node; warn and skip if unreachable.
try_node() {
  local name="$1"
  local port="$2"
  local host="$3"
  shift 3
  if "$@" 2>/dev/null; then
    return 0
  else
    echo "  [warn] could not reach $name on port $port, skipping" >&2
    return 0
  fi
}

ensure_remote_dir_writable() {
  local name="$1"
  local host="$2"
  local port="$3"

  echo "==> Ensuring $REMOTE_DIR exists and is writable on $host:$port"
  if ! ssh "${SSH_KEY_ARG[@]}" -p "$port" "$host" "sudo mkdir -p '$REMOTE_DIR' && test -w '$REMOTE_DIR'" 2>/dev/null; then
    ssh "${SSH_KEY_ARG[@]}" -p "$port" "$host" \
      "sudo chown \"\$(id -un)\":\"\$(id -gn)\" '$REMOTE_DIR' && sudo chmod u+rwx '$REMOTE_DIR'" 2>/dev/null || {
      echo "  [warn] $name ($port) unreachable or dir not writable, skipping" >&2
      return 0
    }
  fi
  echo "    OK: $REMOTE_DIR is writable"
}

ensure_remote_tmp_dir() {
  local host="$1"
  local port="$2"
  ssh "${SSH_KEY_ARG[@]}" -p "$port" "$host" "rm -rf '$REMOTE_TMP_DIR' && mkdir -p '$REMOTE_TMP_DIR'"
}

install_remote_files() {
  local name="$1"
  local host="$2"
  local port="$3"
  shift 3
  local files=("$@")

  echo "==> Uploading to $host:$port ($REMOTE_TMP_DIR) then installing to $REMOTE_DIR"
  ensure_remote_tmp_dir "$host" "$port" 2>/dev/null || { echo "  [warn] $name unreachable, skipping upload" >&2; return 0; }

  scp "${SSH_KEY_ARG[@]}" -P "$port" "${files[@]}" "$host":"$REMOTE_TMP_DIR"/ 2>/dev/null || { echo "  [warn] $name scp failed, skipping" >&2; return 0; }

  local mv_cmd="set -e; sudo mkdir -p '$REMOTE_DIR';"
  for f in "${files[@]}"; do
    base="$(basename "$f")"
    mv_cmd="$mv_cmd sudo rm -f '$REMOTE_DIR/$base'; sudo mv '$REMOTE_TMP_DIR/$base' '$REMOTE_DIR/$base'; sudo chown \"\$(id -un)\":\"\$(id -gn)\" '$REMOTE_DIR/$base';"
  done
  mv_cmd="$mv_cmd rm -rf '$REMOTE_TMP_DIR'"

  ssh "${SSH_KEY_ARG[@]}" -p "$port" "$host" "$mv_cmd" 2>/dev/null || { echo "  [warn] $name install failed" >&2; return 0; }
}

sync_artifacts_from_genesis() {
  local join_name="$1"
  local join_host="$2"
  local join_port="$3"

  echo "==> [$join_name] syncing artifacts from genesis"

  for binary in devshardd decentralized-api; do
    if ssh "${SSH_KEY_ARG[@]}" -p "$join_port" "$join_host" "test -f '$REMOTE_DIR/$binary'" 2>/dev/null; then
      echo "    $binary already present, skipping"
    else
      echo "    copying $binary"
      src_path=$(ssh "${SSH_KEY_ARG[@]}" -p "$GENESIS_SSH_PORT" "$GENESIS_SSH" \
        "test -f '$REMOTE_DIR/$binary' && echo '$REMOTE_DIR/$binary' || echo '$REMOTE_DIR/gonka/build/$binary'" 2>/dev/null) || { echo "  [warn] could not resolve $binary path on genesis"; continue; }
      ssh "${SSH_KEY_ARG[@]}" -p "$GENESIS_SSH_PORT" "$GENESIS_SSH" "cat '$src_path'" 2>/dev/null \
        | ssh "${SSH_KEY_ARG[@]}" -p "$join_port" "$join_host" \
            "cat > '$REMOTE_DIR/${binary}.tmp' && chmod +x '$REMOTE_DIR/${binary}.tmp' && mv '$REMOTE_DIR/${binary}.tmp' '$REMOTE_DIR/$binary'" 2>/dev/null \
        || echo "  [warn] could not copy $binary to $join_name"
    fi
  done

  if ssh "${SSH_KEY_ARG[@]}" -p "$join_port" "$join_host" \
      "docker image inspect ghcr.io/product-science/versiond:0.2.11 >/dev/null 2>&1" 2>/dev/null; then
    echo "    versiond image already present, skipping"
  else
    echo "    syncing versiond Docker image"
    ssh "${SSH_KEY_ARG[@]}" -p "$GENESIS_SSH_PORT" "$GENESIS_SSH" \
      "docker save ghcr.io/product-science/versiond:0.2.11 | gzip" 2>/dev/null \
      | ssh "${SSH_KEY_ARG[@]}" -p "$join_port" "$join_host" "docker load" 2>/dev/null \
      || echo "  [warn] could not sync versiond image to $join_name"
  fi
}

echo "==> Uploading files"
install_remote_files "genesis" "$GENESIS_SSH" "$GENESIS_SSH_PORT" "$SCRIPT_DIR/launch.py" "$SCRIPT_DIR/genesis-overrides.json" "$SCRIPT_DIR/.env"
install_remote_files "join-1"  "$JOIN_1_SSH"  "$JOIN_1_SSH_PORT"  "$SCRIPT_DIR/launch.py" "$SCRIPT_DIR/join-1.sh" "$SCRIPT_DIR/.env"
if [ "${SKIP_JOIN2:-0}" != "1" ]; then
  install_remote_files "join-2" "$JOIN_2_SSH" "$JOIN_2_SSH_PORT" "$SCRIPT_DIR/launch.py" "$SCRIPT_DIR/join-2.sh" "$SCRIPT_DIR/.env"
fi

echo "==> Syncing artifacts from genesis to join nodes"
sync_artifacts_from_genesis "join-1" "$JOIN_1_SSH" "$JOIN_1_SSH_PORT"
if [ "${SKIP_JOIN2:-0}" != "1" ]; then
  sync_artifacts_from_genesis "join-2" "$JOIN_2_SSH" "$JOIN_2_SSH_PORT"
fi

echo "==> Done"
