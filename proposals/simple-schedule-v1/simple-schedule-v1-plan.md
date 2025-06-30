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
  - Added the "Qwen/Qwen2-72B-Instruct" and "Qwen/Qwen1.5-7B-Instruct" models to all `genesis-overrides.json` files.
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
- **Task**: [ ] Add model snapshot field to EpochGroupData
- **What**: Add `model_snapshot` (Model) field to EpochGroupData protobuf. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/epoch_group_data.pb.go`
- **Dependencies**: 1.1

#### 3.2 Epoch Model Management Functions
- **Task**: [ ] Create epoch model management functions
- **What**: Create `GetEpochModel` function in new epoch_models.go file
- **Where**: `inference-chain/x/inference/keeper/epoch_models.go`
- **Dependencies**: 3.1

#### 3.3 Epoch Group Formation Enhancement
- **Task**: [ ] Update epoch group formation to store model snapshots
- **What**: Modify `createNewEpochSubGroup` and `CreateSubGroup` to store complete Model objects
- **Where**: `inference-chain/x/inference/epochgroup/epoch_group.go`
- **Dependencies**: 3.1, 3.2

#### 3.4 Current Models API Update
- **Task**: [ ] Update models API to use epoch snapshots
- **What**: Modify `getModels` to query epoch model snapshots instead of governance models
- **Where**: `decentralized-api/internal/server/public/get_models_handler.go`
- **Dependencies**: 3.1, 3.2, 3.3

#### 3.5 Current Pricing API Update
- **Task**: [ ] Update pricing API to use epoch snapshots  
- **What**: Modify `getPricing` to use epoch model snapshots for price calculations
- **Where**: `decentralized-api/internal/server/public/get_pricing_handler.go`
- **Dependencies**: 3.1, 3.2, 3.3

#### 3.6 Governance Models API Creation
- **Task**: [ ] Create new governance models API endpoint
- **What**: Create `getGovernanceModels` function and handler for latest governance models
- **Where**: `decentralized-api/internal/server/public/get_governance_models_handler.go`
- **Dependencies**: 1.2

#### 3.7 Governance Pricing API Creation
- **Task**: [ ] Create new governance pricing API endpoint
- **What**: Create `getGovernancePricing` function and handler for upcoming pricing
- **Where**: `decentralized-api/internal/server/public/get_governance_pricing_handler.go`
- **Dependencies**: 1.2

#### 3.8 API Routes Registration
- **Task**: [ ] Register new API routes for governance endpoints
- **What**: Add routes for `/v1/governance/models` and `/v1/governance/pricing`
- **Where**: API router configuration files
- **Dependencies**: 3.6, 3.7

### Section 4: MLNode Snapshots in Epoch Groups

#### 4.1 MLNode Info Protobuf Structure
- **Task**: [ ] Create MLNodeInfo protobuf structure
- **What**: Create `MLNodeInfo` message with `node_id`, `throughput`, and `poc_weight` fields. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/epoch_group_data.pb.go`
- **Dependencies**: 3.1

#### 4.2 Epoch Group MLNode Fields
- **Task**: [ ] Add MLNode fields to EpochGroupData
- **What**: Add `ml_nodes` (repeated MLNodeInfo) field organized per participant. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/epoch_group_data.pb.go`
- **Dependencies**: 4.1

#### 4.3 Epoch Group MLNode Management Functions
- **Task**: [ ] Add MLNode management to epoch group formation
- **What**: Create `StoreMLNodeInfo` function and enhance member addition to snapshot MLNode configs
- **Where**: `inference-chain/x/inference/epochgroup/epoch_group.go`
- **Dependencies**: 4.1, 4.2

#### 4.4 Module MLNode Snapshotting
- **Task**: [ ] Add MLNode snapshotting to module functions
- **What**: Enhance `setModelsForParticipants` to snapshot hardware node configurations
- **Where**: `inference-chain/x/inference/module/module.go`
- **Dependencies**: 4.2, 4.3

#### 4.5 API Node State MLNode Fields
- **Task**: [ ] Add epoch MLNode fields to NodeState
- **What**: Add `EpochModels` and `EpochMLNodes` maps to NodeState structure
- **Where**: `decentralized-api/broker/broker.go`
- **Dependencies**: 4.1

#### 4.6 Epoch Data Update Functions
- **Task**: [ ] Create broker epoch data update functions
- **What**: Create `UpdateNodeWithEpochData` and `MergeModelArgs` functions
- **Where**: `decentralized-api/broker/broker.go`
- **Dependencies**: 4.5

#### 4.7 New Block Dispatcher Enhancement
- **Task**: [ ] Add epoch data sync to block dispatcher
- **What**: Enhance `handlePhaseTransitions` to call `UpdateNodeWithEpochData`
- **Where**: `decentralized-api/internal/event_listener/new_block_dispatcher.go`
- **Dependencies**: 4.6

#### 4.8 Node Worker Commands Update
- **Task**: [ ] Update inference commands to use epoch models
- **What**: Modify `InferenceUpNodeCommand.Execute` to use `EpochModels` instead of broker models
- **Where**: `decentralized-api/broker/node_worker_commands.go`
- **Dependencies**: 4.5, 4.6

### Section 5: Per-MLNode PoC Tracking System

#### 5.1 PoCBatch Protobuf Enhancement
- **Task**: [ ] Add NodeId field to PoCBatch structure
- **What**: Add `NodeId` field to PoCBatch protobuf to track which MLNode generated the batch. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/poc_batch.pb.go`
- **Dependencies**: None

