# Testermint Documentation

## Overview

**Testermint** is our custom end-to-end integration testing framework for the blockchain and decentralized API (DAPI) components of the project. It simulates realistic environments with multiple nodes, enabling deep testing of interactions that can’t be captured with isolated unit or component tests.

Testermint handles the orchestration of:

- **Docker containers** containing running versions of the blockchain node and the API binary
- **WireMock** mocks for external systems like customers or third-party APIs

---

## Justification

Much of the functionality we are building—especially the interplay between blockchain nodes, the DAPI, and proofs of compute—cannot be effectively tested in isolation. Bugs often only appear when:

- Multiple nodes are communicating and reaching consensus
- Epoch transitions are simulated
- External systems respond (or fail to respond) to requests

To address this, we built **Testermint**, a Kotlin-based test harness that:

- Runs in close-to-production environments
- Allows simulation of full chain+DAPI+mocked inference flows
- Supports deterministic test scenarios through mocked responses and scripted inputs

---

## Initialization

### Running the Full Test Suite

Before running tests, you’ll need to build the required Docker containers for both the Node (blockchain) and the API. This is done using the `make all` command.

To execute the full Testermint integration test suite:

```bash
./test-chain.sh
make run-tests
```

- `./test-chain.sh`: Builds the Docker images for both the chain Node and the DAPI for testing and launches local tests.
- `make run-tests`: Compiles the Kotlin test suite, brings up the environment, and runs the integration tests.

Test output is saved to the `testermint/logs` directory.

### Running Tests Interactively (IDE)

To write or debug tests interactively:

1. Open the `Testermint` project directory in **IntelliJ IDEA**.

2. Make sure Docker is installed and running.

3. From the root of the project, run:

   ```bash
   ./local-test-net/stop.sh
   make build-docker
   ```

   This ensures the necessary Docker containers are built and ready.

4. Load the `testermint` project in your IDE.

5. You can now:

    - Run tests individually from the IDE, or
    - Execute the full suite via:
      ```bash
      make run-tests
      ```

---

## Cluster Modeling

Testermint models a **cluster** as a collection of `LocalInferencePair` instances—each representing a single participant in the network. A cluster typically mirrors the number of clients or validators participating in a test.

### LocalInferencePair

The `LocalInferencePair` class is the fundamental building block of the Testermint simulation. It encapsulates:

1. **`ApplicationCLI`**

    - Interfaces with the blockchain node.
    - Uses Docker to connect and execute commands via the `inferenced` binary.
    - Supports issuing transactions, querying chain state, and verifying chain-level behavior.

2. **`ApplicationAPI`**

    - Interfaces with the DAPI container (API node).
    - Communicates via HTTP.
    - Used to submit inference results, retrieve compute tasks, and interact with the API's functionality.

3. **`InferenceMock`**

    - Represents the mock inference/training/validation engine.
    - Used to simulate behavior of ML compute clusters.
    - Supports programmable responses using tools like WireMock.

### Cluster Composition

Each `LocalInferencePair` corresponds to a simulated participant. A typical test involving *n* participants will spin up:

- *n* blockchain nodes
- *n* API containers
- *n* `InferenceMock` instances

These are bundled into *n* `LocalInferencePair` objects, forming a fully interactive and testable cluster environment.

### Logs

All Dockerized components—`ApplicationCLI`, `ApplicationAPI`, and `InferenceMock`—output logs to the `testermint/logs` directory. With multiple nodes and services, logs can be quite verbose. See the **Log Reading** section for strategies to interpret and filter logs efficiently.

---

## Test Flow

### Cluster Initialization with `initCluster`

Most Testermint tests begin by calling the `initCluster` method. This method is responsible for setting up a clean, consistent cluster environment.

#### What `initCluster` Does

1. **Cluster Discovery**

    - Scans for any Docker containers that are already running.
    - Attempts to identify the existing cluster topology and configuration.

2. **Verifies Default Topology**

   - Genesis node  
   - Number of Joining nodes (usually 2)

3. **Configuration: `ApplicationConfig` and `inferenceConfig`**

    - `initCluster` accepts a configuration argument of type `ApplicationConfig`.
    - If none is provided, it uses the default: `inferenceConfig`.
    - Defines:
        - App name
        - Docker images
        - Root denomination
        - Expected parameters (via the `Spec` class)

4. **Cluster Rebuilds for Consistency**

    - If the live Docker environment doesn’t match the config, the cluster is rebuilt from scratch.

### Node and Network Setup

- Nodes are initialized and connected
- Validators are registered
- Wallets are funded
- Mock responses are installed
- All nodes have equal voting power (default: 10)

This process may take time but ensures a clean and deterministic test state.

---

## Core Test Utilities

Once a test is running, several helper functions are essential:

### Block Synchronization

- `waitForNextBlock()` waits for the next block (or multiple, if a parameter is passed).

### Epoch Stage Coordination

- Epochs are short in tests (10 blocks).
- Use `waitForStage(stageName)` on a `LocalInferencePair` to wait for precise epoch stages (e.g., proof-of-compute).

### Cluster Reset Trigger

- `markNeedsReboot()` flags a `LocalInferencePair` so that the next `initCluster` will force a full rebuild.

---

## Logging

### Overview

Testermint logs are comprehensive and include:

- Blockchain node output
- API container output
- Test execution logs

> 🔥 Inference mock output is *not currently logged*.

### Log Location

- All logs go to `testermint/logs`
- **Each test has its own log file**

### Reading Logs with `lnav`

- Recommended viewer: [`lnav`](https://lnav.org)
- Custom format file: `testermint_logs.json`
- Load it with:
  ```bash
  lnav -i testermint_logs.json
  ```
From this point on, you will gain a lot of additional functionality when opening testerming logs:
1. Jumping to specific sections (see below)
2. Proper highlighting and filtering of log levels (ERROR, WARN, INFO, DEBUG, TRACE)
3. Properly rendered ANSI colors
4. Easy filtering by subsystem, pair name, system (node, dapi, test)
5. Easy search and highlighting (see lnav docs)
6. Highlighting of important log lives (such as new block heights)

### Section Navigation

- Use `{` and `}` in `lnav` to jump between marked test sections
- Current section is shown at the top of the `lnav` interface

### Filtering Logs

- Press `Tab` in `lnav` to enter filter mode
    - Press `i` to include lines by regex
    - Press `o` to exclude lines by regex

#### Subsystems

- Most Log lines are tagged with `subsystem=` identifiers
- Example: `subsystem=Stages` shows all epoch transition logs
- Find subsystem definitions in: `inference-chain/x/inference/types/logging.go`
You can also use the consts in `logging.go` to see exactly where specific subsytems are used.
---
