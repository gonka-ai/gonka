# Use these scripts to run health check on your node

[Health Check script](https://github.com/gonka-ai/gonka/blob/testnet/main/test-net-cloud/nebius/health-check-scripts/healthcheck.sh)

[Node examples](https://github.com/gonka-ai/gonka/blob/testnet/main/test-net-cloud/nebius/health-check-scripts/healthcheck-nodes-example.csv)

.env example (please copy in edit mode, because of the markup and because .env files are not attachable):

# Copy to .env and edit values for your environment.

# Shared
PUBLIC_DOMAIN=xj7-5.s.filfox.io
CHAIN_ID=gonka-testnet-3
REPO_BRANCH=origin/testnet/main
DEPLOY_DIR=/srv/dai
HF_HOME=/srv/dai/cache/
KEYRING_PASSWORD=12345678
GENESIS_KEY_NAME=gonka-account-key
SSH_USER=decentai
SSH_KEY_PATH=~/.ssh/id_ed25519

# Genesis
GENESIS_SSH_PORT=18220
GENESIS_P2P_PORT=19239
GENESIS_API_PORT=19240
GENESIS_INTERNAL_API_PORT=8000
GENESIS_INTERNAL_IP=172.18.114.113
GENESIS_HEALTHCHECK_SSH_PORT=22

# Join-1
JOIN1_SSH_PORT=18225
JOIN1_P2P_PORT=19249
JOIN1_API_PORT=19250
JOIN1_INTERNAL_API_PORT=8000
JOIN1_INTERNAL_IP=172.18.114.125
JOIN1_HEALTHCHECK_SSH_PORT=22

# Join-2
JOIN2_SSH_PORT=18226
JOIN2_P2P_PORT=19251
JOIN2_API_PORT=19252
JOIN2_INTERNAL_API_PORT=8000
JOIN2_INTERNAL_IP=172.18.114.105
JOIN2_HEALTHCHECK_SSH_PORT=18226
