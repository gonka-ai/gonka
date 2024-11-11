if [ -z "$ZONE" ]; then
  echo "Defaulting ZONE to us-central1-a"
  ZONE="us-central1-a"
fi

if [ -z "$INSTANCE_NAME_GENESIS" ]; then
  echo "Defaulting INSTANCE_NAME_GENESIS to node-genesis"
  INSTANCE_NAME_GENESIS="node-genesis"
fi

if [ -z "$INSTANCE_NAME_JOIN" ]; then
  echo "Defaulting INSTANCE_NAME_JOIN to node-join-1"
  INSTANCE_NAME_JOIN="node-join-1"
fi

rm config.env
echo "export KEY_NAME=alice" >> config.env
echo 'export NODE_CONFIG=node-config.json' >> config.env

GENESIS_IP=$(gcloud compute instances describe "$INSTANCE_NAME_GENESIS" --zone="$ZONE" \
  --format="get(networkInterfaces[0].accessConfigs[0].natIP)")
echo "External IP: $GENESIS_IP"

echo "export SEED_IP=$GENESIS_IP" >> config.env

# Configure my address
NODE_JOIN_IP=$(gcloud compute instances describe "$INSTANCE_NAME_JOIN" --zone=us-central1-a \
  --format="get(networkInterfaces[0].accessConfigs[0].natIP)")
echo "export PORT=8080" >> config.env
echo "export PUBLIC_IP=$NODE_JOIN_IP" >> config.env
