set -e

export INSTANCE_NAME_GENESIS="node-genesis"
export INSTANCE_NAME_JOIN="node-join-1"
export ZONE="us-central1-a"

echo "Getting config.env"
./get_config.sh

gcloud compute scp --zone "$ZONE" config-genesis.env "$INSTANCE_NAME_GENESIS":~/config.env
gcloud compute scp --zone "$ZONE" \
  node-config.json \
  ../launch_chain_genesis.sh \
  ../relaunch_chain.sh \
  ../docker-compose-cloud-genesis.yml \
  ../docker-compose-cloud-restart.yml \
  "$INSTANCE_NAME_GENESIS":~/.

gcloud compute scp --zone "$ZONE" \
  config.env \
  node-config.json \
  ../launch_chain.sh \
  ../relaunch_chain.sh \
  ../docker-compose-cloud-join.yml \
  ../docker-compose-cloud-restart.yml \
  "$INSTANCE_NAME_JOIN":~/.
