# Multi Model and GPU Uptime System

## Overview

This document describes the implementation of a comprehensive multi-model support system with GPU uptime optimization for the inference network. The system enhances existing model governance with per-mlnode tracking, throughput-based incentives, and intelligent GPU allocation during Proof-of-Compute (PoC) periods.

**Key Definitions:**
- **PoC Period**: From Start of PoC Stage through Set New Validators (complete epoch transition)
- **Nonce**: Individual proof-of-work units recorded and validated during PoC
- **MLNode**: Individual ML compute node managed by a participant controller

## System Requirements

### 1. Enhanced Model Parameters
Each model includes comprehensive metadata:
- Unique identifier and creator information
- Model name, version, and Hugging Face repository link and commit hash
- Technical parameters (quantization, context size, KV cache quantization)
- **Throughput per PoC nonce** - performance metric for economic calculations

### 2. Per-MLNode Model Assignment
Participants assign specific models to individual MLNodes (through model id) for upcoming epochs, enabling granular resource management and specialized node optimization.

### 3. Model Metadata Consistency During Epoch
Model changes through existing governance become effective only when epoch subgroups are formed after PoC validation.

### 4. MLNode Metadata Consistency During Epoch
MLNode changes through Api Node synchronization become effective only when epoch subgroups are formed after PoC validation.

### 5. Per-MLNode Proof-of-Compute
PoC nonces are tracked per MLNode rather than per participant, enabling precise performance measurement and network balancing between models. PoC nonce validation remain per participants.

### 6. Per Model Sybil Resistance Incentives
Participants running at least one MLNode per supported model receive 10% additional mining rewards, encouraging comprehensive model coverage.

### 7. MLNode Uptime Management
During PoC periods, the system intelligently allocates MLNodes between inference service and PoC mining to maintain service availability while maximizing computational proof generation.

## Detailed Implementation Changes

### 1. Enhanced Model Structure with Additional Parameters

#### Before: Basic Model Registry
**Current Implementation:**
- Models stored in `inference-chain/x/inference/keeper/model.go` with basic metadata
- Simple `SetModel` and `GetAllModels` functions
- Genesis initialization in `inference-chain/x/inference/module/genesis.go`
- Governance registration via `inference-chain/x/inference/keeper/msg_server_register_model.go`

The current Model structure in `inference-chain/x/inference/types/model.pb.go` contains only 3 basic fields: ProposedBy, Id, and UnitsOfComputePerToken.

#### After: Enhanced Model Registry with Comprehensive Metadata

**New Implementation:**
- Extended Model structure in `inference-chain/x/inference/types/model.pb.go` adds HFRepo, HFCommit, ModelArgs (array of strings), VRAM, and ThroughputPerNonce (performance metric for economic calculations).

**Enhanced Functions in `inference-chain/x/inference/keeper/model.go`:**
- `GetGovernanceModels` - Enhanced version of existing `GetAllModels` (or semantic naming for clarity)
- `GetGovernanceModel` - Enhanced function to get specific model from governance registry

**Enhanced Message Handler:**
- `inference-chain/x/inference/keeper/msg_server_register_model.go` updated to handle all model parameter changes including throughput updates through governance proposals

**Important:** All model parameter changes (including throughput updates) must go through Cosmos SDK governance. No direct parameter updates are allowed.

### 2. MLNode Model Assignment

#### Current Implementation: Unvalidated Model References
**Problem:**
- MLNodes (HardwareNodes) already store model identifiers as strings in the `Models` field
- However, no validation exists against the governance model registry
- Participants can declare support for arbitrary, non-existent models
- Invalid model references propagate through epoch group formation

