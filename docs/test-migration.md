# Test a Migration on a Mainnet Fork

This procedure forks a synced mainnet node into a single-validator local
chain and runs an upgrade handler against the real state. Used to validate
a migration before promoting the upgrade proposal.

## How it works

Cosmos SDK ships `inferenced in-place-testnet [chain-id] [operator-addr]`,
which rewrites CometBFT consensus state so the local validator key
controls the network. The chain app additionally rewrites x/staking and
x/slashing state and schedules the upgrade requested via
`--trigger-testnet-upgrade` (see `inference-chain/app/testnet_init.go`).
The resulting chain runs the upgrade handler at the first block of the
fork, against unmodified mainnet state, and produces blocks under the
local key alone.

The fork's chain ID is local; its data directory cannot rejoin mainnet
afterwards.

## Prerequisites

- A node container synced to mainnet head, with its data directory on
  the host (e.g. mounted at `./.inference` from a `deploy/join` setup).
- The branch with the upgrade handler checked out.
- Disk for one full copy of `.inference`.

## Procedure

Replace `<deploy>` with your deploy directory, `<workspace>` with the
checkout root, `<vX.Y.Z>` with the upgrade name registered in
`inference-chain/app/upgrades.go`, and `<base-image>` with the image tag
of the currently running node (the binary mounts in but uses that image's
runtime).

1. Snapshot the synced state. Stop the running node and copy
   `.inference` so the test can be rerun without re-syncing:

   ```sh
   cd <deploy>
   docker stop node tmkms
   sudo cp -a .inference .inference.synced-backup
   ```

2. Build the upgrade binary:

   ```sh
   cd <workspace>
   make build-for-upgrade
   ```

   Output: `public-html/v2/inferenced/inferenced` (musl-linked, runs
   inside the alpine-based node image).

3. Switch the node config from TMKMS to a local file-backed validator
   key. The fork uses a fresh, throwaway consensus key that
   `LoadOrGenFilePV` generates on first launch:

   ```sh
   cd <deploy>
   sudo sed -i \
     -e 's|^priv_validator_laddr =.*|priv_validator_laddr = ""|' \
     -e 's|^# priv_validator_key_file *=|priv_validator_key_file =|' \
     -e 's|^# priv_validator_state_file *=|priv_validator_state_file =|' \
     .inference/config/config.toml
   ```

4. Pick any existing mainnet `gonkavaloper1...` operator address. Power
   is reassigned to it; the address never signs.

   ```sh
   curl -s '<chain-api>/cosmos/staking/v1beta1/validators?pagination.limit=200&status=BOND_STATUS_BONDED' \
     | jq -r '.validators | sort_by(.tokens|tonumber) | reverse | .[0].operator_address'
   ```

5. Launch the fork. Mount the new binary into the running image as
   runtime; pass the upgrade name to `--trigger-testnet-upgrade`:

   ```sh
   docker run -d --name fork-node \
     -v <workspace>/public-html/v2/inferenced:/upgrade-bin:ro \
     -v <deploy>/.inference:/root/.inference \
     -p 26657:26657 -p 1317:1317 \
     --entrypoint /upgrade-bin/inferenced \
     <base-image> \
     in-place-testnet gonka-mainnet-fork <operator-addr> \
       --trigger-testnet-upgrade <vX.Y.Z> \
       --skip-confirmation \
       --home /root/.inference
   ```

6. Confirm the migration ran:

   ```sh
   docker logs fork-node 2>&1 \
     | grep -E "applying upgrade|starting upgrade|successfully upgraded|panic|FAILURE"
   ```

   Expected: `applying upgrade "<vX.Y.Z>" at height: 1`, then
   `starting upgrade ... version=<vX.Y.Z>`, then
   `successfully upgraded ... version=<vX.Y.Z>` with no panic.

7. Confirm block production for at least 500 blocks past the upgrade
   block. Stale-vote errors from mainnet peers (`cannot find validator N
   in valSet of size 1`) are expected and benign:

   ```sh
   START=$(curl -s http://127.0.0.1:26657/status \
     | jq -r '.result.sync_info.latest_block_height')
   while H=$(curl -s http://127.0.0.1:26657/status \
     | jq -r '.result.sync_info.latest_block_height'); \
     [ $((H - START)) -lt 500 ]; do echo "h=$H"; sleep 5; done
   ```

8. Spot-check post-upgrade state. The exact assertions are
   migration-specific. Useful entry points:

   ```sh
   # On-chain params (any new fields the migration sets should appear here)
   curl -s http://127.0.0.1:1317/productscience/inference/inference/params | jq

   # Run any inferenced query directly inside the fork container
   docker exec fork-node /upgrade-bin/inferenced q <module> <subcommand> ...
   ```

   Pick the queries that exercise the specific migrations the handler
   performs (see `inference-chain/app/upgrades/<vX_Y_Z>/upgrades.go`).

## Reset between runs

The fork modifies state in place. To rerun:

```sh
docker rm -f fork-node
cd <deploy>
sudo rm -rf .inference
sudo cp -a .inference.synced-backup .inference
# re-apply the priv_validator sed from step 3
```

## Scope

This validates the upgrade handler against real mainnet state and
post-upgrade block production. It does not exercise the cosmovisor
binary download path or the governance proposal flow; those are covered
by Testermint's `submit upgrade` test in
`testermint/src/test/kotlin/UpgradeTests.kt`.
