# Download latest inferenced binary to create a dev account
export ACCOUNT_NAME="test-account"
# Url of the genesis k8s node API
export NODE_URL="http://34.9.136.116:30000"
export GONKA_ENDPOINTS=$NODE_URL/v1

curl "$NODE_URL/v1/epochs/current/participants" | jq


