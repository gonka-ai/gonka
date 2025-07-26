# MLNode (Hardware Node) Lifecycle and Model Management

## Overview

This document describes how MLNodes (also called Hardware Nodes) are registered, managed, and synchronized between the decentralized API nodes and the blockchain. It details the complete lifecycle from initial node configuration through blockchain registration and model assignment.

## MLNode Definition

An MLNode is a computational resource that can execute AI inference and participate in Proof-of-Compute operations. Each MLNode has:
- Unique identifier (LocalId)
- Hardware specifications (GPU/CPU types and counts)
- Network configuration (host, ports)
- Model capabilities (list of supported AI models)
- Operational status (INFERENCE, POC, TRAINING, etc.)

## MLNode Registration Flow

### Phase 1: Initial Configuration and Loading

**Configuration Sources:**
MLNodes are initially defined in the API node configuration files managed by the ConfigManager in `decentralized-api/apiconfig`. These configurations specify the basic node parameters including host, ports, hardware specifications, and most importantly, the supported models.

**Startup Loading Process:**
During API node startup in `decentralized-api/main.go`, the system loads all configured MLNodes through the `LoadNodeToBroker` function. This process reads the configuration and queues registration commands `RegisterNode` command for each node, ensuring all configured MLNodes are available when the API node begins operation.

**Model Source in Configuration:**
The models listed in MLNode configurations are arbitrary model names/identifiers chosen by the node operator. These are typically model names like "Qwen/Qwen2.5-7B-Instruct" or similar identifiers that correspond to actual AI models the node can run. At this stage, there is no validation against the blockchain governance registry.

### Phase 2: Broker Registration

**Local Registration Process:**
The Broker in `decentralized-api/broker/broker.go` serves as the central coordinator for all MLNode operations. When nodes are registered through the `RegisterNode.Execute` command, they are stored in an in-memory map with their complete configuration and operational state. This creates the local registry of available computational resources. 

**Model Processing and Storage Structure:**
In the `RegisterNode.Execute`, model configurations from the initial setup are converted and stored in the Node structure. The `Models` field is a `map[string]ModelArgs` where:

- **Keys**: Model identifiers (strings like "Qwen/Qwen2.5-7B-Instruct" or "Qwen/QwQ-32B")
- **Values**: `ModelArgs` structures containing execution arguments

**ModelArgs Structure:**
The `ModelArgs` type is defined in `decentralized-api/broker/broker.go` as a structure containing an `Args` field that holds an array of strings for execution arguments.

**Configuration Examples:**
Real examples from the codebase show how models are configured:

- **Single model with quantization**: An MLNode named "mlnode1" hosts the "Qwen/Qwen2.5-7B-Instruct" model with arguments for FP8 quantization
- **Model with multiple arguments**: An MLNode named "mlnode2" hosts the "Qwen/QwQ-32B" model with both quantization and KV cache quantization arguments

**Internal Storage During Registration:**
In the `RegisterNode.Execute` method of `decentralized-api/broker/node_admin_commands.go`, the models are processed by creating a new map where each model identifier from the configuration is paired with its corresponding ModelArgs structure containing the execution arguments.

This creates an in-memory representation where each model identifier maps to its specific execution arguments, enabling the broker to launch models with the correct parameters when needed.

**Node and NodeState Object Creation:**
The `RegisterNode.Execute` method creates both `Node` and `NodeState` objects:

- **Node Object**: Contains the hardware specifications, network configuration, model capabilities, and other operational parameters
- **NodeState Object**: Tracks the operational status, failure reasons, lock counts for concurrent usage, and intended vs actual states
- **NodeWithState**: Combines both Node and NodeState into a single structure that gets added to the broker's nodes map

**State Management:**
Each MLNode gets associated with a NodeState that tracks operational status, failure reasons, lock counts for concurrent usage, and intended vs actual states. This state management enables the broker to coordinate between different operational modes.

### Phase 3: Runtime Node Addition

**Administrative Interface:**
Beyond startup configuration, MLNodes can be added at runtime through administrative endpoints in `decentralized-api/internal/server/admin/node_handlers.go`. The `addNode` and `addNodes` functions allow operators to dynamically expand their computational capacity.

