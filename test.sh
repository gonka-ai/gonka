#In case previous run hasn't stopped:
docker compose -f docker-compose-sim.yml down
# Build
make api-build-docker
make node-build-docker

# setup
./init-prod-sim.sh

docker compose -f docker-compose-sim.yml up -d

# Give time for chain to bootstrap
sleep 20

./fund_accounts.py
