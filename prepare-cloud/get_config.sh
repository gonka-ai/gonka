rm config.env
echo "export KEY_NAME=alice" >> config.env
echo 'export NODE_CONFIG=node-config.json' >> config.env

GENESIS_IP=$(gcloud compute instances describe node-genesis --zone=us-central1-a \
  --format="get(networkInterfaces[0].accessConfigs[0].natIP)")
echo "External IP: $GENESIS_IP"

echo "export ADD_ENDPOINT=http://$GENESIS_IP:8080" >> config.env

# TODO: get node address
NODE_ID=$(gcloud compute ssh --zone us-central1-a --quiet --tunnel-through-iap node-genesis \
  -- 'docker run -i --rm -v $HOME/.inference:/root/.inference \
  gcr.io/decentralized-ai/inferenced-genesis inferenced tendermint show-node-id' 2>/dev/null | tr -d '\r\n')

echo "NODE_ID=$NODE_ID"
echo "SEEDS=$NODE_ID@$GENESIS_IP:26656"
echo "export SEEDS=$NODE_ID@$GENESIS_IP:26656" >> config.env

# Configure my address
NODE_JOIN_IP=$(gcloud compute instances describe node-join-1 --zone=us-central1-a \
  --format="get(networkInterfaces[0].accessConfigs[0].natIP)")
echo "export PORT=8080" >> config.env
echo "export PUBLIC_URL=http://$NODE_JOIN_IP:8080" >> config.env
