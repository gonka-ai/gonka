# Gonka Genesis Ceremony

The genesis ceremony is a coordinated process to bootstrap the Gonka blockchain with a pre-defined set of initial validators and an agreed-upon genesis.json file.   
This ceremony is important because it establishes the network's foundational security, ensures fair participation among validators, and creates a verifiable starting point for the blockchain.

## Overview

The ceremony is a transparent and auditable process managed entirely through GitHub Pull Requests (PRs). The core workflow is straightforward:

- Participants (Validators) submit information and offline transaction files (GENTX and GENPARTICIPANT) via PRs
- The Coordinator aggregates and verifies these inputs to publish the final, agreed `genesis.json` with a scheduled `genesis_time` and recorded hash.
- Validators verify that the file is produced correctly and launch their nodes

The ceremony proceeds through clearly defined phases to produce an auditable, shared `genesis.json`. All collaboration happens via GitHub PRs for full transparency and accountability.


This process is guided by several key principles:

- **Transparency and Auditability:** Using GitHub PRs for all submissions creates a public, verifiable record of the entire process from start to finish.

- **Decentralized Launch:** The ceremony ensures the network begins with an agreed-upon set of independent validators, establishing decentralization from block zero.

- **Verifiable State:** The final genesis.json hash is recorded, allowing every participant to confirm they are starting from the exact same initial state.

- **Consensus:** The process guarantees that all initial validators have reviewed and accepted the genesis state before the network goes live.

## Prerequisites

Before participating in the ceremony, each participant (validator) must:

