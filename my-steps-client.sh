# 1. [Optional] Build a node's docker image or use
make node-release-docker

# 2. Reset the local state
rm -rf ~/.inference
mkdir ~/.inference

IMAGE_NAME="inferenced-join"

# Create a request-payload.json file in $HOME/inference-requests/ directory and then mount it
# when running our docker image
docker run -it --rm \
 -v $HOME/.inference:/root/.inference \
 -v $HOME/inference-requests:/root/inference-requests \
  "$IMAGE_NAME" \
  sh

# 3. Create a key
inferenced keys add client-3

# 4. Add participant
curl -X POST http://34.72.225.168:8080/v1/participants \
-H "Content-Type: application/json" \
-d '{
      "pub_key": "A3ZqXdKRHWXaT0R0WuIhQhMNj1mV5IuKgc4vYJuLzIj1",
      "address": "cosmos1jwzq2gfv0pwsnwg4vvy83966alauf9yueslk0g"
    }'

# 5. Verify participant
curl -X GET http://34.72.225.168:8080/v1/participant/cosmos1jwzq2gfv0pwsnwg4vvy83966alauf9yueslk0g \
-H "Content-Type: application/json"

# 6. Send signed request:
inferenced signature send-request --account-address cosmos1jwzq2gfv0pwsnwg4vvy83966alauf9yueslk0g --node-address http://34.72.225.168:8080 --file /root/inference-requests/request_payload-2.json

# Or send inference without signature
# "X-Funded-By-Transfer-Node"
curl -X POST http://34.72.225.168:8080/v1/chat/completions \
-H "Content-Type: application/json" \
-H 'X-Funded-By-Transfer-Node: true' \
-d @request_payload.json
