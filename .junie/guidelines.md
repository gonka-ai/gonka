# Development Guidelines for Gonka Project

This document outlines the development guidelines for the Gonka project, with a special focus on considerations for AI Agents working with the codebase.

## Project Overview

Gonka is a decentralized AI infrastructure designed to optimize computational power for AI model training and inference. The project uses a novel consensus mechanism called "Proof of Work 2.0" that ensures computational resources are allocated to AI workloads rather than being wasted on securing the blockchain.

The system consists of three main components:
1. **Chain Node** - Connects to the blockchain, maintains the blockchain layer, and handles consensus
2. **API Node** - Serves as the coordination layer between the blockchain and the AI execution environment
3. **ML Node** - Handles AI workload execution: training, inference, and Proof of Work 2.0(This is currently not in this repo)

## Repository Structure

```
/client-libs        # Client script to interact with the chain
/cosmovisor         # Cosmovisor binaries
/decentralized-api  # Api node
/dev_notes          # Chain developer knowledge base
/docs               # Documentation on specific aspects of the chain
/inference-chain    # Chain node
/prepare-local      # Scripts and configs for running local chain
/testermint         # Integration tests suite
/local-test-net     # Scripts and other files for runnina a local test net with multiple nodes
```

## Guidelines for Generated Files

### Protobuf Files

**IMPORTANT**: Do not edit `.pb.go` files directly. These are generated files based on `.proto` files.

When working with protobuf definitions:

1. Edit the `.proto` files
2. Run `ignite generate proto-go` in the `inference-chain` directory to regenerate the Go code
3. For ML node protobuf definitions, refer to the [chain-protos repository](https://github.com/product-science/chain-protos/blob/main/proto/network_node/v1/network_node.proto)
4. After editing `.proto` files, copy them to the ML node and Inference Ignite repositories, and regenerate the bindings (currently not possible within this repo)

### Ignite Commands

For working with Cosmos Ignite:

**ALL** ignite commands should be run in the `inference-chain` directory, NOT the root.

- Add new store object: 
  ```
  ignite scaffold map participant reputation:int weight:int join_time:uint join_height:int last_inference_time:uint --index address --module inference --no-message
  ```
  - Include `--no-message` to prevent the store object from being modifiable by messages sent to the chain
  - Prefer snake_case naming
  
- Add new message:
  ```
  ignite scaffold message createGame black red --module checkers --response gameIndex
  ```

- Add new query:
  ```
  ignite scaffold query getGameResult gameIndex --module checkers --response result
  ```

- Modify existing store object:
  1. Change the types in the `.proto` file for the store object
  2. Run `ignite generate proto-go`

- Modify existing message:
  1. Change the types in `tx.proto`
  2. Run `ignite generate proto-go`

## Blockchain-Specific Considerations

### Avoiding Consensus Failures

Consensus failures can occur when nodes calculate the state differently. To prevent this:

1. **Don't use maps in state calculations**
   - Go's map iteration order is indeterminate
   - Use slices or arrays instead
   - If maps are necessary, implement a deterministic map

2. **Avoid randomness in state calculations**
   - All GUIDs, random numbers, and anything using randomness must be calculated outside chain state calculation
   - Any randomness in state calculations means consensus cannot be reached

3. **Don't use map iteration to generate lists or maps**
   - If needed, implement a deterministic map or iterate on a sorted list of keys

### Debugging Consensus Failures

If a consensus failure occurs:

1. Note the block height of the failure
2. Exec into a container running the node
3. Run `inferenced export --height <block height>`
4. Compare the JSON state output from different nodes to identify where states differed

## Testing Requirements

Before submitting a pull request:

1. Run unit tests and integration tests:
   ```
   make local-build
   make run-tests
   ```
2. `make run-tests` will take a very long time to run (90 minutes+), so do it only when most of the work is done.
2. Ensure all unit tests pass
3. Ensure all integration tests pass, minus known issues listed in `testermint/KNOW_ISSUES.md`

## Documentation

- Update documentation alongside code changes that affect behavior, APIs, or assumptions
- Missing documentation may delay PR approval

## Guidelines for AI Agents

When working with this codebase, AI Agents should:

1. **Understand the architecture** - Familiarize yourself with the three main components (Chain Node, API Node, ML Node) and how they interact

2. **Respect generated files** - Never modify `.pb.go` files directly; always edit the `.proto` files and regenerate

3. **Be aware of blockchain constraints** - Pay special attention to determinism in state calculations, avoiding maps and randomness

4. **Follow testing protocols** - Run unit tests before finishing. Do not run integration tests unless specifically told to

5. **Document changes thoroughly** - Provide clear explanations for any proposed modifications

6. **Consider consensus implications** - Any changes to state calculation must maintain deterministic behavior across all nodes. NEVER use maps in proto files or iterate over maps to generate parts of the state, as these are non-deterministic and will BREAK THE CHAIN.

7. **Use appropriate Ignite commands** - Follow the patterns established in the ignite_cheat_sheet.md for scaffolding new and modifying existing components. Components only need to be added using ignite if they are to be stored in the actual state of the blockchain, however.

8. **Add new files to Git** - Use `git add` on the CLI to add newly created files to Github.
9. **NEVER COMMIT FILES** - AI Agents should never commit files directly. Instead, they should provide the necessary changes and explanations, which can then be reviewed and committed by a human developer.

By following these guidelines, AI Agents can contribute effectively to the Gonka project while maintaining the integrity and stability of the system.

## Running Unit Tests During Dev
To run tests in the `inference-chain` project:
1. change to the `inference-chain` directory
2. To execute ALL tests: `go test ./...`
3. To execute tests for a specific file `go test (relative path from inference-chain)`