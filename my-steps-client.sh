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
inferenced create-client client-7 --node-address http://34.72.225.168:8080

# 4. Send signed request:
inferenced signature send-request --account-address cosmos1jpsf5rnnfxl9yjmm9yemz7l2nkupeq67kgd53n --node-address http://34.72.225.168:8080 --file /root/inference-requests/request_payload-2.json
