set -e

INSTANCE_NAME="node-join-1"
ZONE="us-central1-a"

echo "Getting genesis.json"
./get_genesis.sh

echo "Getting config.env"
# TODO: pass INSTANCE_NAME + ZONE arguments
./get_config.sh

cp genesis.json ../inference-chain/build/.

make -C ../. node-release-docker
make -C ../inference-chain docker-push-join

gcloud compute scp --zone "$ZONE" \
  config.env \
  node-config.json \
  ../launch_chain.sh \
  ../docker-compose-cloud-join.yml \
  "$INSTANCE_NAME":~/.
