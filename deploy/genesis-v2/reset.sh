cp ../../inference-chain/scripts/init-docker-genesis.sh ./init-docker-genesis.sh
rm -rf multigen-tests

docker compose -p genesis-0 down --volumes
docker compose -p genesis-1 down --volumes
docker compose -p genesis-2 down --volumes

# then run
# docker compose -f docker-compose.yml up tmkms node
# docker compose -f docker-compose.yml down
