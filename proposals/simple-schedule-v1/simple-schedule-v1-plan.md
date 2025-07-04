# Multi Model and GPU Uptime System - Task Plan

## Prerequisite Reading

Before starting implementation, please read the following documents to understand the full context of the changes:
- The main proposal: `proposals/simple-schedule-v1/readme.md`
- The current flow, and how models are used: `proposals/simple-schedule-v1/models-flow.md`
- Models in registration process: `proposals/simple-schedule-v1/models-registration.md`
- Models in MLNodes lifecycle: `proposals/simple-schedule-v1/models-for-mlnode.md`
- Models usage in inference: `proposals/simple-schedule-v1/models-for-inference.md`

## How to Use This Task List

### Workflow
- **Focus on a single task**: Please work on only one task at a time to ensure clarity and quality. Avoid implementing parts of future tasks.
- **Request a review**: Once a task's implementation is complete, change its status to `[?] - Review` and wait for my confirmation.
- **Update all usages**: If a function or variable is renamed, find and update all its references throughout the codebase.
- **Build after each task**: After each task is completed, build the project to ensure there are no compilation errors.
- **Test after each section**: After completing all tasks in a section, run the corresponding tests to verify the functionality.
- **Wait for completion**: After I confirm the review, mark the task as `[x] - Finished`, add a **Result** section summarizing the changes, and then move on to the next one.

### Build & Test Commands
- **Build Inference Chain**: From the project root, run `make node-local-build`
- **Build Decentralized API**: From the project root, run `make api-local-build`

### Status Indicators
- `[ ]` **Not Started** - Task has not been initiated
- `[~]` **In Progress** - Task is currently being worked on
- `[?]` **Review** - Task completed, requires review/testing
- `[x]` **Finished** - Task completed and verified

### Task Organization
Tasks are organized by implementation area and numbered for easy reference. Dependencies are noted where critical. Complete tasks in order.

### Task Format
Each task includes:
- **What**: Clear description of work to be done
- **Where**: Specific files/locations to modify
- **Why**: Brief context of purpose when not obvious

## Task List

### Section 1: Enhanced Model Structure and Parameters

#### 1.1 Protobuf Model Enhancement
- **Task**: [x] Add new fields to Model protobuf structure
- **What**: Add `HFRepo`, `HFCommit`, `ModelArgs` (repeated string), `VRAM`, and `ThroughputPerNonce` fields to Model message. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/model.pb.go`
- **Dependencies**: None

#### 1.2 Model Keeper Functions Enhancement  
- **Task**: [x] Enhance model keeper functions with comprehensive metadata support
- **What**: Update `SetModel`, rename `GetAllModels` to `GetGovernanceModels`, and add `GetGovernanceModel` function
- **Where**: `inference-chain/x/inference/keeper/model.go`
- **Dependencies**: 1.1

#### 1.3 Model Registration Message Handler Update
- **Task**: [x] Update model registration to handle all new parameters
- **What**: Modify `RegisterModel` to process and validate all new model fields including throughput
- **Where**: `inference-chain/x/inference/keeper/msg_server_register_model.go`
- **Result**:
  - Added `hf_repo`, `hf_commit`, `model_args`, `v_ram`, and `throughput_per_nonce` to the `MsgRegisterModel` message in `inference-chain/proto/inference/inference/tx.proto`.
  - Regenerated protobuf files using `ignite generate proto-go`.
  - Updated the `RegisterModel` message handler in `inference-chain/x/inference/keeper/msg_server_register_model.go` to handle the new fields.
  - The `inference-chain` build was successful after the changes.
- **Dependencies**: 1.1, 1.2

#### 1.4 Genesis Model Initialization Update
- **Task**: [x] Update genesis handling for enhanced model structure
- **What**: Modify genesis initialization and export to handle new model fields. **Note**: `throughput_per_nonce` is temporarily set to 1,000,000 for both models in the genesis files and should be properly computed later.
- **Where**: `inference-chain/x/inference/module/genesis.go`
- **Result**:
  - Updated `genesis_test.go` to include a test case for models with the new fields.
  - Added the "Qwen/QwQ-32B" and "Qwen/Qwen2.5-7B-Instruct" models to all `genesis-overrides.json` files.
  - The `inference-chain` build and tests were successful after the changes.
- **Dependencies**: 1.1, 1.2

### Section 2: MLNode Model Assignment Validation

#### 2.1 Model Validation Functions
- **Task**: [x] Add governance model validation functions
- **What**: Create `IsValidGovernanceModel` function to check model ID existence in governance registry
- **Where**: `inference-chain/x/inference/keeper/model.go`
- **Result**:
  - Added the `IsValidGovernanceModel` function to `inference-chain/x/inference/keeper/model.go`.
  - The `inference-chain` build was successful after the changes.
- **Dependencies**: 2.1

#### 2.2 Hardware Node Validation Functions
- **Task**: [x] Add hardware node validation and query functions
- **What**: Create `GetNodesForModel` functions
- **Where**: `inference-chain/x/inference/keeper/hardware_node.go`
- **Result**:
  - Added the `GetNodesForModel` function to `inference-chain/x/inference/keeper/hardware_node.go`.
  - The `inference-chain` build was successful after the changes.
- **Dependencies**: 2.1

#### 2.3 Hardware Diff Message Validation
- **Task**: [x] Add model validation to hardware diff submission
- **What**: Enhance `MsgSubmitHardwareDiff` to validate all model IDs against governance registry
- **Where**: `inference-chain/x/inference/keeper/msg_server_submit_hardware_diff.go`
- **Result**:
  - Added logic to `MsgSubmitHardwareDiff` to validate that all models in a hardware diff submission exist in the governance registry.
  - Defined a new `ErrInvalidModel` in `x/inference/types/errors.go`.
  - The `inference-chain` build was successful after the changes.
- **Dependencies**: 2.1, 2.2

#### 2.4 API Node Registration Validation
- **Task**: [x] Add model validation to node registration
- **What**: Enhance `RegisterNode Execute` to validate model IDs during node registration
- **Where**: `decentralized-api/broker/node_admin_commands.go`
- **Result**:
  - Enhanced `RegisterNode.Execute` in `decentralized-api/broker/node_admin_commands.go` to validate model IDs against the governance model registry.
  - Added the `GetGovernanceModels` method to the `BrokerChainBridge` interface in `decentralized-api/broker/broker.go` to allow the API to query the chain for governance models.
  - Refactored the `RegisterNode` command.
  - The `decentralized-api` was successfully built with `make api-local-build` after all changes.
- **Dependencies**: 2.1

### Section 3: Model Parameter Snapshots in Epoch Groups

#### 3.1 Epoch Group Data Protobuf Enhancement
- **Task**: [x] Add model snapshot field to EpochGroupData
- **What**: Add `model_snapshot` (Model) field to EpochGroupData protobuf. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/epoch_group_data.pb.go`
- **Result**:
  - Added the `model_snapshot` field to the `EpochGroupData` message in `inference-chain/proto/inference/inference/epoch_group_data.proto`.
  - Regenerated the protobuf files using `ignite generate proto-go`.
