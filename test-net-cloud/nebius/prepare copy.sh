set -euo pipefail

if [ -n "${SSH_KEY_PATH:-}" ]; then
  SSH_KEY_ARG=(-i "$SSH_KEY_PATH")
else
  SSH_KEY_ARG=()
fi

# scp $SSH_KEY_ARG launch.py genesis-overrides.json ubuntu@89.169.111.79:/srv/dai/
# scp $SSH_KEY_ARG launch.py join-1.sh ubuntu@89.169.110.61:/srv/dai/
# scp $SSH_KEY_ARG launch.py join-2.sh ubuntu@89.169.110.250:/srv/dai/

GENESIS_SSH="decentai@xj7-5.s.filfox.io"
GENESIS_SSH_PORT="18220"
JOIN_1_SSH="decentai@xj7-5.s.filfox.io"
JOIN_1_SSH_PORT="18225"
JOIN_2_SSH="decentai@xj7-5.s.filfox.io"
JOIN_2_SSH_PORT="18226"
REMOTE_DIR="/srv/dai"
REMOTE_TMP_DIR="/tmp/gonka-prepare"

ensure_remote_dir_writable() {
  local host="$1"
  local port="$2"

  echo "==> Ensuring $REMOTE_DIR exists and is writable on $host:$port"

  # Create dir (best effort) and then assert it is writable.
  ssh "${SSH_KEY_ARG[@]}" -p "$port" "$host" "sudo mkdir -p '$REMOTE_DIR' || true"

  if ssh "${SSH_KEY_ARG[@]}" -p "$port" "$host" "test -w '$REMOTE_DIR'"; then
    echo "    OK: $REMOTE_DIR is writable"
    return 0
  fi

  echo "    $REMOTE_DIR is not writable. Attempting sudo chown to current user..."
  ssh "${SSH_KEY_ARG[@]}" -p "$port" "$host" "sudo chown \"\$(id -un)\":\"\$(id -gn)\" '$REMOTE_DIR' && sudo chmod u+rwx '$REMOTE_DIR'"

  if ! ssh "${SSH_KEY_ARG[@]}" -p "$port" "$host" "test -w '$REMOTE_DIR'"; then
    echo "ERROR: Still cannot write to $REMOTE_DIR on $host:$port. Check sudo rights and ownership." >&2
    ssh "${SSH_KEY_ARG[@]}" -p "$port" "$host" "ls -ld '$REMOTE_DIR' && id" || true
    exit 1
  fi
}

ensure_remote_dir_writable "$GENESIS_SSH" "$GENESIS_SSH_PORT"
ensure_remote_dir_writable "$JOIN_1_SSH" "$JOIN_1_SSH_PORT"
ensure_remote_dir_writable "$JOIN_2_SSH" "$JOIN_2_SSH_PORT"

ensure_remote_tmp_dir() {
  local host="$1"
  local port="$2"
  ssh "${SSH_KEY_ARG[@]}" -p "$port" "$host" "rm -rf '$REMOTE_TMP_DIR' && mkdir -p '$REMOTE_TMP_DIR'"
}

install_remote_files() {
  local host="$1"
  local port="$2"
  shift 2
  local files=("$@")

  echo "==> Uploading to $host:$port ($REMOTE_TMP_DIR) then installing to $REMOTE_DIR"
  ensure_remote_tmp_dir "$host" "$port"

  scp "${SSH_KEY_ARG[@]}" -P "$port" "${files[@]}" "$host":"$REMOTE_TMP_DIR"/

  # Move into place with sudo (handles existing root-owned files)
  local mv_cmd="set -e; sudo mkdir -p '$REMOTE_DIR';"
  for f in "${files[@]}"; do
    base="$(basename "$f")"
    mv_cmd="$mv_cmd sudo rm -f '$REMOTE_DIR/$base'; sudo mv '$REMOTE_TMP_DIR/$base' '$REMOTE_DIR/$base'; sudo chown \"\$(id -un)\":\"\$(id -gn)\" '$REMOTE_DIR/$base';"
  done
  mv_cmd="$mv_cmd rm -rf '$REMOTE_TMP_DIR'"

  ssh "${SSH_KEY_ARG[@]}" -p "$port" "$host" "$mv_cmd"
}

echo "==> Uploading files"
install_remote_files "$GENESIS_SSH" "$GENESIS_SSH_PORT" launch.py genesis-overrides.json
install_remote_files "$JOIN_1_SSH" "$JOIN_1_SSH_PORT" launch.py join-1.sh
install_remote_files "$JOIN_2_SSH" "$JOIN_2_SSH_PORT" launch.py join-2.sh

echo "==> Done"
