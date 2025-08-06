# Multi-Validator Genesis Deployment (Manual, Staged Process)

This guide outlines the manual, multi-stage process for launching a new network with multiple genesis validators. This process is robust and explicit, ensuring all participants are synchronized at each step. It is the recommended procedure for a real-world, multi-machine launch.

## Overview of the Stages

*   **Stage 1: Key Generation (All Validators)**
    *   Each validator generates their keys and saves the full output, including the mnemonic. They send their public address to the coordinator.

*   **Stage 2: Create Intermediate Genesis (Coordinator Only)**
    *   The coordinator creates an *intermediate* `genesis.json` containing the initial account balances for everyone.

*   **Stage 3: Gentx Generation (All Validators)**
    *   The coordinator distributes the `genesis-intermediate.json`.
    *   Each validator runs an interactive script, pasting their mnemonic to create their `gentx` (genesis transaction). The `gentx` is sent back to the coordinator.

*   **Stage 4: Assemble Final Genesis (Coordinator Only)**
    *   The coordinator collects all `gentx` files and produces the `genesis-final.json`.

*   **Stage 5: Launch (All Validators)**
    *   The coordinator distributes the `genesis-final.json`, and all validators launch their nodes.

---

## Detailed Steps

### Stage 1: Generate Validator Files

*   **Action**: To be performed by **every** genesis validator (including the coordinator).
*   **Goal**: Create your keys and node files, and save your mnemonic.

1.  Create a local directory for your validator (e.g., `mkdir -p ~/validator-1-files`).
2.  Run the `stage-1-generate-key.sh` script via Docker.

    ```bash
    MONIKER="validator-1"
    VAL_DIR_PATH="$HOME/validator-1-files"
    
    docker run --rm -it \
        -v "$VAL_DIR_PATH:/output" \
        -e MONIKER="$MONIKER" \
        -v ./deploy/multi-genesis-manual/stage-1-generate-key.sh:/root/stage-1.sh \
        ghcr.io/product-science/inferenced:latest \
        sh /root/stage-1.sh
    ```
3.  **IMPORTANT**: The script will save all key generation output to `mnemonic.txt` in your validator directory. **Open this file and back up your 24-word mnemonic phrase somewhere safe and offline.**

4.  **Send the `address.txt` file** from your validator directory to the coordinator.

### Stage 2: Create Intermediate Genesis

*   **Action**: To be performed by the **Coordinator only**.
*   **Goal**: Create a `genesis-intermediate.json` file.

1.  Collect all `address.txt` files and place them in a directory (e.g., `./coordinator-data/addresses_collected`). Include your own.
2.  Run the `stage-2-create-intermediate-genesis.sh` script.

    ```bash
    COORDINATOR_DIR_PATH="./coordinator-data"
    docker run --rm -it \
        -v "$COORDINATOR_DIR_PATH/addresses_collected:/data/addresses_collected" \
        -v "$COORDINATOR_DIR_PATH:/data/intermediate_genesis_output" \
        -v ./deploy/multi-genesis-manual/genesis_overrides.json:/data/genesis_overrides.json \
        -v ./deploy/multi-genesis-manual/stage-2-create-intermediate-genesis.sh:/root/stage-2.sh \
        ghcr.io/product-science/inferenced:latest \
        sh /root/stage-2.sh
    ```
3.  **Distribute the `genesis-intermediate.json`** file to all validators.

### Stage 3: Generate Gentx

*   **Action**: To be performed by **every** genesis validator, including the coordinator.
*   **Goal**: Create your `gentx` file by providing your mnemonic.

1.  Place the `genesis-intermediate.json` you received into your local validator directory from Stage 1.
2.  Run the `stage-3-create-gentx.sh` script. You will be prompted to paste the 24-word mnemonic phrase you saved from Stage 1.

    ```bash
    MONIKER="validator-1"
    VAL_DIR_PATH="$HOME/validator-1-files"

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

1.  Collect all `gentx-....json` files into a single directory (e.g., `./coordinator-data/gentx_collected/`). Include your own.
2.  Run the `stage-4-assemble-final-genesis.sh` script.

    ```bash
    COORDINATOR_DIR_PATH="./coordinator-data"
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

1.  **Distribute the Final Genesis**: The coordinator distributes the `genesis-final.json`. **Use a checksum to verify integrity!**

2.  **Prepare your Node's Launch Directory**: Your validator directory from Stage 1 is now your launch directory.
    *   Copy the final `genesis.json` into `config/genesis.json`.
    *   Copy the `priv_validator_key.json` into a new `tmkms/` subdirectory.
    *   Create a `config.env` file with the `P2P_PERSISTENT_PEERS` list.

3.  **Launch your Node**: Navigate to your validator directory and run the `docker-compose.validator.yml`.
    ```bash
    cd ~/validator-1-files/
    docker-compose -f /path/to/repo/deploy/multi-genesis-manual/docker-compose.validator.yml up -d
    ```