- **Dependencies**: 1.1

#### 3.2 Epoch Model Management Functions
- **Task**: [x] Create epoch model management functions
- **What**: Create `GetEpochModel` function in new epoch_models.go file
- **Where**: `inference-chain/x/inference/keeper/epoch_models.go`
- **Result**:
  - Created a new file `inference-chain/x/inference/keeper/epoch_models.go`.
  - Added the `GetEpochModel` function to retrieve model snapshots from epoch data.
  - Defined a new `ErrModelSnapshotNotFound` error in `x/inference/types/errors.go`.
  - The `inference-chain` build was successful after the changes.
- **Dependencies**: 3.1

#### 3.3 Epoch Group Formation Enhancement
- **Task**: [x] Update epoch group formation to store model snapshots
- **What**: Modify `createNewEpochSubGroup` and `CreateSubGroup` to store complete Model objects
- **Where**: `inference-chain/x/inference/epochgroup/epoch_group.go`
- **Result**:
  - Modified `epoch_group.go` to snapshot the full `Model` object into `ModelSnapshot` during subgroup creation.
  - Introduced the `ModelKeeper` interface and added it to the `EpochGroup` struct to facilitate fetching governance models.
  - Refactored the original `GetSubGroup` into two distinct methods for clarity and safety: a read-only `GetSubGroup` and a write-capable `GetOrCreateSubGroup`.
  - Updated callers (`addToModelGroups`, `GetRandomMemberForModel`) to use the appropriate new functions.
  - Simplified `keeper.GetEpochModel` to use the new safe, read-only `GetSubGroup` function.
  - The `inference-chain` build was successful after all refactoring.
- **Dependencies**: 3.1, 3.2

#### 3.4 Current Models API Update
- **Task**: [x] Update models API to use epoch snapshots
- **What**: Modify `getModels` to query epoch model snapshots instead of governance models
- **Where**: `decentralized-api/internal/server/public/get_models_handler.go`
- **Result**:
  - The `getModels` handler in the public API was updated to return models active in the current epoch.
  - It now fetches the parent epoch group, then iterates through the model IDs to query the `EpochGroupData` for each subgroup.
  - The `ModelSnapshot` from each subgroup is extracted to build the final list of active models.
  - This approach reuses the existing `EpochGroupData` query, avoiding the need for a new chain-level query.
  - The `decentralized-api` build was successful.
  - **Note**: This handler will be refactored in task 4.6 to use a cached model list from the Broker instead of querying the chain directly.
- **Dependencies**: 3.1, 3.2, 3.3

#### 3.5 Current Pricing API Update
- **Task**: [x] Update pricing API to use epoch snapshots  
- **What**: Modify `getPricing` to use epoch model snapshots for price calculations
- **Where**: `decentralized-api/internal/server/public/get_pricing_handler.go`
- **Result**:
  - The `getPricing` handler was updated to use epoch snapshots for its calculations.
  - It now fetches active models for the current epoch and uses their snapshotted `UnitsOfComputePerToken` for consistent pricing.
  - The `decentralized-api` build was successful.
