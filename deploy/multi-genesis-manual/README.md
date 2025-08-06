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
2.  Run the `stage-1-generate-key.sh` script via Docker. This will create your keys and place your address and private consensus key in your local directory.

    ```bash
    MONIKER="validator-1"
    PATH="~/validator-1-files"
    docker run --rm -it \
        -v "$PATH":/output \
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
2.  Place all received `address.txt` files into the `addresses_collected` directory.
3.  Run the `stage-2-create-intermediate-genesis.sh` script. This will generate the `genesis-intermediate.json`.

    ```bash
    docker run --rm -it \
        -v ./coordinator-data/addresses_collected:/root/addresses_collected \
        -v ./coordinator-data:/root/intermediate_genesis \
        -v ./deploy/multi-genesis-manual/genesis_overrides.json:/root/genesis_overrides.json \
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

    ```bash
    docker run --rm -it \
        -v ~/validator-1-files:/output \
        -e MONIKER="validator-1" \
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
    docker run --rm -it \
        -v ./coordinator-data:/root/intermediate_genesis \
        -v ./coordinator-data/gentx_collected:/root/gentx_collected \
        -v ./coordinator-data:/root/final_genesis \
        -v ./deploy/multi-genesis-manual/stage-4-assemble-final-genesis.sh:/root/stage-4.sh \
        ghcr.io/product-science/inferenced:latest \
        sh /root/stage-4.sh
    ```

### Stage 5: Launch!

1.  **Distribute the Final Genesis**: The coordinator takes the `genesis-final.json` from `./coordinator-data/` and distributes it to all validators. **Use a checksum to verify integrity!**
2.  **All Validators**: Prepare your node's directory as described in the `docker-compose.validator.yml` file's comments. Place the `genesis-final.json` as `config/genesis.json`, place your `priv_validator_key.json` in `tmkms/`, and create your `config.env`.
3.  **Launch your node** using `docker-compose -f deploy/multi-genesis-manual/docker-compose.validator.yml up -d`.
4.  **Coordinator**: The coordinator can launch their node using the same `docker-compose.validator.yml` after setting up their own directory.