#### 5.2 PoC Batch Keeper Functions
- **Task**: [ ] Add per-MLNode PoC query functions
- **What**: Create `GetPoCBatchesForNode`, `GetPoCBatchesForModel`, `CalculateNodeWeight`, `CalculateModelPower`
- **Where**: `inference-chain/x/inference/keeper/poc_batch.go`
- **Dependencies**: 5.1

#### 5.3 Epoch Group Total Throughput Field
- **Task**: [ ] Add total throughput tracking to EpochGroupData
- **What**: Add `total_throughput` field to EpochGroupData protobuf. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/epoch_group_data.pb.go`
- **Dependencies**: 4.2

#### 5.4 Chain Validation Weight Calculation Update
- **Task**: [ ] Enhance weight calculation for per-MLNode tracking
- **What**: Modify `ComputeNewWeights` to record per-MLNode poc_weight
- **Where**: `inference-chain/x/inference/module/chainvalidation.go`
- **Dependencies**: 5.2

#### 5.5 Epoch Group Member Update Enhancement
- **Task**: [ ] Add MLNode weight and throughput calculation to epoch group updates
- **What**: Enhance `updateEpochGroupWithNewMember` to calculate poc_weight and total_throughput
- **Where**: `inference-chain/x/inference/epochgroup/epoch_group.go`
- **Dependencies**: 5.3, 5.4

### Section 6: Per Model Sybil Resistance Incentives

#### 6.1 Model Coverage Query Function
- **Task**: [ ] Create participant model coverage checking function
- **What**: Create `GetParticipantModelCoverage` function to check if participant supports all models
- **Where**: `inference-chain/x/inference/epochgroup/epoch_group.go`
- **Dependencies**: 4.3

#### 6.2 Account Settlement Enhancement
- **Task**: [ ] Add model coverage bonus to reward calculation
- **What**: Enhance `getSettleAmount` and `GetSettleAmounts` to apply 10% bonus for full model coverage
- **Where**: `inference-chain/x/inference/keeper/accountsettle.go`
- **Dependencies**: 6.1

### Section 7: MLNode Uptime Management System

#### 7.1 MLNode Allocation Protobuf Types
- **Task**: [ ] Create timeslot allocation protobuf types
- **What**: Create enum for PRE_POC_SLOT, POC_SLOT and timeslot allocation structures. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/mlnode_allocation.pb.go`
- **Dependencies**: None

#### 7.2 MLNode Timeslot Fields
- **Task**: [ ] Add timeslot allocation to MLNodeInfo
- **What**: Add `timeslot_allocation` (repeated boolean) field to MLNodeInfo. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/epoch_group_data.pb.go` (update to existing MLNodeInfo)
- **Dependencies**: 7.1, 4.1

#### 7.3 Throughput Vector Fields
- **Task**: [ ] Add throughput vectors to EpochGroupData
- **What**: Add `expected_throughput_vector` and `real_throughput_vector` fields. **Note**: Use `ignite generate proto-go` to regenerate protobuf files.
- **Where**: `inference-chain/x/inference/types/epoch_group_data.pb.go`
- **Dependencies**: 5.3

#### 7.4 Throughput Measurement Functions
- **Task**: [ ] Create throughput measurement and validation functions
- **What**: Create `MeasureModelThroughputForBlocks`, `SetExpectedThroughput`, `SetRealThroughput`, `ValidateThroughputPerformance`
- **Where**: `inference-chain/x/inference/keeper/throughput_measurement.go` (new file)
- **Dependencies**: 7.3

#### 7.5 Pre-PoC MLNode Selection Algorithm
- **Task**: [ ] Implement weighted participant selection for PoC slot assignment
- **What**: Create node selection algorithm in EndBlocker before PoC Stage
- **Where**: `inference-chain/x/inference/module/module.go` (enhance EndBlocker)
- **Dependencies**: 7.2, 7.4

#### 7.6 PoC Weight Preservation System
- **Task**: [ ] Implement weight preservation for inference-serving MLNodes
- **What**: Enhance `ComputeNewWeights` to handle mixed PoC mining and inference service weights
- **Where**: `inference-chain/x/inference/module/chainvalidation.go`
- **Dependencies**: 5.4, 7.5

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
- **Where**: `decentralized-api/internal/poc/orchestrator.go`
- **Dependencies**: 8.2

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