- **Dependencies**: 3.1, 3.2, 3.3

#### 3.6 Governance Models API Creation
- **Task**: [x] Create new governance models API endpoint
- **What**: Create `getGovernanceModels` function and handler for latest governance models
- **Where**: `decentralized-api/internal/server/public/get_governance_models_handler.go`
- **Result**:
  - The `getGovernanceModels` function was created and consolidated into `get_models_handler.go`.
  - The `/v1/governance/models` route was added to the API server.
- **Dependencies**: 1.2

#### 3.7 Governance Pricing API Creation
- **Task**: [x] Create new governance pricing API endpoint
- **What**: Create `getGovernancePricing` function and handler for upcoming pricing
- **Where**: `decentralized-api/internal/server/public/get_governance_pricing_handler.go`
- **Result**:
  - The `getGovernancePricing` function was created and consolidated into `get_pricing_handler.go`.
- **Dependencies**: 1.2

#### 3.8 API Routes Registration
- **Task**: [x] Register new API routes for governance endpoints
- **What**: Add routes for `/v1/governance/models` and `/v1/governance/pricing`
- **Where**: API router configuration files
- **Result**:
  - The `/v1/governance/models` and `/v1/governance/pricing` routes were added to the API server.
- **Dependencies**: 3.6, 3.7

### Section 4: MLNode Snapshots in Epoch Groups

#### 4.1 MLNode Info Protobuf Structure
- **Task**: [x] Create MLNodeInfo protobuf structure
- **What**: Create `MLNodeInfo` message with `node_id`, `throughput`, and `poc_weight` fields. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/epoch_group_data.pb.go`
- **Result**:
  - The `MLNodeInfo` message was added to `epoch_group_data.proto` with the specified fields.
  - Protobuf files were regenerated successfully.
- **Dependencies**: 3.1

#### 4.2 Epoch Group MLNode Fields
- **Task**: [x] Add MLNode fields to EpochGroupData
- **What**: Add `ml_nodes` (repeated MLNodeInfo) field organized per participant. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/epoch_group_data.pb.go`
- **Result**:
  - The `ml_nodes` field was added to the `ValidationWeight` message in `epoch_group_data.proto` to associate ML nodes with each participant.
  - Protobuf files were regenerated successfully.
- **Dependencies**: 4.1

#### 4.3 Epoch Group MLNode Management Functions
- **Task**: [x] Add MLNode management to epoch group formation
- **What**: Create `StoreMLNodeInfo` function and enhance member addition to snapshot MLNode configs
- **Where**: `inference-chain/x/inference/epochgroup/epoch_group.go`
- **Result**:
  - Created a `storeMLNodeInfo` function in `epoch_group.go` to fetch ML nodes for a given participant and model.
  - Enhanced `updateEpochGroupWithNewMember` to call this function and store the `MLNodeInfo` in the `ValidationWeight` structure, effectively snapshotting the nodes.
  - Refactored multiple keeper interfaces (`ParticipantKeeper`, `HardwareNodeKeeper`) and their test mocks to support the changes.
  - The `inference-chain` was successfully built after the changes.
- **Dependencies**: 4.1, 4.2

#### 4.4 Module MLNode Snapshotting
- **Task**: [x] Add MLNode snapshotting to module functions
- **What**: Enhance `setModelsForParticipants` to snapshot hardware node configurations
- **Where**: `inference-chain/x/inference/module/module.go`
- **Result**:
  - The necessary snapshotting logic was already implemented in `epochgroup.go` as part of task 4.3. The `updateEpochGroupWithNewMember` function, called during member addition, now handles the snapshotting of ML node configurations into the epoch data. No further changes were required.
- **Dependencies**: 4.2, 4.3

#### 4.5 API Node State MLNode Fields
- **Task**: [x] Add epoch MLNode fields to NodeState
- **What**: Add `EpochModels` and `EpochMLNodes` maps to NodeState structure
- **Where**: `decentralized-api/broker/broker.go`
- **Result**:
  - Added `EpochModels` and `EpochMLNodes` map fields to the `NodeState` struct.
  - Initialized the new maps during node registration in the `LoadNodeToBroker` function to prevent nil map panics.
- **Dependencies**: 4.1

#### 4.6 Epoch Data Update Functions
- **Task**: [x] Create broker epoch data update functions
- **What**: Create `UpdateNodeWithEpochData` and `MergeModelArgs` functions.
- **Where**: `decentralized-api/broker/broker.go`
- **Result**:
  - The `MLNodeInfo` message was added to `epoch_group_data.proto` and `EpochModels`/`EpochMLNodes` maps were added to the broker's `NodeState`.
  - The `UpdateNodeWithEpochData` function was created in the broker to synchronize epoch data from the chain to the `NodeState` maps.
- **Dependencies**: 4.5

