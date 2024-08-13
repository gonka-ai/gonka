# 1. Reset the state of the chain
rm -rf prod-sim
mkdir -p prod-sim/kms-alice
mkdir -p prod-sim/node-carol
mkdir -p prod-sim/sentry-alice
mkdir -p prod-sim/sentry-bob
mkdir -p prod-sim/val-alice
mkdir -p prod-sim/val-bob

IMAGE_NAME="minid"
APP_NAME="minid"
TMKMS_IMAGE_NAME="tmkms:0.12.2"
CHAIN_ID="prod-sim"

# 2. Init the chains, creating genesis state and config files
echo -e desk-alice'\n'desk-bob'\n'node-carol'\n'sentry-alice'\n'sentry-bob'\n'val-alice'\n'val-bob \
    | xargs -I {} \
    docker run --rm -i \
    -v $(pwd)/prod-sim/{}:/root/.$APP_NAME \
    "$IMAGE_NAME" \
    "$APP_NAME" init prod-sim # moniker is not chain id!

# 3. Rename the token
docker run --rm -it \
    -v $(pwd)/prod-sim/desk-alice:/root/.$APP_NAME \
    --entrypoint sed \
    "$IMAGE_NAME" \
    -i 's/"stake"/"icoin"/g' /root/.$APP_NAME/config/genesis.json

echo -e desk-alice'\n'desk-bob'\n'node-carol'\n'sentry-alice'\n'sentry-bob'\n'val-alice'\n'val-bob \
    | xargs -I {} \
    docker run --rm -i \
    -v $(pwd)/prod-sim/{}:/root/.$APP_NAME \
    --entrypoint sed \
    "$IMAGE_NAME" \
    -Ei 's/([0-9]+)stake/\1icoin/g' /root/.$APP_NAME/config/app.toml

echo -e desk-alice'\n'desk-bob'\n'node-carol'\n'sentry-alice'\n'sentry-bob'\n'val-alice'\n'val-bob \
    | xargs -I {} \
    docker run --rm -i \
    -v $(pwd)/prod-sim/{}:/root/.$APP_NAME \
    --entrypoint sed \
    "$IMAGE_NAME" \
    -Ei 's/^chain-id = .*$/chain-id = "prod-sim"/g' \
    /root/.$APP_NAME/config/client.toml

# 4. Create keys. I'm using "password" as my passphrase to keep things simple
# 4.1 Create keys for alice and bob
docker run --rm -it \
    -v $(pwd)/prod-sim/desk-alice:/root/.$APP_NAME \
    "$IMAGE_NAME" \
    $APP_NAME keys \
    --keyring-backend file --keyring-dir /root/.$APP_NAME/keys \
    add alice

docker run --rm -it \
    -v $(pwd)/prod-sim/desk-bob:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME keys \
    --keyring-backend file --keyring-dir /root/.$APP_NAME/keys \
    add bob

# 4.2.1 Create Alice consensus key
docker run --rm -it \
    -v $(pwd)/prod-sim/kms-alice:/root/tmkms \
    $TMKMS_IMAGE_NAME \
    init /root/tmkms

docker run --rm -i \
  -v $(pwd)/prod-sim/kms-alice:/root/tmkms \
  --entrypoint sed \
  $TMKMS_IMAGE_NAME \
  -Ei 's/^protocol_version = .*$/protocol_version = "v0.34"/g' \
  /root/tmkms/tmkms.toml

docker run --rm -i \
  -v $(pwd)/prod-sim/kms-alice:/root/tmkms \
  --entrypoint sed \
  $TMKMS_IMAGE_NAME \
  -Ei 's/path = "\/root\/tmkms\/secrets\/cosmoshub-3-consensus.key"/path = "\/root\/tmkms\/secrets\/val-alice-consensus.key"/g' \
  /root/tmkms/tmkms.toml

docker run --rm -i \
    -v $(pwd)/prod-sim/kms-alice:/root/tmkms \
    --entrypoint sed \
    $TMKMS_IMAGE_NAME \
    -Ei "s/cosmoshub-3/$CHAIN_ID/g" /root/tmkms/tmkms.toml

# 4.2.2
# BE CAREFUL!
# If your minid app logs something unconditionally of the command
#   check the generated pub_validator_key-val-alice.json file and remove the logs
docker run --rm -t \
    -v $(pwd)/prod-sim/val-alice:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME tendermint show-validator \
    | tr -d '\n' | tr -d '\r' \
    > prod-sim/desk-alice/config/pub_validator_key-val-alice.json

# Simulate removing the key from the validator node
cp prod-sim/val-alice/config/priv_validator_key.json \
  prod-sim/desk-alice/config/priv_validator_key-val-alice.json
