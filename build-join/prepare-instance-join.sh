set -e

INSTANCE_NAME="node-join-1"
ZONE="us-central1-a"

echo "Getting genesis.json"
./get_genesis.sh

echo "Getting config.env"
# TODO: pass INSTANCE_NAME + ZONE arguments
./get_config.sh

gcloud compute scp --zone "$ZONE" \
  config.env \
  ../launch_chain.sh \
  ../docker-compose-cloud-join.yml \
  ../node-config.json \
  "$INSTANCE_NAME":~/.