**Current Implementation:**
- HardwareNode structure in `inference-chain/x/inference/types/hardware_node.pb.go` contains contains LocalId, Status, Models (array 
of strings with model ids), Hardware, Host, and Port
- `MsgSubmitHardwareDiff` in `inference-chain/x/inference/keeper/msg_server_submit_hardware_diff.go` accepts any model strings without validation
- Model assignment via `decentralized-api/participant_registration/participant_registration.go` and hardware node synchronization
- Hardware nodes stored per participant via `SetHardwareNodes` in `inference-chain/x/inference/keeper/hardware_node.go`

#### Enhanced Implementation: Governance-Validated Model Assignment

**New Validation Functions in `inference-chain/x/inference/keeper/model.go`:**
- `IsValidGovernanceModel` - Check if model ID exists in governance registry

**Enhanced Hardware Node Functions in `inference-chain/x/inference/keeper/hardware_node.go`:**
- `GetNodesForModel` - Find nodes supporting a specific model (enhanced from existing functionality)

**Modified Functions in `inference-chain/x/inference/keeper/msg_server_submit_hardware_diff.go`:**
- `MsgSubmitHardwareDiff` in `inference-chain/x/inference/keeper/msg_server_submit_hardware_diff.go` enhanced to validate all model IDs against governance registry before accepting hardware node updates
- Reject transactions containing invalid model IDs with clear error messages
- Query governance models using `GetGovernanceModels` (renamed 
`GetAllModels` in previous section) from `inference-chain/x/inference/keeper/model.go`
- Return clear error messages listing specific invalid model IDs

**Modified Functions in `decentralized-api/broker/node_admin_console.go`:**
- `RegisterNode Execute` - Enhanced to validate model IDs against governance registry during node registration

**Model Registration Validation Flow:**
1. During MLNode registration (startup or runtime), query governance models using `GetGovernanceModels`
2. Validate that all models declared in MLNode configuration exist in governance registry
3. Reject MLNode registration if any models are invalid

#### Optional Enhancement: Rename Hardware Nodes to MLNodes

**Cosmetic Renaming (Optional):**
- Update protobuf field names from `hardware_nodes` to `ml_nodes` in `inference-chain/x/inference/types/hardware_node.pb.go`
- Rename `HardwareNode` message to `MLNode` 
- Update function names in `inference-chain/x/inference/keeper/hardware_node.go`
- Update API variable names throughout `decentralized-api/` codebase

**Note:** This renaming is purely cosmetic and can be implemented independently or skipped entirely. The core functionality enhancement is the model validation system.

### 3. Model Parameter Snapshots in Epoch Model Subgroups

#### Before: Runtime Parameter Queries
**Current Implementation:**
- Epoch groups only store model IDs, parameters queried from governance registry during operations `inference-chain/x/inference/epochgroup/epoch_group.go`
- No parameter consistency guarantee during epochs - mid-epoch governance changes affect ongoing operations

#### After: Epoch Parameter Freezing with Snapshot System

**New Implementation:**
- Epoch model parameter freezing for consistency during epochs
- Complete model parameters preserved for validation and economic computations
- Two-tier system separating governance changes from epoch operations

**Two-Tier Model System:**
- **Governance Registry**: Contains the latest approved models and parameters
- **Epoch Model Cache**: Contains model parameters frozen at epoch group formation
- **Timing**: Governance changes are immediate in registry, but only become effective for inference when copied to epoch cache during epoch group formation

**Model Parameter Consistency:**
- Complete model parameters frozen in `model_snapshot` during epoch group formation
- Mid-epoch governance changes don't affect current epoch operations
- Economic calculations use consistent throughput_per_nonce values
- Model metadata preserved for validation and economic computations

**New Fields in `inference-chain/x/inference/types/epoch_group_data.pb.go`:**
- `model_snapshot` (Model) - Frozen model parameters from governance

**New Functions in `inference-chain/x/inference/keeper/epoch_models.go`:**
- `GetEpochModel` - Retrieve frozen model parameters for current epoch

**Modified Functions in `inference-chain/x/inference/epochgroup/epoch_group.go`:**
- `createNewEpochSubGroup` - Enhanced to store full Model object instead of just modelId
- `CreateSubGroup` - Updated to accept Model parameter and store complete model data