mv prod-sim/val-alice/config/priv_validator_key.json \
  prod-sim/kms-alice/secrets/priv_validator_key-val-alice.json

# Import the key into the KMS
docker run --rm -i \
    -v $(pwd)/prod-sim/kms-alice:/root/tmkms \
    -w /root/tmkms \
    $TMKMS_IMAGE_NAME \
    softsign import secrets/priv_validator_key-val-alice.json \
    secrets/val-alice-consensus.key

# On start, val-alice may still recreate a missing private key file due to how defaults are handled in the code.
# To prevent that, you can instead copy it from sentry-alice where it has no value.
cp prod-sim/sentry-alice/config/priv_validator_key.json \
    prod-sim/val-alice/config/

# 5. Connect KMS
docker run --rm -i \
    -v $(pwd)/prod-sim/kms-alice:/root/tmkms \
    --entrypoint sed \
    $TMKMS_IMAGE_NAME \
    -Ei 's/^addr = "tcp:.*$/addr = "tcp:\/\/val-alice:26659"/g' /root/tmkms/tmkms.toml

docker run --rm -i \
  -v $(pwd)/prod-sim/val-alice:/root/.$APP_NAME \
  --entrypoint sed \
  $IMAGE_NAME \
  -Ei 's/priv_validator_laddr = ""/priv_validator_laddr = "tcp:\/\/0.0.0.0:26659"/g' \
  /root/.$APP_NAME/config/config.toml

docker run --rm -i \
  -v $(pwd)/prod-sim/val-alice:/root/.$APP_NAME \
  --entrypoint sed \
  $IMAGE_NAME \
  -Ei 's/^priv_validator_key_file/# priv_validator_key_file/g' \
  /root/.$APP_NAME/config/config.toml

docker run --rm -i \
  -v $(pwd)/prod-sim/val-alice:/root/.$APP_NAME \
  --entrypoint sed \
  $IMAGE_NAME \
  -Ei 's/^priv_validator_state_file/# priv_validator_state_file/g' \
  /root/.$APP_NAME/config/config.toml

# Create a dummy key so the code doesn't try to generate it again
cp prod-sim/sentry-alice/config/priv_validator_key.json \
    prod-sim/val-alice/config

# 6. Genesis
docker run --rm -i \
    -v $(pwd)/prod-sim/desk-alice:/root/.$APP_NAME \
    --entrypoint sed \
    $IMAGE_NAME \
    -Ei 's/"chain_id": ".*"/"chain_id": "prod-sim"/g' \
    /root/.$APP_NAME/config/genesis.json

ALICE=$(echo password | docker run --rm -i \
    -v $(pwd)/prod-sim/desk-alice:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME keys \
    --keyring-backend file --keyring-dir /root/.$APP_NAME/keys \
    show alice --address)

docker run --rm -it \
    -v $(pwd)/prod-sim/desk-alice:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME genesis add-genesis-account $ALICE 1000000000icoin

BOB=$(echo password | docker run --rm -i \
    -v $(pwd)/prod-sim/desk-bob:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME keys \
    --keyring-backend file --keyring-dir /root/.$APP_NAME/keys \
    show bob --address)

docker run --rm -it \
    -v $(pwd)/prod-sim/desk-bob:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME genesis add-genesis-account $BOB 500000000icoin

# cp Bob's key to his desktop since he won't be using the KMS
cp prod-sim/val-bob/config/priv_validator_key.json \
    prod-sim/desk-bob/config/priv_validator_key.json

echo password | docker run --rm -i \
    -v $(pwd)/prod-sim/desk-bob:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME genesis gentx bob 40000000icoin \
    --keyring-backend file --keyring-dir /root/.$APP_NAME/keys \
    --account-number 1 --sequence 0 \
    --chain-id $CHAIN_ID \
    --gas 1000000 \
    --gas-prices 0.1icoin

mv prod-sim/desk-bob/config/genesis.json \
    prod-sim/desk-alice/config/

echo password | docker run --rm -i \
    -v $(pwd)/prod-sim/desk-alice:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME genesis gentx alice 60000000icoin \
    --keyring-backend file --keyring-dir /root/.$APP_NAME/keys \
    --account-number 0 --sequence 0 \
    --pubkey $(cat prod-sim/desk-alice/config/pub_validator_key-val-alice.json) \
    --chain-id $CHAIN_ID \
    --gas 1000000 \
    --gas-prices 0.1icoin

cp prod-sim/desk-bob/config/gentx/gentx-* \
    prod-sim/desk-alice/config/gentx

docker run --rm -it \
    -v $(pwd)/prod-sim/desk-alice:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME genesis collect-gentxs

