# Multi-Validator Genesis Deployment (Manual, Staged Process)

This guide outlines the manual, multi-stage process for launching a new network with multiple genesis validators. This process is robust and explicit, ensuring all participants are synchronized at each step. It is the recommended procedure for a real-world, multi-machine launch.

## Overview of the Stages

The process is divided into several distinct stages, with specific actions for validators and the coordinator.

*   **Stage 1: Key Generation (All Validators)**
    *   Each validator generates their keys and all necessary config files in a local directory. They send their public address to the coordinator.

*   **Stage 2: Create Intermediate Genesis (Coordinator Only)**
    *   The coordinator collects all addresses and creates an *intermediate* `genesis.json` containing the initial account balances for everyone.

*   **Stage 3: Gentx Generation (All Validators)**
    *   The coordinator distributes the `genesis-intermediate.json`.
    *   Each validator uses it to generate their `gentx` (genesis transaction) and sends it back to the coordinator.

*   **Stage 4: Assemble Final Genesis (Coordinator Only)**
    *   The coordinator collects all `gentx` files and runs a final script to produce the `genesis-final.json`.

*   **Stage 5: Launch (All Validators)**
    *   The coordinator distributes the `genesis-final.json`, and all validators launch their nodes.

---

## Detailed Steps

### Stage 1: Generate Validator Files

*   **Action**: To be performed by **every** genesis validator (including the coordinator).
*   **Goal**: Create your keys and all necessary node files in a single directory.

1.  Create a local directory for your validator. 
    *For local testing on a single machine, you can create a set of directories:*
    ```bash
    cd multigen-tests
    rm -rf validator-1 validator-2 validator-3 coordinator || true
    mkdir validator-1 validator-2 validator-3 coordinator
    cd ..
    ```

2.  Run the `stage-1-generate-key.sh` script via Docker. This will initialize a full node directory, including your keys, in your local folder.

    *For a single validator:*
    ```bash
    # Replace with your moniker and directory path
    MONIKER="validator-1"
    VAL_DIR_PATH="$HOME/validator-1-files"
    
    docker run --rm -it \
        -v "$VAL_DIR_PATH:/output" \
        -e MONIKER="$MONIKER" \
        -v ./deploy/multi-genesis-manual/stage-1-generate-key.sh:/root/stage-1.sh \
        ghcr.io/product-science/inferenced:latest \
        sh /root/stage-1.sh
    ```
3.  **Send the `address.txt` file** from your validator directory to the coordinator.

### Stage 2: Create Intermediate Genesis

*   **Action**: To be performed by the **Coordinator only**.
*   **Goal**: Create a `genesis-intermediate.json` file with all validators' accounts.

1.  Collect all `address.txt` files and place them in a single directory. Remember to include your own from Stage 1.
    
    *Example for a local test:*
    ```bash
    # Reset dirs
    rm -rf ./multigen-tests/coordinator-data/addresses_collected || true
    mkdir -p ./multigen-tests/coordinator-data/addresses_collected
    # Copy addresses from all validators, including the coordinator
    cp ./multigen-tests/validator-1/address.txt ./multigen-tests/coordinator-data/addresses_collected/validator-1.address
    cp ./multigen-tests/validator-2/address.txt ./multigen-tests/coordinator-data/addresses_collected/validator-2.address
    cp ./multigen-tests/validator-3/address.txt ./multigen-tests/coordinator-data/addresses_collected/validator-3.address
    cp ./multigen-tests/coordinator/address.txt ./multigen-tests/coordinator-data/addresses_collected/coordinator.address
    ```

2.  Run the `stage-2-create-intermediate-genesis.sh` script.

    ```bash
    COORDINATOR_DIR_PATH="./multigen-tests/coordinator-data"
    docker run --rm -it \
        -v "$COORDINATOR_DIR_PATH:/data" \
        -v ./deploy/multi-genesis-manual/genesis_overrides.json:/data/genesis_overrides.json \
        -v ./deploy/multi-genesis-manual/stage-2-create-intermediate-genesis.sh:/root/stage-2.sh \
        ghcr.io/product-science/inferenced:latest \
        sh /root/stage-2.sh
    ```
3.  **Distribute the `genesis-intermediate.json`** file to all validators.

### Stage 3: Generate Gentx

*   **Action**: To be performed by **every** genesis validator, including the coordinator.
*   **Goal**: Use the intermediate genesis to create your `gentx` file non-interactively.

