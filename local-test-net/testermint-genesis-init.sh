#!/bin/sh
# Testermint genesis entrypoint. The stock init script is left unchanged.
# When prod-local/genesis/join_genesis_accounts.txt exists, this inserts an
# add-genesis-account pass immediately before the chain is finalized so join
# cold accounts exist while user-to-user transfers are still restricted.
set -e

src=/root/init-docker-genesis.sh
dst=/tmp/init-docker-genesis-testermint.sh
hook=/tmp/join-genesis-accounts.hook
marker='echo "Genesis file created"'

if [ ! -f "$src" ]; then
  echo "testermint genesis init: $src is missing" >&2
  exit 1
fi
if ! grep -F -q "$marker" "$src"; then
  echo "testermint genesis init: marker not found in $src" >&2
  exit 1
fi

cat > "$hook" <<'EOF'
JOIN_ACCOUNTS_FILE="$STATE_DIR/join_genesis_accounts.txt"
if [ -f "$JOIN_ACCOUNTS_FILE" ]; then
  echo "Adding join cold accounts from $JOIN_ACCOUNTS_FILE"
  while read -r addr amount; do
    [ -z "$addr" ] && continue
    case "$addr" in
      \#*) continue ;;
    esac
    echo "Adding join genesis account $addr $amount"
    $APP_NAME genesis add-genesis-account "$addr" "$amount" --keyring-backend "$KEYRING_BACKEND"
  done < "$JOIN_ACCOUNTS_FILE"
fi

EOF

awk '
  BEGIN {
    hook = ""
    while ((getline line < "/tmp/join-genesis-accounts.hook") > 0) {
      hook = hook line "\n"
    }
    close("/tmp/join-genesis-accounts.hook")
  }
  index($0, "echo \"Genesis file created\"") && !inserted {
    printf "%s", hook
    inserted = 1
  }
  { print }
' "$src" > "$dst"

exec sh "$dst"