1. **Fork** [the Gonka Repository](https://github.com/gonka-ai/gonka/) to your GitHub account

2. **Choose a participant (validator) name** and create your validator directory:
   ```bash
   cp -r genesis/validators/template genesis/validators/<YOUR_VALIDATOR_NAME>
   ```
   This directory will be used for sharing information and transactions during the ceremony.

3. **Follow the local setup portion of the Quickstart Guide**. Before the ceremony, you must complete the local machine setup as described in the [Gonka Quickstart](https://gonka.ai/participant/quickstart) guide. This includes installing the `inferenced` CLI, creating your Account Cold Key, and pulling the Docker images. **Stop** after pulling the images and do not launch the services; the ceremony process replaces the server-side setup and on-chain transactions with an offline, PR-based workflow.

4. Confirm readiness:
   - `inferenced` CLI is installed locally and your Account Cold Key is created
   - Containers are pulled, models downloaded, and environment variables (`config.env`) are configured


## Ceremony Process

The ceremony follows a 5-phase process, replacing the on-chain registration steps from `quickstart.md` with an offline, PR-based workflow. All transaction files are generated locally and submitted for aggregation by the Coordinator.

- **Phase 1 [Validators]**: Prepare Keys and initial server setup; open PR with validator information (including node ID, ML operational address, and consensus pubkey)
- **Phase 2 [Coordinator]**: Aggregate validator info and publish `genesis.json` draft for review
- **Phase 3 [Validators]**: Generate offline `GENTX` and `GENPARTICIPANT` files from the draft; open PR with files
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

Working directory: run all `docker compose` commands from `deploy/join` (change directory first), or pass `-f deploy/join/docker-compose.yml` explicitly when running from the repository root.

### 1. [Validators]: Prepare Keys and Initial Server Setup

This phase mirrors the key generation steps in `quickstart.md`, but all setup is performed offline to generate files for the ceremony. The Account Key (Cold) was already created during the quickstart; the following steps will guide you through generating the ML Operational Key (Warm) on your server.

#### 1.1 [Local] Confirm Account Cold Key (from Quickstart)
The Account Cold Key was created during `quickstart.md`. You can view its information with:
```bash
./inferenced keys list --keyring-backend file
```

**Example output:**
```
Enter keyring passphrase (attempt 1/3):
- address: gonka1eq4f5p32ewkekf9rv5f0qjsa0xaepckmgl85kr
  name: "gonka-account-key"
  pubkey: '{"@type":"/cosmos.crypto.secp256k1.PubKey","key":"A4U3G2eY46mwhWx7ZXieT+LetPJhG0jHNuVCQB6wgBZK"}'
  type: local
```

#### 1.2 [Server]: Initialize Node and Get Node ID
```bash
docker compose run --rm node
```

**Example output:**
```
51a9df752b60f565fe061a115b6494782447dc1f
```


#### 1.3 [Server]: Extract Consensus Public Key
Start the `tmkms` service to generate the consensus key, then extract the public key.
```bash
docker compose up -d tmkms && docker compose run --rm --entrypoint /bin/sh tmkms -c "tmkms-pubkey"
```

**Example output:**
```
/wTVavYr5OCiVssIT3Gc5nsfIH0lP1Rqn/zeQtq4CvQ=
```

#### 1.4 [Server]: Generate ML Operational Key

Create the warm key inside the `api` container using the `file` keyring backend (required for programmatic access). The key will be stored in a persistent volume mapped to `/root/.inference` of the container:

Note: `$KEY_NAME` and `$KEYRING_PASSWORD` are defined in Quickstart `config.env`.
```bash
docker compose run --rm --no-deps -it api /bin/sh
```

Inside the container, create the ML operational key:
```bash
printf '%s\n%s\n' "$KEYRING_PASSWORD" "$KEYRING_PASSWORD" | inferenced keys add "$KEY_NAME" --keyring-backend file
```

**Example output:**
```
~ # printf '%s\n%s\n' "$KEYRING_PASSWORD" "$KEYRING_PASSWORD" | inferenced keys add "$KEY_NAME" --keyring-backend file

- address: gonka1gyz2agg5yx49gy2z4qpsz9826t6s9xev6tkehw
  name: node-702105
  pubkey: '{"@type":"/cosmos.crypto.secp256k1.PubKey","key":"Ao8VPh5U5XQBcJ6qxAIwBbhF/3UPZEwzZ9H/qbIA6ipj"}'
  type: local


**Important** write this mnemonic phrase in a safe place.
It is the only way to recover your account if you ever forget your password.

again plastic athlete arrow first measure danger drastic wolf coyote work memory already inmate sorry path tackle custom write result west tray rabbit jeans
```

#### 1.5 [Local]: Prepare PR with validator information
Create or update `genesis/validators/<YOUR_VALIDATOR_NAME>/README.md` with the following fields. Use values collected above and from Quickstart.

```markdown
Account Public Key: <value of ACCOUNT_PUBKEY from your config.env file>
Node ID: <node-id-from-step-1.2>
ML Operational Address: <ml-operational-key-address-from-step-1.4>
Consensus Public Key: <consensus-pubkey-from-step-1.3>
P2P_EXTERNAL_ADDRESS: <value of P2P_EXTERNAL_ADDRESS from your config.env file>
```

#### 1.6 Create Pull Request

Submit a PR to [the Gonka Repository](https://github.com/gonka-ai/gonka/) with your validator information. Include a clear title like "Add validator: <YOUR_VALIDATOR_NAME>" and ensure all required fields are populated in your README.md file.

### 2. [Coordinator]: Genesis Draft Preparation

The coordinator will:
- Review and merge all validator PRs from Phase 1
- Prepare the initial `genesis.json` draft which includes all Account Addresses and place it in `genesis/genesis-draft.json`
- Announce the availability of the draft to all participants

### 3. [Validators]: GENTX and GENPARTICIPANT Generation

This phase involves generating the necessary transaction files for chain initialization. These transactions include:

- `MsgCreateValidator` - Creates your validator on the chain
- `MsgSubmitNewParticipant` - Registers your node as a network participant

The gentx command requires the following variables from previous steps:

- `<cold key name>` - name of Account Cold Key in local registry (e.g., "gonka-account-key" from Quickstart)
- `<YOUR_VALIDATOR_NAME>` - the validator name chosen in the Prerequisites section
- `<ml-operational-key-address-from-step-1.4>` - address of ML Operational Key from step 1.4
- `$PUBLIC_URL` - environment variable with public URL from Quickstart's `config.env`
- `<consensus-pubkey-from-step-1.3>` - consensus public key from step 1.3
- `<node-id-from-step-1.2>` - node ID from step 1.2

This custom `gentx` command automatically creates the required `authz` grants from your Account Key to your ML Operational Key, simplifying the setup process.

Before generating files, you must copy the draft `genesis/genesis-draft.json` into the `config` directory where your Account Cold Key is stored. This allows the `gentx` command to access your key and validate the transaction against the correct chain configuration.

The default home directory for `inferenced` is `~/.inference`. If you created your key there, use the following command:

```bash
cp ./genesis/genesis-draft.json ~/.inference/config/genesis.json
```

*If you specified a custom home directory with the `--home` flag when creating your key, be sure to use that same directory for the `gentx` command by providing the `--home` flag again.*

#### [Local]: Create GENTX and GENPARTICIPANT Files

The `1nicoin` value represents an artificial consensus weight for the genesis transaction. The real validator weight will be determined during the first Proof of Compute (PoC) phase.

```bash
./inferenced genesis gentx \
    --keyring-backend file \
    <cold key name> 1nicoin \
    --moniker <YOUR_VALIDATOR_NAME> \
    --pubkey <consensus-pubkey-from-step-1.3> \
    --ml-operational-address <ml-operational-key-address-from-step-1.4> \
    --url $PUBLIC_URL \
    --chain-id gonka-mainnet \
    --node-id <node-id-from-step-1.2>
```

**Example output:**
```
./inferenced genesis gentx \
    --home ./702121 \
    --keyring-backend file \
    702121 1nicoin \
    --pubkey eNrjtkSXzfE18jq3lqvpu/i1iIog9SN+kqR2Wsa6fSM= \
    --ml-operational-address gonka13xplq68fws3uvs8m7ej2ed5ack9hzpc68fwvex \
    --url http://36.189.234.237:19238 \
    --moniker "mynode-702121" --chain-id gonka-mainnet \
    --node-id 149d25924b9a6676448aea716864c31775645459
Enter keyring passphrase (attempt 1/3):
Classic genesis transaction written to "702121/config/gentx/gentx-149d25924b9a6676448aea716864c31775645459.json"
Genparticipant transaction written to "702121/config/genparticipant/genparticipant-149d25924b9a6676448aea716864c31775645459.json"
```

#### [Local]: Submit Generated Files

Copy the generated files to your validator directory and create a PR:

1. Copy files to your validator directory:

   ```bash
   cp ~/.inference/config/gentx/gentx-<node-id>.json genesis/validators/<YOUR_VALIDATOR_NAME>/
   cp ~/.inference/config/genparticipant/genparticipant-<node-id>.json genesis/validators/<YOUR_VALIDATOR_NAME>/
   ```

2. Create a PR with the following files:

   - `genesis/validators/<YOUR_VALIDATOR_NAME>/gentx-<node-id-from-step-1.2>.json`
   - `genesis/validators/<YOUR_VALIDATOR_NAME>/genparticipant-<node-id-from-step-1.2>.json`

Use a clear PR title like "Add gentx files for validator: <YOUR_VALIDATOR_NAME>".


### 4. [Coordinator]: Final Genesis Preparation

Once all validators have submitted their transaction files, the Coordinator begins constructing the official `genesis.json`. This critical step ensures all initial participants are correctly included in the blockchain's state from the very first block.

The process involves two main commands:
1.  **Collecting Genesis Transactions**: The `collect-gentxs` command gathers all `gentx-<node-id>.json` files, validates them, and incorporates them into `genesis.json` to populate the initial validator set.
2.  **Patching Participant Data**: The `patch-genesis` command processes the `genparticipant-<node-id>.json` files, verifying their signatures and patching the initial state to include all registered participants.

After merging all transactions, the Coordinator sets the `genesis_time` to a future timestamp, ensuring all validators have enough time to prepare for a synchronized launch.

Finally, the Coordinator commits the official `genesis.json` to the `genesis/` directory. The hash of this commit is then embedded into the source code to ensure all nodes start from the same verified state.

#### [Coordinator Local]: Collect Genesis Transactions

```bash
./inferenced genesis collect-gentxs --gentx-dir gentxs
```

#### [Coordinator]: Process Participant Registrations

```bash
./inferenced genesis patch-genesis --genparticipant-dir genparticipants
```

#### [Coordinator]: Configure Network Seeds

The Coordinator configures the initial network peering by setting the `GENESIS_SEEDS` variable in `deploy/join/docker-compose.yml`. This variable is a comma-separated list of validator node addresses, constructed using the `Node ID` and `P2P_EXTERNAL_ADDRESS` provided by each validator in their respective `README.md` files.

Example format: `<node-id-1>@<P2P_EXTERNAL_ADDRESS_1>,<node-id-2>@<P2P_EXTERNAL_ADDRESS_2>,...`

Additionally, the Coordinator sets `INIT_ONLY` to `false`, which allows the nodes to fully start up and connect to the network at launch time instead of just initializing their data directories.

### 5. [Validators]: Chain Launch

With the final `genesis.json` published, validators must verify that it is produced correctly and prepare their nodes to launch at the specified `genesis_time`. The blockchain will begin producing blocks at exactly this moment.

#### 5.1 [Server]: Update and Launch

These steps should be performed on your validator server.

1.  **Pull Latest Configuration**

    Pull the latest changes from the repository to get the final `genesis.json` and seed node configuration.
    ```bash
    git pull
    ```

2.  **Update Container Images**

    From the `deploy/join` directory, pull the latest Docker container images. The node image is built with the final genesis hash for verification.
    ```bash
    source config.env
    docker compose -f docker-compose.yml -f docker-compose.mlnode.yml pull
    ```

3.  **Launch Your Validator**

    Finally, start all services.
    ```bash
    docker compose -f docker-compose.yml -f docker-compose.mlnode.yml up -d
    ```

#### 5.2 [Server]: Verify Launch Status

After launching, monitor your node's logs to confirm it is waiting for the genesis time:

```bash
docker compose logs node -f
```

Look for a message similar to this:
```
INF Genesis time is in the future. Sleeping until then... genTime=2025-08-14T09:13:39Z module=server
```

**Important Notes:**
- The `api` container may restart several times before the `node` container is fully operational
- Once the genesis time passes, you should see block production messages in the logs

### 6. [Coordinator]: Post-Launch Cleanup

Remove genesis-specific variables from docker-compose.yml configuration files to transition to normal operation mode.

## Troubleshooting

For additional support, consult the [Quickstart Guide](https://gonka.ai/participant/quickstart) or join the community Discord.