**Dynamic Registration Flow:**
Runtime node addition follows the same broker registration process using the `RegisterNode` command but also updates the persistent configuration through the ConfigManager. This ensures that dynamically added nodes persist across API node restarts.

**Model Validation During Addition:**
When nodes are added through administrative interfaces, the model identifiers they declare are still arbitrary strings at this point. The validation against blockchain governance models happens later in the synchronization process.

## Blockchain Synchronization Process

### Automatic Synchronization

**Background Sync Worker:**
The `nodeSyncWorker` function in `decentralized-api/broker/broker.go` runs every 60 seconds to maintain consistency between local MLNode state and blockchain records. This ensures that the blockchain has current information about each participant's computational capabilities.

**Diff Calculation Process:**
The synchronization process queries the current blockchain state through `QueryHardwareNodes` and compares it with local node states using the `calculateNodesDiff` function. This produces a minimal set of changes needed to bring blockchain state in sync with local reality.

**Model ID Conversion:**
During synchronization, the `convertInferenceNodeToHardwareNode` function transforms local Node structures into blockchain-compatible HardwareNode messages. The model names are extracted from the local Models map, sorted for consistency, and included in the Models array of the HardwareNode structure.

### Blockchain Transaction Submission

**Hardware Diff Submission:**
When differences are detected, the system submits a `MsgSubmitHardwareDiff` transaction containing arrays of new/modified and removed hardware nodes. This message is processed by the blockchain's message server in `inference-chain/x/inference/keeper/msg_server_submit_hardware_diff.go`.

**Model Validation on Chain:**
The blockchain message server should validate that all model identifiers in incoming hardware node updates correspond to models that exist in the governance registry. Invalid model identifiers should cause the transaction to be rejected with clear error messages.

**Persistent Storage:**
Successfully validated hardware nodes are stored in the blockchain state through the HardwareNode keeper functions in `inference-chain/x/inference/keeper/hardware_node.go`. This creates the authoritative record of each participant's computational capabilities and model support.

## Model ID Propagation Through the System

### Origin Points

**Local Configuration:**
Model identifiers originate from MLNode configuration files where operators specify which AI models their hardware can execute. These are arbitrary strings chosen by operators, typically matching standard model names from repositories like Hugging Face.

**Governance Registry:**
The authoritative source of valid model identifiers is the governance registry managed through `inference-chain/x/inference/keeper/model.go`. Models are added to this registry through governance proposals processed by `RegisterModel` message handlers.

### Validation Points

**Blockchain Validation:**
Currently, the `MsgSubmitHardwareDiff` handler in `inference-chain/x/inference/keeper/msg_server_submit_hardware_diff.go` does NOT validate model identifiers against the governance registry. The current implementation simply accepts and stores whatever model identifiers are provided in hardware node updates.

**Proposed Enhancement:** The primary validation point should be enhanced in the `MsgSubmitHardwareDiff` handler to check incoming hardware node updates against the governance model registry using `GetAllModels` or the proposed `GetGovernanceModels` function.

**API Node Validation:**
Currently, there is no model validation at MLNode registration time in the broker and administrative interfaces. 

**Proposed Enhancement:** 
Future enhancements should add validation, checking model identifiers against the blockchain governance registry before accepting node configurations.

### Propagation Flow

**Configuration to Broker:**
Model identifiers flow from configuration files into the Broker's in-memory node registry during the registration process. At this stage, they remain unvalidated strings.

**Broker to Blockchain:**
During synchronization, model identifiers are extracted from broker node states and included in blockchain transactions. Currently, no validation occurs at this stage - nodes with any model references are accepted.

**Proposed Enhancement:** This synchronization stage should include validation, rejecting nodes with invalid model references before transaction submission.

**Blockchain to Epoch Groups:**
Model identifiers from hardware nodes (whether valid or invalid) are used during epoch group formation in `setModelsForParticipants` within `inference-chain/x/inference/module/module.go`. The `getAllModels` function extracts model lists from participant hardware nodes for assignment to ActiveParticipants.

**Epoch Group Assignment:**
During epoch transitions, participants are assigned to model-specific subgroups based on their declared model capabilities. The `AddMember` function in `inference-chain/x/inference/epochgroup/epoch_group.go` uses these model lists to organize participants into appropriate computational groups.