**Updated Decentralized API Endpoints:**

**Modified Functions in `decentralized-api/internal/server/public/get_models_handler.go`:**
- `getModels` - Updated to query epoch model snapshots from `CurrentEpochGroupData` and extract models from epoch subgroups instead of using governance models
- Returns models currently active in the epoch with frozen parameters

**Modified Functions in `decentralized-api/internal/server/public/get_pricing_handler.go`:**
- `getPricing` - Updated to use epoch model snapshots from `CurrentEpochGroupData` for price calculations instead of governance models
- Ensures pricing consistency using frozen model parameters throughout the epoch

**New Governance API Endpoints:**

**New Functions in `decentralized-api/internal/server/public/get_governance_models_handler.go`:**
- `getGovernanceModels` - New endpoint to query latest governance-approved models via existing `ModelsAll` query
- Returns models with latest governance parameters (may differ from current epoch)

**New Functions in `decentralized-api/internal/server/public/get_governance_pricing_handler.go`:**
- `getGovernancePricing` - New endpoint showing upcoming pricing based on latest governance models
- Provides preview of pricing changes that will take effect in next epoch

**New and Updated API Routes:**
- `GET /v1/models` - Current epoch active models (existing, modified behavior)
- `GET /v1/pricing` - Current epoch pricing (existing, modified behavior)  
- `GET /v1/governance/models` - Latest governance models (new)
- `GET /v1/governance/pricing` - Upcoming governance pricing (new)

**Implementation Notes:**
- Use existing `CurrentEpochGroupData` query to retrieve epoch model snapshots instead of creating new queries
- Add EpochGroup data caching in decentralized API for efficiency (cache epoch group data to avoid repeated blockchain queries)
- Cache invalidation occurs only during epoch transitions to ensure data consistency

### 4. MLNode Snapshots in Epoch Model Subgroups

#### Before: No Snapshot for MLNode during epoch:
**Chain Node:**
- Hardware nodes stored per participant via `SetHardwareNodes` in `inference-chain/x/inference/keeper/hardware_node.go`
- No MLNode-level tracking in epoch groups `inference-chain/x/inference/epochgroup/epoch_group.go`
- No snapshot mechanism for hardware node configurations during epoch formation

**API Node:**
- Models stored in broker nodes via `Models` field in `decentralized-api/broker/broker.go`
- `InferenceUpNodeCommand` in `decentralized-api/broker/node_worker_commands.go` uses `worker.node.Node.Models` directly from broker
- No integration with epoch group data for model assignment consistency

#### After: MLNode Models conrolled through Epoch Group
**Chain Node - Add Epoch Snapshots:**
- Add epoch snapshotting of hardware node configurations for consistency during epochs
- Enhanced EpochGroupData structure stores complete MLNode ecosystem data with model assignments

**Hardware Node Consistency:**
- Hardware node configurations are snapshotted when epoch groups are formed
- Mid-epoch hardware changes don't affect current epoch inference routing
- New configurations become effective only at next epoch group formation
- Ensures consistent participant-model mappings throughout an epoch

**New Fields in `inference-chain/x/inference/types/epoch_group_data.pb.go`:**
- `ml_nodes` (repeated MLNodeInfo) - Detailed MLNode information array, it should be organized per participant (`member_address`), e.g. in ValidationWeight or a new structure

**New Message Structures:**
- `MLNodeInfo` containing node_id (will add throughput, and poc_weight in next section)

**Note**:
- Check if MLNodes should be added to ActiveParticipants as well, as currently Weights are set ActiveParticipants in `ComputeNewWeights` and Models in `setModelsForParticipants`, and only then them both to Epoch Group from ActiveParticipants

**New Functions in `inference-chain/x/inference/epochgroup/epoch_group.go`:**
- `StoreMLNodeInfo` - Record MLNode details during epoch group formation
- Use `GetNodesForModel` from `inference-chain/x/inference/keeper/hardware_node.go`