#### 4.7 New Block Dispatcher Enhancement
- **Task**: [x] Add epoch data sync to block dispatcher
- **What**: Enhance `handlePhaseTransitions` to call `UpdateNodeWithEpochData`
- **Where**: `decentralized-api/internal/event_listener/new_block_dispatcher.go`
  - The `handlePhaseTransitions` function in the block dispatcher was enhanced to call `UpdateNodeWithEpochData` on phase changes.
- **Dependencies**: 4.6

#### 4.8 Node Worker Commands Update
- **Task**: [x] Update inference commands to use epoch models
- **What**: Modify `InferenceUpNodeCommand.Execute` to use `EpochModels` instead of broker models
- **Where**: `decentralized-api/broker/node_worker_commands.go`
- **Result**:
  - The `InferenceUpNodeCommand` was updated to use the `EpochModels` and `EpochMLNodes` from the node's state, ensuring nodes load the correct models for the current epoch.
  - The test suite was updated to handle the new interfaces and logic, including fixes for mock data and nil map panics. All tests are passing.
- **Dependencies**: 4.5, 4.6

### Section 5: Per-MLNode PoC Tracking System

#### 5.1 PoCBatch Protobuf Enhancement
- **Task**: [x] Add NodeId field to PoCBatch structure
- **What**: Add `NodeId` field to PoCBatch protobuf to track which MLNode generated the batch. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/pocbatch.pb.go`
- **Result**:
  - Added the `node_id` string field to the `PoCBatch` message in `inference-chain/proto/inference/inference/pocbatch.proto`.
  - Added the `node_id` string field to the `MsgSubmitPocBatch` message in `inference-chain/proto/inference/inference/tx.proto`.
  - Regenerated the protobuf Go files using `ignite generate proto-go` to apply the changes.
- **Dependencies**: None

#### 5.1.1 dAPI PoC Batch Handling Update
- **Task**: [x] Update dAPI to process Node ID for PoC Batches
- **What**: Enhance the dAPI to identify which MLNode generated a PoC batch. After `NodeNum` is added to the `ProofBatch` payload from the ML node, the `postGeneratedBatches` handler will use it to query the Broker for the node's string `Id`. This `Id` will then be added to the `MsgSubmitPocBatch` before it's sent to the chain. For backward compatibility, if the `NodeNum` field is missing or zero, the handler should submit an empty string for the `NodeId`.
- **Where**: 
  - `decentralized-api/mlnodeclient/client.go` (or wherever `ProofBatch` is defined)
  - `decentralized-api/internal/server/mlnode/post_generated_batches_handler.go`
- **Result**:
  - Added a `NodeNum` field to the `ProofBatch` struct in `decentralized-api/mlnodeclient/poc.go`.
  - Injected the `Broker` into the `mlnode.Server` to provide access to node information.
  - Added a `GetNodeByNodeNum` helper method to the `Broker`.
  - The `postGeneratedBatches` handler now correctly reads the `NodeNum`, retrieves the full node `Id`, and adds it to the `MsgSubmitPocBatch` sent to the chain.
  - Updated Go module dependencies with `go mod tidy` to resolve compilation errors.
- **Dependencies**: 5.1
- **Note**: This task may require providing the `postGeneratedBatches` handler with access to the Broker instance via dependency injection.

#### 5.2 PoC Batch Keeper Functions
- **Task**: [x] Add per-MLNode PoC query functions
- **What**: Create `GetPoCBatchesForNode`, `GetPoCBatchesForModel`, `CalculateNodeWeight`, `CalculateModelPower`
- **Where**: `inference-chain/x/inference/keeper/poc_batch.go`
- **Dependencies**: 5.1
- **Note**: Implementation of these functions is deferred for now.

#### 5.3 Epoch Group Total Throughput Field
- **Task**: [x] Add total throughput tracking to EpochGroupData
- **What**: Add `total_throughput` field to EpochGroupData protobuf. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/epoch_group_data.pb.go`
- **Result**:
  - Added the `total_throughput` int64 field to the `EpochGroupData` message in `epoch_group_data.proto`.
  - Regenerated the protobuf Go files successfully.
- **Dependencies**: 4.2

#### 5.4 Chain Validation Weight Calculation Update
- **Task**: [x] Enhance weight calculation for per-MLNode tracking
- **What**: Modify `ComputeNewWeights` and `validatedParticipant` to record per-MLNode poc_weight in active participants (use repeated MLNodeInfo)
- **Where**: `inference-chain/x/inference/module/chainvalidation.go`
- **Result**:
  - Added a `repeated MLNodeInfo ml_nodes` field to the `ActiveParticipant` protobuf message.
  - The `calculateParticipantWeight` function was refactored to return a map of weights per `NodeId` and the participant's total weight.
  - The `validatedParticipant` function was updated to use this new data structure, populating the `ActiveParticipant.MlNodes` slice with the per-node weights.
  - Regenerated the protobuf Go files to apply the changes.
- **Dependencies**: 5.2

