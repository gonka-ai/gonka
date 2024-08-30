# Stop on error! Otherwise the script will run, but use an older version that succeeded.
set -e

#In case previous run hasn't stopped:
docker compose -f docker-compose-sim.yml down
# Build
make node-build-docker
make api-build-docker

# setup
./init-prod-sim.sh

docker compose -f docker-compose-sim.yml up -d

# Give time for chain to bootstrap
sleep 10

./fund_accounts.py