**Modified Functions in `inference-chain/x/inference/module/module.go`:**
- `setModelsForParticipants` or `updateEpochGroupWithNewMember` enhanced to snapshot current hardware node configurations to epoch storage
- Epoch group formation uses snapshotted hardware configurations for consistent subgroup creation

**API Node - Use Epoch Snapshots:**
- Add command to query EpochGroups using `CurrentEpochGroupData` and extract MLNode model assignments
- Store epoch-based model assignments in NodeState instead of using broker's Models field directly
- Modify `InferenceUpNodeCommand` to use epoch-snapshotted models from NodeState

**New Fields in NodeState:**
- `EpochModels` (map[string]Model) - Models assigned to this node from epoch snapshot, keyed by ModelId
- `EpochMLNodes` (map[string]MLNodeInfo) - MLNode configurations for this node from epoch snapshot, keyed by ModelId

**EpochGroup Data Population Logic:**
1. Query `CurrentEpochGroupData` to get parent EpochGroup
2. Iterate through all `SubGroupModels` to get model-specific subgroups
3. For each subgroup:
   - Query EpochGroupData for that specific ModelId
   - Check if participant has MLNodes in that subgroup
   - For each MLNode that matches current node:
     - Add entry to `EpochModels[ModelId]` = copy of the model from subgroup
     - Add entry to `EpochMLNodes[ModelId]` = MLNode info for this node

**New Functions in `decentralized-api/broker/broker.go`:**
- `UpdateNodeWithEpochData` - Query current epoch group data and populate NodeState EpochModels/EpochMLNodes maps
- `MergeModelArgs` - Merge epoch model arguments with local model arguments according to precedence rules

**Modified Functions in `decentralized-api/internal/event_listener/new_block_dispatcher.go`:**
- `OnNewBlockDispatcher.handlePhaseTransitions` - Enhanced to call broker's `UpdateNodeWithEpochData` before executing broker commands
- Ensures all nodes have latest epoch model assignments before state transitions

**Modified Functions in `decentralized-api/broker/node_worker_commands.go`:**
- `InferenceUpNodeCommand.Execute` - Use `worker.node.State.EpochModels` instead of `worker.node.Node.Models`
- Select first available ModelId from EpochModels map and use corresponding model and args from EpochMLNodes
- Call `MergeModelArgs` to combine epoch arguments with local arguments
- Fall back to broker models if epoch models are not available (during transitions)

**Integration Flow:**
1. **Epoch Formation**: Chain snapshots all hardware node configurations and model assignments into EpochGroupData
2. **Phase Transition Detection**: OnNewBlockDispatcher.handlePhaseTransitions detects epoch phase changes
3. **Epoch Data Sync**: Before executing any broker commands, call `UpdateNodeWithEpochData` to:
   - Query `CurrentEpochGroupData` to get parent epoch group
   - Iterate through all `SubGroupModels` to get model-specific subgroups
   - For each subgroup: Query EpochGroupData for that specific ModelId
   - Check if participant has MLNodes in that subgroup
   - For each MLNode that matches broker nodes: Populate EpochModels and EpochMLNodes maps
4. **Broker Command Execution**: Execute broker commands (StartPoCEvent, InitValidateCommand, InferenceUpAllCommand) with updated epoch data
5. **Model Loading**: `InferenceUpNodeCommand` uses epoch-assigned models from NodeState maps for consistent model loading, merging ModelArgs from epoch snapshot with locally set

**ModelArgs Merging Logic:**
- **Epoch Authority**: ModelArgs from epoch snapshot take precedence as source of truth during epoch
- **Local Extensions**: Local ModelArgs can include additional arguments not specified in epoch snapshot
- **Merge Strategy**: Epoch args are applied first, then local-only args are appended
- **Conflict Resolution**: If same argument exists in both epoch and local, epoch version is used