1.  Place the `genesis-intermediate.json` you received into your local validator directory from Stage 1.

    *For a local test, copy the file to all validator directories:*
    ```bash
    COORDINATOR_DIR_PATH="./multigen-tests/coordinator-data"
    cp "$COORDINATOR_DIR_PATH/intermediate_genesis_output/genesis-intermediate.json" ./multigen-tests/validator-1/
    cp "$COORDINATOR_DIR_PATH/intermediate_genesis_output/genesis-intermediate.json" ./multigen-tests/validator-2/
    cp "$COORDINATOR_DIR_PATH/intermediate_genesis_output/genesis-intermediate.json" ./multigen-tests/validator-3/
    cp "$COORDINATOR_DIR_PATH/intermediate_genesis_output/genesis-intermediate.json" ./multigen-tests/coordinator/
    ```

2.  Run the `stage-3-create-gentx.sh` script. It will read your key from the `keyring-file` subdirectory and create the `gentx`.

    *For a single validator:*
    ```bash
    # Replace with your moniker and directory path
    MONIKER="validator-1"
    VAL_DIR_PATH="./multigen-tests/$MONIKER"

    docker run --rm -it \
        -v "$VAL_DIR_PATH:/output" \
        -e MONIKER="$MONIKER" \
        -v ./deploy/multi-genesis-manual/stage-3-create-gentx.sh:/root/stage-3.sh \
        ghcr.io/product-science/inferenced:latest \
        sh /root/stage-3.sh
    ```
3.  **Send the generated `gentx-$MONIKER.json` file** back to the coordinator.

### Stage 4: Assemble Final Genesis

*   **Action**: To be performed by the **Coordinator only**.
*   **Goal**: Collect all `gentx` files and produce the final `genesis.json`.

1.  Collect all `gentx-....json` files into a single directory. Remember to include your own from Stage 3.

    *For a local test:*
    ```bash
    COORDINATOR_DIR_PATH="./multigen-tests/coordinator-data"
    mkdir -p "$COORDINATOR_DIR_PATH/gentx_collected"
    cp ./multigen-tests/validator-1/gentx-validator-1.json "$COORDINATOR_DIR_PATH/gentx_collected/"
    cp ./multigen-tests/validator-2/gentx-validator-2.json "$COORDINATOR_DIR_PATH/gentx_collected/"
    cp ./multigen-tests/coordinator/gentx-coordinator.json "$COORDINATOR_DIR_PATH/gentx_collected/"
    ```

2.  Run the `stage-4-assemble-final-genesis.sh` script.

    ```bash
    COORDINATOR_DIR_PATH="./multigen-tests/coordinator-data"
    docker run --rm -it \
        -v "$COORDINATOR_DIR_PATH/intermediate_genesis_output:/data/intermediate_genesis_output" \
        -v "$COORDINATOR_DIR_PATH/gentx_collected:/data/gentx_collected" \
        -v "$COORDINATOR_DIR_PATH:/data/final_genesis_output" \
        -v ./deploy/multi-genesis-manual/stage-4-assemble-final-genesis.sh:/root/stage-4.sh \
        ghcr.io/product-science/inferenced:latest \
        sh /root/stage-4.sh
    ```

### Stage 5: Launch!

*   **Action**: To be performed by **every** validator, including the coordinator.
*   **Goal**: Start your node and connect to the network.

1.  **Distribute the Final Genesis**: The coordinator takes the `genesis-final.json` from `./coordinator-data/final_genesis_output` and distributes it. **Use a checksum to verify integrity!**

2.  **Prepare your Node's Launch Directory**: Your validator directory from Stage 1 is now your launch directory. You just need to update the genesis file and create your `config.env`.
    *For a local test, copy the final genesis to each validator's config directory:*
    ```bash
    FINAL_GENESIS_PATH="./multigen-tests/coordinator-data/final_genesis_output/genesis-final.json"
    cp "$FINAL_GENESIS_PATH" ./multigen-tests/validator-1/config/genesis.json
    cp "$FINAL_GENESIS_PATH" ./multigen-tests/validator-2/config/genesis.json
    cp "$FINAL_GENESIS_PATH" ./multigen-tests/coordinator/config/genesis.json
    ```
    *   For each validator, copy the `priv_validator_key.json` into a new `tmkms/` subdirectory: `mkdir tmkms && mv priv_validator_key.json tmkms/`.
    *   Create a `config.env` file (using the template) with the `P2P_PERSISTENT_PEERS` list.

3.  **Launch your Node**:
    Navigate to your validator directory (e.g., `./multigen-tests/validator-1/`) and run the `docker-compose.validator.yml`.
    ```bash
    cd ./multigen-tests/validator-1/
    docker-compose -f ../../deploy/multi-genesis-manual/docker-compose.validator.yml up -d
    ```
