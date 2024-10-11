# 1. Build the chain
ignite chain build

# 2. Reset the local state
rm -rf ~/.inference

# 3. Create a key
docker run -it --rm \
 -v $HOME/.inference:/root/.inference \
  gcr.io/decentralized-ai/inferenced-join \
  sh -c "inferenced config set client chain-id demo; inferenced config set client keyring-backend test"

docker run -it --rm \
 -v $HOME/.inference:/root/.inference \
  gcr.io/decentralized-ai/inferenced-join \
  inferenced keys add client

# 4. Add participant
curl -X POST http://34.72.225.168:8080/v1/participants \
-H "Content-Type: application/json" \
-d '{
      "pub_key": "AqqILw9/4dsX9vvrD6fRVzSQQ2XIdCBGb1rZJmnavnHt",
      "address": "cosmos1667wh0cgezjed2nxw6lrpfccsf3lf0rd2frga4"
    }'

# 5. Verify participant
curl -X GET http://34.72.225.168:8080/v1/participants/cosmos1667wh0cgezjed2nxw6lrpfccsf3lf0rd2frga4 \
-H "Content-Type: application/json"

# 6. Sign a request
docker run -it --rm \
 -v $HOME/.inference:/root/.inference \
 -v $HOME/inference-requests:/root/inference-requests \
  gcr.io/decentralized-ai/inferenced-join \
  inferenced signature create --account-address cosmos1667wh0cgezjed2nxw6lrpfccsf3lf0rd2frga4 --file /root/inference-requests/request_payload.json

# 7. Post inference
curl -X POST http://34.72.225.168:8080/v1/chat/completions \
-H "Content-Type: application/json" \
-H 'Authorization: g74Mpaz7BkdSc0Va7sTozIzXA/MTZbeSdluPMgdljG1QF8aU9/PKQbLDgjorgtschFkpD45ct1M1K+bDU2+mNw==' \
-H "X-Requester-Address: cosmos1667wh0cgezjed2nxw6lrpfccsf3lf0rd2frga4" \
-d @request_payload.json

# Or send inference without signature
# "X-Funded-By-Transfer-Node"
curl -X POST http://34.72.225.168:8080/v1/chat/completions \
-H "Content-Type: application/json" \
-H 'X-Funded-By-Transfer-Node: true' \
-d @request_payload.json