# validate genesis file
docker run --rm -it \
    -v $(pwd)/prod-sim/desk-alice:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME genesis validate-genesis

# Distribute genesis file
echo -e desk-bob'\n'node-carol'\n'sentry-alice'\n'sentry-bob'\n'val-alice'\n'val-bob \
    | xargs -I {} \
    cp prod-sim/desk-alice/config/genesis.json prod-sim/{}/config

# Get node's public key
docker run --rm -i \
    -v $(pwd)/prod-sim/val-alice:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME tendermint show-node-id
echo "958a249da610a4cd9538a2f008d49a5a618c6c30"

docker run --rm -i \
    -v $(pwd)/prod-sim/sentry-alice:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME tendermint show-node-id
echo "0e3a1e940631825ffbe1a3bbc58c69fbd749794e"

docker run --rm -i \
    -v $(pwd)/prod-sim/val-bob:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME tendermint show-node-id
echo "84c9cb6f0553d2533df9d1a8e80bf5043a44d83f"

docker run --rm -i \
    -v $(pwd)/prod-sim/sentry-bob:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME tendermint show-node-id
echo "d8e0c9abb3fc081beeb98dccd8f21a607eaf0830"

docker run --rm -i \
    -v $(pwd)/prod-sim/node-carol:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME tendermint show-node-id
echo "8065dba6b20969213b56e3ea913c5bd1fdedf7e3"

docker compose \
    --file docker-compose.yml \
    --project-name minid-prod up \
    --detach

docker compose \
    --project-name minid-prod down

docker run --rm -it \
    --network minid-prod_net-public \
    $IMAGE_NAME \
    $APP_NAME status \
    --node "tcp://node-carol:26656"

docker run --rm -it \
    --network minid-prod_net-public \
    $IMAGE_NAME \
    $APP_NAME query inference get-inference r1  \
    --node "tcp://node-carol:26657"

docker run --rm -it \
    --network minid-prod_net-public \
    $IMAGE_NAME \
    $APP_NAME query inference get-inference r1  \
    --node "tcp://sentry-alice:26657"

docker run --rm -it \
    --network minid-prod_net-public \
    $IMAGE_NAME \
    $APP_NAME itx r1  \
    --node "tcp://sentry-bob:26657"

docker run --rm -it \
    --network net-alice \
    -v $(pwd)/prod-sim/val-alice:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME itx r1

docker run --rm -it \
    --network minid-prod_net-alice \
    $IMAGE_NAME \
    $APP_NAME query inference get-inference r1

docker run --rm -it \
    --network minid-prod_net-public \
    -v $(pwd)/prod-sim/desk-alice:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME itx r1

# To make it workd I had to:
# 1. Change /desk-alice/config/client.toml keyring-backend = "file" (from "os")
# 2. Change /desk-alice/config/client.toml node = "tcp://node-carol:26657" (from "tcp://localhost:26657)
docker run --rm -it \
    --network minid-prod_net-public \
    -v $(pwd)/prod-sim/desk-alice:/root/.$APP_NAME \
    $IMAGE_NAME \
    $APP_NAME itx r2

# --Trying to reproduce--
docker run --rm -i \
    -v $(pwd)/prod-sim-2/dima:/root/.$APP_NAME \
    "$IMAGE_NAME" \
    "$APP_NAME" init prod-sim-2 \
    --chain-id prod-sim \
    --default-denom icoin

docker run --rm -i \
    -v $(pwd)/prod-sim-2/dima:/root/.$APP_NAME \
    "$IMAGE_NAME" \
    "$APP_NAME" config set client chain-id prod-sim

# Seems like the only things we need to change are:
# 1. chain_id and bond_denom in genesis.json (Achieved by --chain-id and --default-denom)
# 2. chain_id in client.toml (Achieved by config set client chain-id prod-sim)
# 3. Denom units in app.toml!! For example for settings like minimum-gas-prices. Try both stake and mini!

# When configuring your server you may want to:
# 1. Change p2p.laddr (usual port is 26656) in config.toml
# 2. Change rpc.laddr (usual port is 26657) in config.toml

# When you have a js faucet
# 1. Change enable-unsafe-cors in app.toml
# 2. Change enabled-unsafe-cors in app.toml
# 3. Change cors_allowed_origins in config.toml

# When creating our custom docker image what needs to be done:
# 1. Build app
# 2. Run init
# 3. Copy genesis.json into the config folder
# 4. Change denom in app.toml (see min-gas for example)
# 5. Run minid config set client chain-id <your-chain-id>
# 6. Run minid config set client keyring-backend file
# 7. Change seeds in config.toml
# 8. Create a key
# 9. Set custom.account-id in app.toml to the created key id
