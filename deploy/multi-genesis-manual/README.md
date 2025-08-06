# Multi-Validator Genesis Deployment (Manual Process)

This guide outlines the manual, multi-machine process for launching a new network with multiple genesis validators. It is designed for a scenario where each validator is on a separate machine and coordination is done manually.

## Overview

The process is divided into three main phases:

1.  **Phase 1: Key & Gentx Generation (All Validators)**
    Each validator operator (including the coordinator) runs a script to generate their keys and a genesis transaction (`gentx`). The output is a set of files that must be securely communicated to the coordinator.

2.  **Phase 2: Genesis Creation (Coordinator Only)**
    The coordinator collects the files from all validators, places them in a specific directory structure, and runs a script to assemble the final `genesis.json`. The coordinator then starts their node and distributes the `genesis.json` to all other validators.

3.  **Phase 3: Launching Validator Nodes (Validators Only)**
    Once they receive the final `genesis.json`, the other validator operators can configure and launch their nodes.

## Detailed Steps

### Phase 1: Generate Validator Keys and `gentx`

This step must be performed by **every** genesis validator, including the one who will act as the coordinator.

1.  **Prepare the environment**:
    Create a directory for your validator's files. For example:
    ```bash
    mkdir -p ~/validator-files
    ```

2.  **Run the generation script**:
    Use the following `docker run` command to execute the `generate-validator-files.sh` script. This command mounts your local directory into the container and runs the script, which packages all the necessary files into `~/validator-files`.

    *   Replace `~/validator-files` with the absolute path to your directory.
    *   Set the `MONIKER` environment variable to a unique name for your validator.

    ```bash
    docker run --rm -it \
        -v ~/validator-files:/output \
        -v ./deploy/multi-genesis-manual/generate-validator-files.sh:/root/generate-validator-files.sh \
        -e MONIKER="your-node-moniker" \
        ghcr.io/product-science/inferenced:0.1.21 \
        sh /root/generate-validator-files.sh
    ```
    If you encounter an architecture error (e.g., `no matching manifest`), try using the `:latest` tag for the image.

3.  **Verify the Output**:
    After the script finishes, your `~/validator-files` directory should contain:
    *   `gentx/`: A directory containing your `gentx-....json` file.
    *   `priv_validator_key.json`: The private key for your consensus node. **KEEP THIS SECRET.**
    *   `address.txt`: The public address of your cold key.

4.  **Send files to the Coordinator**:
    Securely transmit the contents of `~/validator-files/gentx/` and `~/validator-files/address.txt` to the person designated as the coordinator.

### Phase 2: Create and Distribute Genesis (Coordinator)

1.  **Collect Files**:
    Create a directory structure like this:

    ```
    ./coordinator-data/
    ├── gentx/
    │   ├── gentx-validator1.json
    │   ├── gentx-validator2.json
    │   └── gentx-coordinator.json
    └── addresses/
        ├── validator1.address
        ├── validator2.address
        └── coordinator.address
    ```
    Place the files received from all validators (and your own from Phase 1) into the `gentx` and `addresses` subdirectories.

2.  **Run the Coordinator Docker Compose**:
    Use the provided `docker-compose.coordinator.yml` to launch the coordinator.

    ```bash
    docker-compose -f deploy/multi-genesis-manual/docker-compose.coordinator.yml up
    ```
    This will start the coordinator node. The `init-docker-genesis-coordinator.sh` script will automatically create the final `genesis.json` and place it in the `./coordinator-data/final_genesis` directory on your host machine.

3.  **Distribute `genesis.json`**:
    Send the final `genesis.json` file to all other validator operators.

### Phase 3: Launch Validator Nodes

This step is for all non-coordinator validators.

1.  **Prepare your node's directory**:
    Create a directory for your node, e.g., `~/my-node`. Inside it, create `config` and `tmkms` subdirectories.
    ```bash
    mkdir -p ~/my-node/config
    mkdir -p ~/my-node/tmkms
    ```

2.  **Place Files**:
    *   Copy the final `genesis.json` received from the coordinator into `~/my-node/config/`.
    *   Copy the `priv_validator_key.json` you generated in Phase 1 into `~/my-node/config/`.
    *   Copy the `init-docker-validator.sh` script into `~/my-node/`.
    *   Create a `config.env` file (using the provided `config.env.template`) in `~/my-node/` and fill in the `P2P_PERSISTENT_PEERS` and `P2P_SEEDS` variables.

3.  **Launch your node**:
    Navigate to your `~/my-node` directory and run the validator Docker Compose.

    ```bash
    cd ~/my-node
    docker-compose -f /path/to/repo/deploy/multi-genesis-manual/docker-compose.validator.yml up -d
    ```

This will start your validator and `tmkms` services, connecting you to the network.