### 5. Per-MLNode PoC Tracking System

#### Before: Participant-Level PoC Aggregation
**Current Implementation:**
- PoC batches tracked per participant in `inference-chain/x/inference/types/poc_batch.pb.go`
- Weight calculation in `inference-chain/x/inference/module/chainvalidation.go` via `ComputeNewWeights` function
- Single weight per participant in epoch groups `inference-chain/x/inference/epochgroup/epoch_group.go` in `ValidationWeight`

The current PoCBatch structure contains ParticipantAddress, BatchId, Nonces (array of nonce values), and BlockHeight.

#### After: MLNode-Specific PoC Tracking

**New Implementation:**
- Enhanced PoCBatch structure adds NodeId (specific MLNode identifier) to track which node generated the batch.
- Per-MLNode tracking with individual weights and throughput in epoch groups
- PoC nonce validation remain per participants.

**New Functions in `inference-chain/x/inference/keeper/poc_batch.go`:**
- `GetPoCBatchesForNode` - Query batches by node
- `GetPoCBatchesForModel` - Query batches by model (determined from node model assignments)
- `CalculateNodeWeight` - Compute node-specific weight from nonce count
- `CalculateModelPower` - Aggregate model power from all nodes supporting that model

**Enhanced EpochGroupData Structure for MLNode Tracking:**

**New Fields in `inference-chain/x/inference/types/epoch_group_data.pb.go`:**
- `total_throughput` (int64) - Aggregate model throughput across all supporting nodes

