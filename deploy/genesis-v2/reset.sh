cp ../../inference-chain/scripts/init-docker-genesis.sh ./init-docker-genesis.sh
rm -rf ../../multigen-tests/genesis
rm -rf multigen-tests
docker compose -f docker-compose.yml down --volumes

# then run
# docker compose -f docker-compose.yml up tmkms node
# docker compose -f docker-compose.yml down
