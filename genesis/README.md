# Gonka Genesis Ceremony

The genesis ceremony is a coordinated process to bootstrap the Gonka blockchain with a pre-defined set of initial validators and an agreed-upon genesis.json file.   
This ceremony is important because it establishes the network's foundational security, ensures fair participation among validators, and creates a verifiable starting point for the blockchain.

## Overview

**Goal**: Participants (validators) submit validator information and offline transaction files (gentx and genparticipant) via PRs; the Coordinator aggregates and verifies these inputs to publish the final, agreed `genesis.json` with a scheduled `genesis_time` and recorded hash.

The ceremony proceeds through clearly defined phases to produce an auditable, shared `genesis.json`. All collaboration happens via GitHub PRs for full transparency and accountability.

**Key aspects:**

- A designated Coordinator runs the process
- Participants (validators) provide required data via GitHub PRs
- The final `genesis.json` hash is recorded in the repo (and may be referenced on-chain) for auditability
- The process ensures consensus among all initial validators before launch
- Terminology: "Participant" and "Validator" refer to the same role in this document
- Roles align with `quickstart.md` terminology: Participants operate a Network Node (chain + API) and ML Node; the Coordinator aggregates and verifies using code in this repository


## Prerequisites

Before participating in the ceremony, each participant (validator) must:

1. **Fork** [the Gonka Repository](https://github.com/gonka-ai/gonka/) to your GitHub account

2. **Choose a participant (validator) name** and create your validator directory:
   ```bash
   cp -r genesis/validators/template genesis/validators/<YOUR_VALIDATOR_NAME>
   ```
   This directory will be used for sharing information and transactions during the ceremony.

3. **Complete the setup guide** by following [Gonka Quickstart](https://gonka.ai/participant/quickstart) through step **1. Pull Docker Images (Containers)**. This ensures required tools and images are available. Do not broadcast any transactions during the ceremony; you will generate offline files for PRs.

4. Confirm readiness:
   - `inferenced` CLI is installed locally and your Account Cold Key is created
   - Containers are pulled, models downloaded, and environment variables (`config.env`) are configured


## Ceremony Process

The ceremony follows a 5-phase process:

- **Phase 1 [Validators]**: Prepare Account Key and initial server setup; open PR with validator information (including node ID, ML operational address, and consensus pubkey)
- **Phase 2 [Coordinator]**: Aggregate validator info and publish `genesis.json` draft for review
- **Phase 3 [Validators]**: Generate offline `gentx` and `genparticipant` files from the draft; open PR with files
- **Phase 4 [Coordinator]**: Verify and collect transactions, patch `genesis.json`, set `genesis_time`
- **Phase 5 [Validators]**: Retrieve final `genesis.json`, verify hash, and launch nodes before `genesis_time`

### Deploy Scripts

To simplify the process, the deploy scripts for the Ceremony will be in [/deploy/join](/deploy/join) directory of [the Gonka Repository](https://github.com/gonka-ai/gonka/).  
The deploy scripts are the same as the standard join flow from `quickstart.md`. During the ceremony, the Coordinator will adjust the following environment variables to enable genesis-specific behavior:

- `INIT_ONLY` — initialize data directories and prepare configs without starting the full stack
- `GENESIS_SEEDS` — seed node address list used for initial P2P connectivity at launch
- `IS_GENESIS` — toggle genesis-only paths (e.g., hash verification, bootstrap behavior) in compose/scripts

Location: these variables are set by the Coordinator in `deploy/join/docker-compose.yml`. Validators should not change them.

Once **Phase 5** is finished and the chain has launched, the variables above are removed from the repo by the Coordinator as they're not required further.

### 1. [Validators]: Account Key Registration

#### Steps:
1. **Generate Account Cold Key**
   ```bash
   # Generate your account key using the appropriate method
   # Keep this key secure - it will be used for validator operations
   ```

2. **Submit Public Key**
   Create `genesis/validators/<YOUR_NAME>/README.md` with:
   ```markdown
   # Validator: <YOUR_NAME>
   
   Account Public Key: <YOUR_PUBKEY>
   ```

3. **Create Pull Request**
   Submit a PR to the gonka repository with your validator information.

### 2. [Coordinator]: Genesis Draft Preparation

The coordinator will:
- Review and merge all validator PRs
- Prepare the initial genesis.json draft
- Distribute the draft for validator review and approval

### 3. [Validators]: GENTX and GENPARTICIPANT Generation

This phase involves generating the necessary transaction files for chain initialization.

#### [Server]: Initialize Node and Get Node ID
```bash
docker compose run --rm node
# Expected output example:
# 51a9df752b60f565fe061a115b6494782447dc1f
```

#### [Server]: Extract Consensus Public Key
```bash
docker compose up -d tmkms && docker compose run --rm --entrypoint /bin/sh tmkms -c "tmkms-pubkey"
# Expected output example:
# /wTVavYr5OCiVssIT3Gc5nsfIH0lP1Rqn/zeQtq4CvQ=
```

#### [Server]: Generate ML Operational Key
```bash
docker compose run --rm --no-deps -it api /bin/sh
# Expected output example:
# address: gonka1z7w7kqukkek7n6yenwu826mqwz8yjuf2u62wm2
```

#### [Local]: Create GENTX and GENPARTICIPANT Files
```bash
./inferenced genesis gentx \
    --home ./702103 \
    --keyring-backend file \
    702103 1nicoin \
    --pubkey /wTVavYr5OCiVssIT3Gc5nsfIH0lP1Rqn/zeQtq4CvQ= \
    --ml-operational-address gonka1z7w7kqukkek7n6yenwu826mqwz8yjuf2u62wm2 \
    --url http://36.189.234.237:19256 \
    --moniker "mynode-702103" \
    --chain-id gonka-testnet-7 \
    --node-id 51a9df752b60f565fe061a115b6494782447dc1f
```

#### [Local]: Submit Generated Files

Create a PR with the following files:
- `genesis/validators/<YOUR_NAME>/gentx-*.json`
- `genesis/validators/<YOUR_NAME>/genparticipant-*.json`

Update your `genesis/validators/<YOUR_NAME>/README.md`:
```markdown
# Validator: <YOUR_NAME>

Account Public Key: <YOUR_PUBKEY>
Node ID: <YOUR_NODE_ID>
P2P Address: <YOUR_P2P_ADDRESS>
```

### 4. [Coordinator]: Final Genesis Preparation

#### Collect Genesis Transactions
```bash
./inferenced genesis collect-gentxs --home inference --gentx-dir gentxs
```

#### Process Participant Registrations
```bash
./inferenced genesis patch-genesis --home inference --genparticipant-dir genparticipants
```

#### Configure Network Seeds
- Set `GENESIS_SEEDS` variable in docker-compose.yml
- Set `INIT_ONLY` to `false`

### 5. [Validators]: Chain Launch

The blockchain will begin producing blocks at the `genesis_time` specified in the genesis.json file.

#### Launch Steps:
1. **Update Configuration**
   ```bash
   # Pull the latest genesis.json from GitHub
   git pull origin main
   ```

2. **Update Containers**
   ```bash
   # Pull latest container images (includes genesis.json hash verification)
   docker compose pull
   ```

3. **Launch Validator Node**
   ```bash
   docker compose up -d
   ```

4. **Verify Launch Status**
   Check for this message in your node container logs:
   ```
   INF Genesis time is in the future. Sleeping until then... genTime=2025-08-14T09:13:39Z module=server
   ```

#### Important Notes:
- The API container may restart multiple times until the node container is fully operational
- Monitor logs carefully during the launch phase
- Ensure your system time is synchronized with UTC

### 6. [Coordinator]: Post-Launch Cleanup

Remove genesis-specific variables from docker-compose.yml configuration files to transition to normal operation mode.

## Troubleshooting

### Common Issues:
- **Time synchronization**: Ensure your server time is accurate
- **Network connectivity**: Verify P2P connectivity with other validators
- **Key management**: Double-check all keys are properly generated and accessible
- **Docker issues**: Ensure all containers can communicate properly

### Support:
For technical support during the ceremony, please:
1. Check the logs for specific error messages
2. Consult the [Quickstart Guide](https://gonka.ai/participant/quickstart)
3. Contact the coordinator through the designated communication channels

## Security Considerations

- Keep your account cold keys secure and offline when not needed
- Use TMKMS for consensus key management in production
- Verify all genesis.json hashes before proceeding
- Maintain secure communication channels during the ceremony