#### 5.5 Epoch Group Member Update Enhancement
- **Task**: [x] Add MLNode weight and throughput calculation to epoch group updates
- **What**: Enhance `updateEpochGroupWithNewMember` to calculate poc_weight and total_throughput
- **Where**: `inference-chain/x/inference/epochgroup/epoch_group.go`
- **Result**:
  - Added the `MlNodes` field to the `EpochMember` struct to carry detailed node information from the `ActiveParticipant`.
  - Updated `NewEpochMemberFromActiveParticipant` to correctly populate this new field.
  - Enhanced `updateEpochGroupWithNewMember` and its helper `storeMLNodeInfo` to:
    - Read the `PocWeight` from the member's `MLNodeInfo` and store it in the `ValidationWeight` for the subgroup.
    - Calculate the `TotalThroughput` for the model's subgroup by summing the throughput of all associated ML nodes (currently stubbed to 0 pending hardware node updates).
  - The `inference-chain` build was successful.
- **Dependencies**: 5.3, 5.4

#### 5.6 ActiveParticipant MLNode Structure Reorganization
- **Task**: [x] Restructure ActiveParticipant to use double repeated MLNodes arrays
- **What**: Change `ActiveParticipant` to have `repeated repeated MLNodeInfo ml_nodes` where each inner array corresponds to a model at the same index, allowing MLNodes to be organized by model
- **Where**: `inference-chain/proto/inference/inference/activeparticipants.proto`
- **Note**: Use `ignite generate proto-go` to regenerate protobuf files after protobuf changes
- **Result**:
  - Created a new `ModelMLNodes` wrapper message containing `repeated MLNodeInfo ml_nodes` to enable the double repeated structure.
  - Modified the `ActiveParticipant` message to use `repeated ModelMLNodes ml_nodes` instead of `repeated MLNodeInfo ml_nodes`.
  - Added detailed comment explaining that each `ModelMLNodes` corresponds to a model at the same index.
  - Successfully regenerated protobuf Go files using `ignite generate proto-go`.
  - The structural change enables model-based MLNode organization where each inner array corresponds to MLNodes supporting a specific governance model.
- **Dependencies**: 5.5

#### 5.7 EpochMember MLNode Structure Update  
- **Task**: [x] Update EpochMember to match ActiveParticipant MLNode structure
- **What**: Change `EpochMember` to have `repeated repeated MLNodeInfo ml_nodes` structure matching ActiveParticipant and update `NewEpochMemberFromActiveParticipant` to properly copy the double repeated structure
- **Where**: `inference-chain/x/inference/epochgroup/epoch_group.go`
- **Result**:
  - Updated the `EpochMember` struct to use `[]*types.ModelMLNodes` instead of `[]*types.MLNodeInfo` for the `MlNodes` field.
  - Modified `NewEpochMemberFromActiveParticipant` to properly copy the double repeated structure from `ActiveParticipant`.
  - Updated `storeMLNodeInfo` function to handle the double repeated structure by iterating through both the outer `ModelMLNodes` arrays and inner `MLNodeInfo` arrays to build the `pocWeightMap`.
  - The changes ensure `EpochMember` structure matches the new `ActiveParticipant` structure for consistent MLNode organization.
- **Dependencies**: 5.6

#### 5.8 Weight Calculation MLNode Array Population
- **Task**: [x] Modify weight calculation to populate first MLNode array
- **What**: Update `calculateParticipantWeight` and related functions to add all MLNodes to the first array (index 0) in the double repeated structure during weight calculation phase
- **Where**: `inference-chain/x/inference/module/chainvalidation.go`
- **Result**:
  - Modified the `validatedParticipant` function in `chainvalidation.go` to create the double repeated MLNode structure.
  - All MLNodes are now populated in the first array (index 0) using a `ModelMLNodes` wrapper containing the individual `MLNodeInfo` objects.
  - Created `firstMLNodeArray` as a `ModelMLNodes` structure and wrapped it in `modelMLNodesArray` slice for the `ActiveParticipant.MlNodes` field.
  - This establishes the foundation for the model-based distribution that will be implemented in subsequent tasks.
- **Dependencies**: 5.7

#### 5.9 Model-Based MLNode Distribution in setModelsForParticipants
- **Task**: [x] Implement governance model iteration and MLNode sorting
- **What**: Modify `setModelsForParticipants` to:
  - Iterate through governance models instead of HardwareNode models
  - For each governance model, pick the first available MLNode from the first array that supports that model
  - Move the selected MLNode to the corresponding model index array 
  - Keep remaining unassigned MLNodes in the final array (governance_models_count + 1)
- **Where**: `inference-chain/x/inference/module/module.go`
- **Result**:
  - Completely restructured `setModelsForParticipants` to implement model-based MLNode distribution.
  - The function now gets governance models first and iterates through them instead of hardware node models.
  - Implemented logic to reorganize MLNodes from the first array (index 0) into model-specific arrays:
    - For each governance model, finds the first available MLNode that supports it
    - Moves selected MLNode to the corresponding model index array
    - Tracks assigned MLNodes to prevent double assignment
    - Builds list of supported governance models for each participant
  - Remaining unassigned MLNodes are placed in the overflow array at index (governance_models_count + 1).
  - Added `nodeSupportsModel` helper function to check if a specific MLNode supports a given governance model.
  - The double repeated structure now correctly maps: `p.Models[i]` corresponds to `p.MlNodes[i]` for each governance model.
  - Removed the old `getAllModels` function as it's no longer needed.
