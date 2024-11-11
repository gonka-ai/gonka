INSTANCE_NAME="node-genesis"
ZONE="us-central1-a"

gcloud compute scp --zone "$ZONE" config-genesis.env "$INSTANCE_NAME":~/config.env
gcloud compute scp --zone "$ZONE" \
  node-config.json \
  ../launch_chain_genesis.sh \
  ../docker-compose-cloud-genesis.yml \
  ../docker-compose-cloud-restart.yml \
  "$INSTANCE_NAME":~/.
