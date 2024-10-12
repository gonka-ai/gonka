# 1. Build the chain
ignite chain build

# 2. Reset the local state
rm -rf ~/.inference

docker run -it --rm \
 -v $HOME/.inference:/root/.inference \
 -v $HOME/inference-requests:/root/inference-requests \
  gcr.io/decentralized-ai/inferenced-join \
  sh

# 3. Create a key
docker run -it --rm \
 -v $HOME/.inference:/root/.inference \
  gcr.io/decentralized-ai/inferenced-join \
  sh -c "inferenced config set client chain-id demo; inferenced config set client keyring-backend test"

docker run -it --rm \
 -v $HOME/.inference:/root/.inference \
  gcr.io/decentralized-ai/inferenced-join \
  inferenced keys add client-2

# 4. Add participant
curl -X POST http://34.72.225.168:8080/v1/participants \
-H "Content-Type: application/json" \
-d '{
      "pub_key": "A9lTZ1PjZ7rwN3ZHJf3oWsLesHqJGpavfIiv3DumeMs1",
      "address": "cosmos155dh0vt8mlgrncjxatl0dktlwvv05uttw3t330"
    }'

# 5. Verify participant
curl -X GET http://34.72.225.168:8080/v1/participants/cosmos155dh0vt8mlgrncjxatl0dktlwvv05uttw3t330 \
-H "Content-Type: application/json"

# 6. Sign a request
docker run -it --rm \
 -v $HOME/.inference:/root/.inference \
 -v $HOME/inference-requests:/root/inference-requests \
  gcr.io/decentralized-ai/inferenced-join \
  inferenced signature create --account-address cosmos155dh0vt8mlgrncjxatl0dktlwvv05uttw3t330 --file /root/inference-requests/request_payload.json

# 7. Post inference
curl -X POST http://34.72.225.168:8080/v1/chat/completions \
-H "Content-Type: application/json" \
-H 'Authorization: 4dEv9kNesXWKAgZl7oRyzcGpHdI19lJEzZH5GNOPIKc1dy6ysFqdWa66docFTzmAZw/bg1wMB8L8J7lhVhyiBQ==' \
-H "X-Requester-Address: cosmos155dh0vt8mlgrncjxatl0dktlwvv05uttw3t330" \
--data-binary @request_payload.json

# Or send inference without signature
# "X-Funded-By-Transfer-Node"
curl -X POST http://34.72.225.168:8080/v1/chat/completions \
-H "Content-Type: application/json" \
-H 'X-Funded-By-Transfer-Node: true' \
-d @request_payload.json
