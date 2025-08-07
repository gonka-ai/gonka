cp ../../inference-chain/scripts/init-docker-genesis.sh ./init-docker-genesis.sh
rm -rf ../../multigen-tests/genesis

# then run
# docker compose -f docker-compose.yml up -d tmkms node
# docker compose -f docker-compose.yml down
