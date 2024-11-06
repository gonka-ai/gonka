rm config.env
echo "export KEY_NAME=alice" >> config.env
echo 'export NODE_CONFIG=node-config.json' >> config.env

GENESIS_IP=$(gcloud compute instances describe node-genesis --zone=us-central1-a \
  --format="get(networkInterfaces[0].accessConfigs[0].natIP)")
echo "External IP: $GENESIS_IP"

echo "export SEED_IP=$GENESIS_IP" >> config.env

# Configure my address
NODE_JOIN_IP=$(gcloud compute instances describe node-join-1 --zone=us-central1-a \
  --format="get(networkInterfaces[0].accessConfigs[0].natIP)")
echo "export PORT=8080" >> config.env
echo "export PUBLIC_URL=http://$NODE_JOIN_IP:8080" >> config.env
