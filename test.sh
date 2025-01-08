# Stop on error! Otherwise the script will run, but use an older version that succeeded.
set -e

#In case previous run hasn't stopped:
docker compose -f docker-compose-sim.yml down
docker compose -f docker-compose-local.yml down -v
# Build
make node-build-docker
make api-build-docker

# setup
./init-prod-sim.sh

docker compose -f docker-compose-sim.yml up -d

# Give time for chain to bootstrap
sleep 10

# Activate Python virtual environment
if [ -d "chain-venv" ]; then
    source chain-venv/bin/activate
fi

make build-docker
# If FUND_ACCOUNTS env var is set
if [ -n "$FUND_ACCOUNTS" ]; then
    # Fund accounts
    echo "FUND_ACCOUNTS is set. Funding!"
    ./fund_accounts.py
else
    echo "FUND_ACCOUNTS is not set. Skipping funding accounts."
fi