- **Dependencies**: 5.8

#### 5.10 Validation Weight MLNode Structure Update
- **Task**: [x] Update ValidationWeight to support model-indexed MLNode arrays
- **What**: Modify how `ValidationWeight.MlNodes` is populated in `updateEpochGroupWithNewMember` to properly handle the new double repeated structure from EpochMember, and update `addToModelGroups` to keep not only one model but the corresponding array of MLNodes for each model
- **Where**: `inference-chain/x/inference/epochgroup/epoch_group.go`  
- **Result**:
  - The `storeMLNodeInfo` function already correctly handles the double repeated structure by finding the model index in `member.Models` and returning the corresponding MLNode array from `member.MlNodes[modelIndex]`.
  - The `addToModelGroups` function properly copies only the MLNode array for the specific model being processed.
  - The `updateEpochGroupWithNewMember` function calls `storeMLNodeInfo` with the correct `modelId`, ensuring ValidationWeight gets populated with only the MLNodes that support that specific model subgroup.
  - The ValidationWeight structure correctly uses `repeated MLNodeInfo ml_nodes` since each model subgroup should only contain MLNodes supporting that specific model.
- **Dependencies**: 5.9

#### 5.11 PoC Weight Distribution and Aggregation Enhancement
- **Task**: [x] Handle PoC weight distribution for mixed batch types and multiple batches per node
- **What**: Enhance PoC weight calculation and distribution to:
  - **Legacy Batch Handling**: Use empty string `""` instead of `"unknown"` for batches without NodeId
  - **Legacy Weight Distribution**: Add function in `setModelsForParticipants` to detect MLNodes with empty NodeId, remove them, and distribute their weight evenly among actual hardware nodes
  - **Multi-Batch Aggregation**: Ensure multiple batches per node are properly accumulated (already using `+=`)
  - **Mixed Scenario Support**: Handle participants with both legacy batches (no NodeId) and new batches (with NodeId) in same epoch
- **Where**: 
  - `inference-chain/x/inference/module/chainvalidation.go` - `calculateParticipantWeight` function (change "unknown" to "")
  - `inference-chain/x/inference/module/module.go` - `setModelsForParticipants` function (add legacy weight distribution)
- **Result**:
  - **Updated `calculateParticipantWeight`**: Changed legacy batch handling to use empty string `""` instead of `"unknown"` for batches without NodeId, ensuring cleaner identification of legacy entries.
  - **Added `distributeLegacyWeight` function**: New simple function in `module.go` that processes legacy PoC weight distribution:
    - **Early Processing**: Called immediately after copying `originalMLNodes` from the first array, before model assignment logic
    - **Legacy Detection**: Finds MLNode with empty NodeId (legacy batches) and removes it from the list
    - **Fair Distribution**: Calculates `weightPerNode = totalWeight / numNodes` and distributes remainder by giving +1 to first nodes until remainder is over
    - **Smart Merging**: Adds distributed weight to existing MLNodes with matching NodeIds or creates new MLNode entries as needed
    - **Clean Return**: Returns updated `originalMLNodes` list ready for model assignment processing
  - **Simplified Integration**: The function processes the MLNode list early in `setModelsForParticipants`, making the subsequent model assignment logic work with clean, properly distributed weights.
  - **Backward Compatibility**: System seamlessly handles mixed scenarios with both legacy batches (no NodeId) and new per-node batches in the same epoch.
  - **Weight Preservation**: Multi-batch aggregation continues to work correctly with the existing `+=` operator in `calculateParticipantWeight`.
- **Dependencies**: 5.8, 5.9, 5.10

### Section 6: MLNode Uptime Management System

#### 6.1 MLNode Allocation Protobuf Types
- **Task**: [~] Create timeslot allocation protobuf types
- **What**: Create enum for PRE_POC_SLOT, POC_SLOT and timeslot allocation structures. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/epoch_group_data.pb.go` (add enum to existing epoch_group_data.proto)
- **Dependencies**: None

#### 6.2 MLNode Timeslot Fields
- **Task**: [~] Add timeslot allocation to MLNodeInfo
- **What**: Add `timeslot_allocation` (repeated boolean) field to MLNodeInfo. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/epoch_group_data.pb.go` (update to existing MLNodeInfo)
- **Dependencies**: 6.1, 4.1

#### 6.2.1 Model-Based MLNode Distribution in setModelsForParticipants
- **Task**: [~] Timeslots initial allocation in MLNode
- **What**: Modify `setModelsForParticipants` to:
  - Add MLNode only for first model of corresponding hardware node
  - Set `PRE_POC_SLOT` to `true` and `POC_SLOT` to `true` for the MLNode
- **Where**: `inference-chain/x/inference/module/module.go`
- **Dependencies**: 6.2

