# Genesis Ceremony

The genesis ceremony is a coordinated process to bootstrap the Gonka blockchain with a pre-defined set of initial validators and an agreed-upon genesis.json file.

## Overview

The ceremony involves multiple rounds to prepare the initial genesis.json file. All rounds are conducted transparently through GitHub, ensuring public visibility and accountability.

Key aspects:
- The ceremony is coordinated by a designated Coordinator
- Validators participate by providing their data via GitHub Pull Requests
- The final genesis.json hash is recorded both on-chain and in the repository for full auditability
- The process ensures consensus among all initial validators before chain launch

## Prerequisites

Before participating in the ceremony, each validator must:

1. **Fork this repository** and create a validator directory
2. **Set up server infrastructure** with proper key management
3. **Review the quickstart guide** at [Gonka Quickstart](https://gonka.ai/participant/quickstart), download models, setup env variables

### Initial Setup

Create your validator directory:
```bash
mkdir -p genesis/validators/<YOUR_VALIDATOR_NAME>
```

Use the template structure from [genesis/validators/template](genesis/validators/template).

## Ceremony Process

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