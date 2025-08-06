# Multi-Validator Genesis Deployment (Manual, Staged Process)

This guide outlines the manual, multi-stage process for launching a new network with multiple genesis validators. This process is robust and explicit, ensuring all participants are synchronized at each step. It is the recommended procedure for a real-world, multi-machine launch.

## Overview of the Stages

The process is divided into several distinct stages, with specific actions for validators and the coordinator.

*   **Stage 1: Key Generation (All Validators)**
    *   Each validator generates their keys and sends their public address to the coordinator.

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

### Stage 1: Generate Validator Key

*   **Action**: To be performed by **every** genesis validator (including the coordinator).
*   **Goal**: Create your keys and send your public address to the coordinator.

1.  Create a local directory for your files (e.g., `mkdir -p ~/validator-1-files`).
    For local testing on single machine you can use:
    ```bash
    cd multigen-tests
    rm -rf validator-1 validator-2 validator-3 coordinator || true
    mkdir validator-1 validator-2 validator-3 coordinator
    cd ..
    ```
2.  Run the `stage-1-generate-key.sh` script via Docker. This will create your keys and place your address and private consensus key in your local directory.

    ```bash
    MONIKER="validator-3"
    VAL_DIR_PATH="./multigen-tests/$MONIKER"
    docker run --rm -it \
        -v "$VAL_DIR_PATH":/output \
        -e MONIKER="$MONIKER" \
        -v ./deploy/multi-genesis-manual/stage-1-generate-key.sh:/root/stage-1.sh \
        ghcr.io/product-science/inferenced:latest \
        sh /root/stage-1.sh
    ```
    **IMPORTANT**: The script will output a 24-word mnemonic phrase. **Save this phrase somewhere safe and offline.** You will need it in Stage 3.

3.  **Send the `address.txt` file** from `~/validator-1-files` to the coordinator. Keep the `priv_validator_key.json` file safe and private.

### Stage 2: Create Intermediate Genesis

*   **Action**: To be performed by the **Coordinator only**.
*   **Goal**: Create a `genesis-intermediate.json` file with all validators' accounts.

1.  Create a directory structure for collecting files:
    ```
    ./coordinator-data/
    └── addresses_collected/
        ├── validator-1.address
        └── coordinator.address
    ```
2.      Place all `address.txt` files received from the other validators into the `addresses_collected` directory.
    **Note**: The coordinator must also run the Stage 1 script to generate their own keys. They will then copy their own `address.txt` into this directory as well.

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
3.  Run the `stage-2-create-intermediate-genesis.sh` script. This will generate the `genesis-intermediate.json`.

    ```bash
    COORDINATOR_DIR_PATH="./multigen-tests/coordinator-data"
    docker run --rm -it \
        -v "$COORDINATOR_DIR_PATH:/data" \
        -v ./deploy/multi-genesis-manual/genesis_overrides.json:/data/genesis_overrides.json \
        -v ./deploy/multi-genesis-manual/stage-2-create-intermediate-genesis.sh:/root/stage-2.sh \
        ghcr.io/product-science/inferenced:latest \
        sh /root/stage-2.sh
    ```
4.  **Distribute the `genesis-intermediate.json`** file (located in `./coordinator-data/`) to all validators.

### Stage 3: Generate Gentx

*   **Action**: To be performed by **every** genesis validator.
*   **Goal**: Use the intermediate genesis to create your `gentx` file.

1.  Place the `genesis-intermediate.json` you received from the coordinator into your local validator directory (e.g., `~/validator-1-files`).
2.  Run the `stage-3-create-gentx.sh` script. You will be prompted to enter the 24-word mnemonic phrase you saved from Stage 1.

    When running locally:
    ```bash
    cp ./multigen-tests/coordinator-data/intermediate_genesis_output/genesis-intermediate.json ./multigen-tests/validator-1/
    cp ./multigen-tests/coordinator-data/intermediate_genesis_output/genesis-intermediate.json ./multigen-tests/validator-2/
    cp ./multigen-tests/coordinator-data/intermediate_genesis_output/genesis-intermediate.json ./multigen-tests/validator-3/
    ```

    Then run the script:
    
    ```bash
    MONIKER="validator-1"
    DIR_PATH="./multigen-tests/$MONIKER"
    docker run --rm -it \
        -v "$DIR_PATH:/output" \
        -e MONIKER="$MONIKER" \
        -v ./deploy/multi-genesis-manual/stage-3-create-gentx.sh:/root/stage-3.sh \
        ghcr.io/product-science/inferenced:latest \
        sh /root/stage-3.sh
    ```
3.  **Send the generated `gentx` file** (located in `~/validator-1-files/gentx/`) back to the coordinator.

### Stage 4: Assemble Final Genesis

*   **Action**: To be performed by the **Coordinator only**.
*   **Goal**: Collect all `gentx` files and produce the final `genesis.json`.

1.  Create a directory for the collected `gentx` files:
    ```
    ./coordinator-data/gentx_collected/
    ```
2.  Place all received `gentx` files into that directory.
3.  Run the `stage-4-assemble-final-genesis.sh` script.

    ```bash
    COORDINATOR_DIR_PATH="./multigen-tests/coordinator-data"
    docker run --rm -it \
        -v "$COORDINATOR_DIR_PATH:/data/intermediate_genesis_output" \
        -v "$COORDINATOR_DIR_PATH/gentx_collected:/data/gentx_collected" \
        -v "$COORDINATOR_DIR_PATH:/data/final_genesis_output" \
        -v ./deploy/multi-genesis-manual/stage-4-assemble-final-genesis.sh:/root/stage-4.sh \
        ghcr.io/product-science/inferenced:latest \
        sh /root/stage-4.sh
    ```

### Stage 5: Launch!

*   **Action**: To be performed by **every** validator, including the coordinator.
*   **Goal**: Start your node and connect to the network.

1.  **Distribute the Final Genesis**: The coordinator takes the `genesis-final.json` from their local `./multigen-tests/coordinator-data/` directory and distributes it to all validators. **Use a checksum to verify integrity!**

2.  **Prepare your Node's Launch Directory**: Each validator must create a launch directory on their machine. For example, `~/validator-1-launch/`. Inside this directory, you need to set up the following structure:
    ```
    ~/validator-1-launch/
    ├── config/
    │   └── genesis.json  (This is the genesis-final.json, renamed)
    ├── tmkms/
    │   └── priv_validator_key.json (The private key from Stage 1)
    ├── start-node.sh (Copy of the script from the repo)
    └── config.env (See template for details)
    ```
    *   Place the **final `genesis.json`** into the `config/` subdirectory.
    *   Place the **`priv_validator_key.json`** you generated in Stage 1 into the `tmkms/` subdirectory.
    *   Copy the `deploy/multi-genesis-manual/start-node.sh` script into the root of your launch directory.
    *   Create a `config.env` file. You can copy `deploy/multi-genesis-manual/config.env.template` and fill it out with the `P2P_PERSISTENT_PEERS` list of all other genesis validators.

3.  **Launch your Node**:
    Navigate to your launch directory and run the `docker-compose.validator.yml`.
    ```bash
    cd ~/validator-1-launch/
    docker-compose -f /path/to/repo/deploy/multi-genesis-manual/docker-compose.validator.yml up -d
    ```
4.  **Repeat for All Nodes**: Every validator, including the coordinator, follows these same steps in Stage 5 to launch their node.