#### 6.2.2 Model-Based MLNode Distribution in setModelsForParticipants
- **Task**: [~] POC 50% of nodes allocation
- **What**: Modify `setModelsForParticipants` to:
  - After allocating nodes per participant, for each participant, iterate through models, for each model calculate total PoC weight of the MLNodes with `POC_SLOT` set to `true` (PoC weight of that participant for that model), and mark nodes `POC_SLOT` to `false` until we reach <50% PoC weight of that participant for that model. Before switching to the next model, take the "remainder" (what we marked above 50%), and subtract the remainder from total PoC weight for the next model, so that we need to switch fewer MLNodes to `false` potentially. When iterating through the models, iterate in random but deterministic order with seed of epoch ID and participant address.
- **Where**: `inference-chain/x/inference/module/module.go`
- **Dependencies**: 6.2.1

#### 6.2.3 API Node POC_SLOT Enforcement
- **Task**: [ ] Prevent ML nodes from switching to PoC when POC_SLOT is true
- **What**: Modify API node state command logic to check MLNode's POC_SLOT allocation before changing node states:
  - In `StartPocCommand.Execute`: Before switching a node to PoC mining, query epoch group data to get the node's timeslot allocation and check if `POC_SLOT` is `true`. If true, skip the PoC mining transition and keep the node in inference service mode.
  - In `InitValidateCommand.Execute`: Ensure validation state transitions respect POC_SLOT allocations for nodes that should continue inference service.
  - In `InferenceUpAllCommand.Execute`: Verify that the existing implementation already checks if nodes are in inference mode and does nothing for those nodes (no special POC_SLOT logic needed).
  - Add helper function `shouldNodeContinueInference(nodeId string)` to query epoch MLNode info and check POC_SLOT status.
  - Log decisions for debugging and monitoring.
- **Where**: `decentralized-api/broker/state_commands.go` in the Execute methods of `StartPocCommand`, `InitValidateCommand`, and `InferenceUpAllCommand`
- **Dependencies**: 6.2.2

#### 6.3 PoC Weight Preservation System
- **Task**: [ ] Implement weight preservation for inference-serving MLNodes
- **What**: Enhance weight transition system to preserve weights from previous epoch for MLNodes that continue inference service during PoC:
  - Create `PreserveInferenceNodeWeights` function that takes old and new `ActiveParticipant` arrays
  - Iterate through old `ActiveParticipant` MLNodes to find nodes with `POC_SLOT = true`
  - For each inference-serving MLNode, copy its weight to the corresponding MLNode in new `ActiveParticipant`
  - Ensure preserved weights are properly integrated with new PoC mining weights
  - Call this function after `setModelsForParticipants` in `onSetNewValidatorsStage`
- **Where**: `inference-chain/x/inference/module/module.go` - new function called from `onSetNewValidatorsStage`
- **Dependencies**: 6.2.3

