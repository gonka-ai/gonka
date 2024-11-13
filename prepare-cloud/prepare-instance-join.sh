set -e

INSTANCE_NAME="node-join-1"
ZONE="us-central1-a"

echo "Getting config.env"
# TODO: pass INSTANCE_NAME + ZONE arguments
./get_config.sh

make -C ../. node-release-docker
make -C ../inference-chain docker-push-join

gcloud compute scp --zone "$ZONE" \
  config.env \
  node-config.json \
  ../launch_chain.sh \
  ../docker-compose-cloud-join.yml \
  ../docker-compose-cloud-restart.yml \
  "$INSTANCE_NAME":~/.