**New Fields in `MLNodeInfo:**
- `throughput` and `poc_weight`

**Per-MLNode Power Distribution:**
- Individual MLNode weights tracked in `poc_weight` field
- Participant total weight is the sum of their MLNode weights (keep it in `ValidationWeight`)
- Model-specific throughput aggregated from individual node capacities

**Integration with Model-Based MLNode Organization:**
The per-MLNode tracking system implement the double repeated MLNode array structure instead repated MLNode introduced in Section 4, to support per model lists:

**Weight Calculation Flow:**
1. **Initial Weight Assignment**: `ComputeNewWeights` calculates per-MLNode weights and populates all MLNodes in the first array (index 0) of the `ActiveParticipant.ml_nodes` structure
2. **Model Distribution**: `setModelsForParticipants` redistributes MLNodes from the first array into model-specific arrays based on governance model assignments
3. **Epoch Group Formation**: `updateEpochGroupWithNewMember` transfers the organized MLNode arrays to epoch group `ValidationWeight.ml_nodes`, preserving both the model-specific organization and individual MLNode weights
4. **Model-Specific Aggregation**: Each model subgroup aggregates `total_throughput` and weights from its assigned MLNodes

**Note**:
- Check if MLNodes should be added to ActiveParticipants as well, as currently Weights are set ActiveParticipants in `ComputeNewWeights`, and only then added to Epoch Group from ActiveParticipants

**Modified Functions in `inference-chain/x/inference/module/chainvalidation.go`:**
- `ComputeNewWeights` enhanced to record per-mlnode poc_weight

**Modified Funcions in `inference-chain/x/inference/epochgroup/epoch_group.go`:**
- `updateEpochGroupWithNewMember` - When Member and its MLNodes are added, add `poc_weight` and calcualate `throughput`, and `total_throughput`
- Use epoch-frozen model parameters for throughput calculations (use model's `ThroughputPerNonce`)

### 6. Per Model Sybil Resistance Incentives

#### Before: Per Model Epoch Group Total Power Tracking
**Current Implementation:**
- Epoch groups in `inference-chain/x/inference/epochgroup/epoch_group.go`
- `total_weight` is the sum of weights of all participants who support that specific model
- No incentive for participants to run all supported model

#### After: Model Total Power Tracking and Incetive

**New Implementation:**
- Participants with MLNodes for all supported models receive 10% mining bonus

**New Functions in `inference-chain/x/inference/epochgroup/epoch_group.go`:**
- `GetParticipantModelCoverage` - Determine if participant supports all models by checking presence in all model subgroups

**Modified Functions in `inference-chain/x/inference/keeper/accountsettle.go`:**
- `getSettleAmount` enhanced to query epoch group data and apply model coverage incentives during reward calculation
- `GetSettleAmounts` enhanced to include model coverage bonus in reward distribution

### 7. MLNode Uptime Management System

#### Before: All MLNodes Mine During PoC
**Current Implementation:**
- All MLNodes participate in PoC mining
- No inference service during PoC periods
- Simple on/off switching for compute nodes

#### After: Intelligent MLNode Allocation with Timeslot Management

**New Implementation:**
- Timeslot allocation vector system for granular MLNode scheduling
- PoC stage throughput-based node selection for continuous inference service

##### 1. Timeslot Allocation Vector System

**MLNode Epoch Timeslot Management:**
- Each MLNode in an epoch has a timeslot allocation vector specifying inference participation during specific time periods
- Two primary timeslots defined: `PRE_POC_SLOT` (before PoC stage) and `POC_SLOT` (PoC stage)
- By default we set `PRE_POC_SLOT` as `true` only for first model for each MLNode, and `false` for the rest of models, and `false` to `POC_SLOT` for all models
- Timeslot allocation vector stored per MLNode in epoch model group data structure
- API nodes read epoch group data when transitioning to each stage to determine slot-based participation

**Integration with Model-Based Organization:**
The timeslot allocation system works seamlessly with the double repeated MLNode array structure:

**MLNode Lifecycle:**
- When MLNode joins, it becomes active only after the next PoC period
- In Genesis, initial weights are set for initial MLNodes

**Timeslot Decision Process:**
- When PoC stage begins, API nodes query their epoch group MLNode data
- Each API node checks its assigned timeslot allocation vector
- MLNodes with `POC_SLOT` allocation equal `true` continue inference service during PoC
- Other MLNodes switch to PoC mining

**Timeslot Assignment During Model Distribution:**
1. **Initial State**: All MLNodes start in the first array (index 0) with default timeslot allocations
2. **Model Assignment**: During `setModelsForParticipants`, as MLNodes are moved to model-specific arrays:
   - `PRE_POC_SLOT` is set to `true` only for the first model assignment per MLNode
   - `PRE_POC_SLOT` is set to `false` for subsequent model assignments  
   - `POC_SLOT` is initially set to `false` for all models
3. **Array Organization**: MLNodes in each model array maintain their individual timeslot allocation vectors
4. **Pre-PoC Selection**: The pre-PoC scheduling algorithm selects nodes from model-specific arrays and updates their `POC_SLOT` allocation to `true`

**New Fields in MLNodeInfo in Epoch Group:**
- `timeslot_allocation` (repeated boolean) - Vector defining inference participation per time period

**New Types in `inference-chain/x/inference/types/mlnode_allocation.pb.go`:**
- enum PRE_POC_SLOT, POC_SLOT

##### 2. Throughput Vector Management

**Throughput Vector Management:**
- Each model in epoch storage maintains expected throughput and real throughput vectors aligned with time slots
- Throughput vectors track performance across `PRE_POC_SLOT` and `POC_SLOT` time periods
- Expected throughput populated during epoch formation or before slot starts based on historical performance
- Real throughput measured and recorded after the period

**Enhanced Scheduling Flow:**
1. **Expected Throughput Initialization**: During epoch formation, system populates expected throughput vector for all time slots based on historical data and node capacity declarations
2. **POC_SLOT Expected Throughput Calculation**: Before PoC begins, system fills `POC_SLOT` expected throughput values for each model using `MeasureModelThroughputForBlocks` from previous period equals PoC in length in blocks
3. **POC_SLOT Real Throughput Measurement**: When PoC finished, right before calculating new weights for the following epoch using, system fills  `POC_SLOT` real throughtput value during PoC stage for each model using `MeasureModelThroughputForBlocks` and record it to epoch group.

**New Fields in EpochGroupData for Throughput Tracking:**
- `expected_throughput_vector` (repeated int64) - Expected throughput values per time slot
- `real_throughput_vector` (repeated int64) - Actual measured throughput values per time slot

**Throughput Measurement Implementation:**
- `MeasureModelThroughputForBlocks` function analyzes all inferences for model from the last n blocks
- Throughput calculated by summing all input and output tokens from inference requests
- `SetExpectedThroughput` function fills expected throughput vector during epoch formation
- `SetRealThroughput` function continuously updates real throughput vector during operation
- `ValidateThroughputPerformance` function compares expected vs real throughput

##### 3. Pre-PoC MLNode Scheduling Methodology

**Weighted Participant Selection Algorithm:**
1. **Participant Selection**: Pick a random participant weighted by their total weight across all models
2. **Node Selection**: From selected participant, pick one MLNode that supports the target model in the sequence they were added
3. **Iteration**: Continue selecting participant-node pairs until either:
   - Not enough eligible nodes remain to fill the required throughput, OR
   - Selection reaches 50% of total model throughput capacity (acceptable if more than 50% nodes switch)
4. **Throughput Accumulation**: Track cumulative throughput of selected nodes against model requirements

**Timing and Communication:**
- Node selection occurs in EndBlocker right before PoC Stage
- API nodes request timeslot allocation data before starting PoC via epoch group data synchronization

**Node Assignment**: Assign `POC_SLOT=true` to selected nodes in timeslot allocation vectors

**MLNode Exclusion**: MLNodes with pending model changes excluded from active selection

##### 4. PoC Power Preservation System

**PoC Weight Calculation and Preservation:**
- PoC weights calculated in nonces for all MLNodes participating in PoC mining
- For MLNodes continuing inference service during PoC, weights are copied from the previous epoch
- No new nonce generation required for inference-serving MLNodes

**Post-PoC Weight Recording:**
- After PoC completes, final participant weights calculated by combining:
  - PoC mining weights from MLNodes that participated in PoC (calculated from nonces)
  - Preserved weights from MLNodes that continued inference service (copied from previous epoch)
  - (By default disabled) Miners can vote, that system should use real throughput vector from `POC_SLOT` to validate performance and adjust weight calculations for MLNodes that continued inference service (`ValidateThroughputPerformance`)
- Weight aggregation handled by enhanced `ComputeNewWeights` function in `inference-chain/x/inference/keeper/poc_batch.go`

**Integration with Existing Weight Systems:**
- Weights are properly recorded to active participants and the upcoming epoch group, and subgroups by `onSetNewValidatorsStage` function in `inference-chain/x/inference/module/module.go`

### 8. Enhanced PoC Stages

#### Before: Basic Six-Stage PoC Cycle
**Current Implementation:**
- Start of PoC Stage
- PoC Exchange Window  
- End of PoC Stage / Start of PoC Validation Stage
- PoC Validation Exchange Window
- End of PoC Validation Stage (This is when "Set New Validators" and switch to the next epoch happens) 

#### After: Extended Nine-Stage PoC Cycle

**New Implementation:**
- Start of PoC Stage
- PoC Exchange Window
- End of PoC Stage / Start of PoC Validation Stage  
- PoC Validation Exchange Window
- End of PoC Validation Stage (Form the next epoch group)
- Model Loading Stage
- End of Epoch - Switch to new validators and to the next epoch

**New Functions in `inference-chain/x/inference/module/poc_stages.go`:**
- `OnNextEpochReady` - Next Epoch Ready

**Modified Functions in `decentralized-api/internal/poc/orchestrator.go`:**
- `ProcessNewBlockEvent` updated to handle OnNextEpochReady, load the required model on mlnode
- `LoadModelsForNextEpoch` added for model loading phase