#### 6.4 Throughput Vector Fields
- **Task**: [ ] Add throughput vectors to EpochGroupData
- **What**: Add `expected_throughput_vector` and `real_throughput_vector` fields. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/epoch_group_data.pb.go`
- **Dependencies**: 6.3

#### 6.5 Throughput Measurement Functions
- **Task**: [ ] Create throughput measurement and validation functions
- **What**: Create `MeasureModelThroughputForBlocks`, `SetExpectedThroughput`, `SetRealThroughput`, `ValidateThroughputPerformance`
- **Where**: `inference-chain/x/inference/keeper/throughput_measurement.go` (new file)
- **Dependencies**: 6.4

#### 6.6 Pre-PoC MLNode Selection Algorithm
- **Task**: [ ] Implement weighted participant selection for PoC slot assignment
- **What**: Create node selection algorithm in EndBlocker before PoC Stage
- **Where**: `inference-chain/x/inference/module/module.go` (enhance EndBlocker)
- **Dependencies**: 6.5

### Section 7: Per Model Sybil Resistance Incentives

#### 7.1 Model Coverage Query Function
- **Task**: [ ] Create participant model coverage checking function
- **What**: Create `GetParticipantModelCoverage` function to check if participant supports all models
- **Where**: `inference-chain/x/inference/epochgroup/epoch_group.go`
- **Dependencies**: 4.3

#### 7.2 Account Settlement Enhancement
- **Task**: [ ] Add model coverage bonus to reward calculation
- **What**: Enhance `getSettleAmount` and `GetSettleAmounts` to apply 10% bonus for full model coverage
- **Where**: `inference-chain/x/inference/keeper/accountsettle.go`
- **Dependencies**: 7.1

### Section 8: Enhanced PoC Stages

#### 8.1 PoC Stages Extension
- **Task**: [ ] Add new PoC stages to stage definitions
- **What**: Add "Model Loading Stage" and "End of Epoch" stages to PoC cycle
- **Where**: `inference-chain/x/inference/module/poc_stages.go`
- **Dependencies**: None

#### 8.2 Next Epoch Ready Function
- **Task**: [ ] Create OnNextEpochReady stage handler
- **What**: Create `OnNextEpochReady` function for next epoch ready handling
- **Where**: `inference-chain/x/inference/module/poc_stages.go`
- **Dependencies**: 8.1

#### 8.3 Orchestrator PoC Stage Handling
- **Task**: [ ] Add new stage handling to orchestrator
- **What**: Update `ProcessNewBlockEvent` to handle OnNextEpochReady and add `LoadModelsForNextEpoch`
- **Where**: `decentralized-api/internal/poc/orchestrator.go`- **Dependencies**: 8.2

### Section 9: Testing and Integration

#### 9.1 Unit Tests for Model Enhancement
- **Task**: [ ] Create unit tests for enhanced model structure and validation
- **What**: Write comprehensive tests for new model fields and validation functions
- **Where**: Test files corresponding to modified keeper functions
- **Dependencies**: 1.*, 2.*

#### 9.2 Unit Tests for Epoch Snapshots
- **Task**: [ ] Create unit tests for epoch snapshotting functionality
- **What**: Write tests for model and MLNode snapshot creation and retrieval
- **Where**: Test files for epoch group and API handlers
- **Dependencies**: 3.*, 4.*

#### 9.3 Integration Tests for PoC Tracking
- **Task**: [ ] Create integration tests for per-MLNode PoC tracking
- **What**: Write end-to-end tests for MLNode PoC batch tracking and weight calculation
- **Where**: Integration test suite
- **Dependencies**: 5.*, 6.*

#### 9.4 Integration Tests for Uptime Management
- **Task**: [ ] Create integration tests for MLNode uptime management
- **What**: Write tests for timeslot allocation and throughput measurement
- **Where**: Integration test suite  
- **Dependencies**: 7.*, 8.*

#### 9.5 API Endpoint Testing
- **Task**: [ ] Test all new and modified API endpoints
- **What**: Verify governance models/pricing endpoints and updated models/pricing endpoints
- **Where**: API integration tests
- **Dependencies**: 3.6, 3.7, 3.4, 3.5

### Section 10: Documentation and Deployment

#### 10.1 API Documentation Update
- **Task**: [ ] Update API documentation for new endpoints
- **What**: Document new governance endpoints and changed behavior of existing endpoints
- **Where**: API documentation files
- **Dependencies**: 3.*

#### 10.2 Migration Guide Creation
- **Task**: [ ] Create migration guide for existing deployments
- **What**: Document breaking changes and required configuration updates
- **Where**: Migration documentation
- **Dependencies**: All previous sections

#### 10.3 Configuration Examples Update
- **Task**: [ ] Update configuration examples with new model parameters
- **What**: Provide examples of enhanced model configurations and MLNode setups
- **Where**: Configuration documentation
- **Dependencies**: 1.*, 4.*

### Section 11: API Optimization

#### 11.1 Broker-Level Model Caching
- **Task**: [ ] Implement broker-level cache for active models
- **What**: Enhance the Broker to cache the active epoch models. This cache will be populated by `UpdateNodeWithEpochData` and exposed via a `GetActiveEpochModels` method. The public `/v1/models` and `/v1/pricing` API endpoints will be refactored to use this cache instead of querying the chain directly, significantly reducing redundant chain queries.
- **Where**: 
  - `decentralized-api/broker/broker.go`
  - `decentralized-api/internal/server/public/get_models_handler.go`
  - `decentralized-api/internal/server/public/get_pricing_handler.go`
- **Dependencies**: 4.6

#### 11.2 MLNode Throughput Population Review
- **Task**: [ ] Review and implement throughput population in setModelsForParticipants
- **What**: Evaluate whether `setModelsForParticipants` should populate `MLNodeInfo.Throughput` using the governance model's `ThroughputPerNonce` field instead of relying solely on hardware node declarations. This would ensure consistent throughput calculations based on governance-approved model parameters during epoch group formation.
- **Where**: `inference-chain/x/inference/module/module.go` - `setModelsForParticipants` function
- **Dependencies**: 5.9, 5.10

## Critical Dependencies Summary

### Blocking Dependencies
- Section 1 must complete before Section 2 (model validation requires enhanced model structure)
- Section 3.1-3.3 must complete before Section 3.4-3.5 (API updates require epoch snapshot functionality)
- Section 4.1-4.2 must complete before Section 4.3+ (MLNode tracking requires protobuf structures)
- Section 5.1-5.2 must complete before Section 5.4+ (PoC tracking requires enhanced batch structure)

### Parallel Work Opportunities
- Sections 1-2 can be developed in parallel with Section 8 (PoC stages)
- Section 6 can be developed independently once Section 4.3 is complete
- Section 9 (testing) can begin incrementally as each section completes
- Section 10 (documentation) can be prepared in parallel with development

## Estimated Completion Timeline
- **Sections 1-2**: 5-7 days (foundational model enhancements)
- **Sections 3-4**: 7-10 days (epoch snapshotting system) 
- **Sections 5-6**: 5-7 days (PoC tracking and incentives)
- **Sections 7-8**: 8-12 days (uptime management system)
- **Sections 9-10**: 5-7 days (testing and documentation)

**Total Estimated Timeline: 30-43 days** 
