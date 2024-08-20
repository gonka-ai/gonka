APP_NAME="minid"
CHAIN_ID="prod-sim"
COIN_DENOM="icoin"
STATE_DIR="/root/.$APP_NAME"

KEY_NAME=$1
if [ -z "$KEY_NAME" ]; then
  echo "Usage: $0 <key-name>. The key name is the name of your account key to sign transactions."
  exit 1
fi

echo "Current directory: $(pwd)"

# Init the chain:
# I'm using prod-sim as the chain name (production simulation)
#   and icoin (intelligence coin) as the default denomination
#   and my-node as a node moniker (it doesn't have to be unique)
$APP_NAME init \
  --chain-id $CHAIN_ID \
  --default-denom $COIN_DENOM \
  my-node

$APP_NAME config set client chain-id $CHAIN_ID
$APP_NAME config set client keyring-backend file
$APP_NAME config set app minimum-gas-prices "0$COIN_DENOM"
sed -Ei 's/^laddr = ".*:26657"$/laddr = "tcp:\/\/0\.0\.0\.0:26657"/g' \
  $STATE_DIR/config/config.toml
sed -Ei 's/^seeds = .*$/seeds = "958a249da610a4cd9538a2f008d49a5a618c6c30@35.232.251.227:26656,84c9cb6f0553d2533df9d1a8e80bf5043a44d83f@34.172.126.50:26656"/g' \
  $STATE_DIR/config/config.toml
cp /root/genesis.json $STATE_DIR/config/genesis.json

# Create your account
$APP_NAME keys \
    --keyring-backend file --keyring-dir "$STATE_DIR" \
    add "$KEY_NAME"

cat <<EOL >> $STATE_DIR/config/app.toml
[custom]
account-id = "$KEY_NAME"
EOL

echo "To complete your setup, you need to ask someone to send you some coins. You can find your address above: \"mini...\""
