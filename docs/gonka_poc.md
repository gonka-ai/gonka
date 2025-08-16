# Gonka Proof of Compute (PoC) Design

This document describes the complete Proof of Compute design and workflow in the Gonka network. The PoC system determines validator consensus power based on computational performance rather than token bonding, utilizing the Cosmos SDK modifications described in [cosmos_changes.md](cosmos_changes.md).

## System Architecture

The Gonka network consists of three main node types that work together to implement the PoC consensus mechanism:

### 1. ML Nodes
- **Purpose**: Execute machine learning computations for PoC generation and validation
- **Location**: `mlnode/` package
- **Key Components**:
  - Proof generation workers using transformer models
  - Batch validation capabilities
  - REST API for external communication

### 2. Decentralized API Nodes
- **Purpose**: Orchestrate PoC operations and manage ML node coordination
- **Location**: `decentralized-api/` package
- **Key Components**:
  - Node broker for ML node management
  - PoC orchestrator for workflow coordination
  - Chain phase tracker for epoch management

### 3. Inference Chain Nodes
- **Purpose**: Blockchain validators running the modified Cosmos SDK
- **Location**: `inference-chain/` package
- **Key Components**:
  - PoC batch and validation message handlers
  - Epoch management and validator set updates
  - Integration with SetComputeValidators function

## PoC Workflow Overview

The PoC system operates in distinct stages within epochs. Each epoch represents a period where validators perform computational work to determine their voting power for the next validation period.

### Epoch Structure

Each epoch contains several distinct stages managed by the EpochContext in `inference-chain/x/inference/types/epoch_context.go`:

1. **PoC Generation Stage**: ML nodes generate proof of compute batches
2. **PoC Validation Stage**: Validators cross-validate submitted batches  
3. **PoC Validation End Stage**: Results are computed and new weights determined
4. **Validator Set Update Stage**: New validators are activated with updated voting power

## Detailed PoC Process

### Stage 1: PoC Generation Initiation

When the chain reaches the PoC start block height (determined by `IsStartOfPocStage` in EpochContext), the following occurs:

**Chain Side (inference-chain/x/inference/module/module.go)**:
- The `onStartOfPocStage` is triggered in the EndBlock handler
- A new epoch is created via `CreateEpochGroup`
- Old inference and PoC data is pruned based on configured thresholds

**API Node Side (decentralized-api/internal/event_listener/new_block_dispatcher.go)**:
- The OnNewBlockDispatcher detects the PoC start transition
- The NodePoCOrchestrator receives a StartPoCEvent
- Random seed is generated using the block height

### Stage 2: ML Node PoC Generation

**API Node Orchestration (decentralized-api/broker/node_worker_commands.go)**:
- API node executes StartPoCNodeCommand for each managed ML node
- Commands are dispatched through the broker's node worker system
- Each ML node receives initialization parameters including block hash, public key, and callback URL

**ML Node Computation (mlnode/packages/pow/src/pow/compute/)**:
- ML nodes initialize transformer models using the distributed block hash as seed
- Workers begin generating proof batches using the Compute class
- Each batch contains nonces and distance calculations from model outputs
- Generated batches are sent back to API nodes via callback mechanism

**Batch Submission (decentralized-api/internal/server/mlnode/post_generated_batches_handler.go)**:
- API node receives generated batches from ML nodes
- Batches are converted to MsgSubmitPocBatch messages
- Messages are submitted to the blockchain via the cosmos client

### Stage 3: PoC Validation Phase

When the validation stage begins (determined by `IsStartOfPoCValidationStage`):

**Validation Initiation (decentralized-api/internal/poc/node_orchestrator.go)**:
- The ValidateReceivedBatches function is triggered
- API node queries all submitted PoC batches from the chain
- Validation sampling is applied using deterministic nonce selection
- ML nodes are switched to validation mode via InitValidateNodeCommand

**ML Node Validation (mlnode/packages/pow/src/pow/compute/compute.py)**:
- ML nodes receive batches to validate through the ValidateBatch API
- The validate method re-generates proofs for given nonces
- Validation results include fraud detection and statistical analysis
- Results are sent back as MsgSubmitPocValidation messages

### Stage 4: PoC Results Computation

At the end of the validation stage (`IsEndOfPoCValidationStage`):

**Weight Calculation (inference-chain/x/inference/module/chainvalidation.go)**:
- The `ComputeNewWeights` function processes all submitted batches and validations
- Current validator weights are retrieved from the active participants
- PoC validation decisions are made using majority-based logic
- Participants are accepted or rejected based on validation results from other validators

**Validation Decision Logic**:
- Each participant's submission is validated by other network participants
- Acceptance requires valid validations from more than half of participants by weight
- Rejection occurs if invalid validations exceed half of participants by weight
- The decision incorporates fraud detection thresholds and statistical analysis

### Stage 5: Validator Set Update

During `IsSetNewValidatorsStage`:

**Validator Power Updates (inference-chain/x/inference/module/module.go)**:
- The system calls `SetComputeValidators` with computed results
- This function is implemented in the modified Cosmos SDK staking module
- New validator set is activated with voting power based on PoC results

**Epoch Transition**:
- The effective epoch index is updated to the upcoming epoch
- Account settlements are performed for the previous epoch
- Model assignments are made for participants in the new epoch
- Active participants are registered for the next validation period

## Power Systems and Usage Context

The Gonka network operates with two distinct power systems that serve different purposes:

### Staking Module Power (Consensus Power)
This power is set via `SetComputeValidators` and is used for all Cosmos SDK native consensus and governance operations:

**Use Cases**:
- **Block Consensus**: Determines validator selection probability for block production in CometBFT
- **Governance Voting**: Voting power for on-chain governance proposals
- **Slashing**: Affects the magnitude of slashing penalties for validator misbehavior
- **Validator Set**: Controls which validators are active in the consensus set
- **Rewards Distribution**: Influences block rewards and commission distribution

**Power Source**: Derived from PoC computational results and updated at the end of each epoch validation cycle.

### EpochGroup Power (Internal Network Power)
This power is recorded in EpochGroups and their subgroups, used for internal network operations during epochs:

**Use Cases**:
- **PoC Validation Decisions**: Determines weight in majority-based validation of other participants' PoC submissions
- **Inference Work Allocation**: Controls how much inference work each participant receives
- **Model Assignments**: Influences which models participants are assigned to serve
- **Network Resource Distribution**: Affects allocation of computational resources within epochs
- **Participant Selection**: Used in determining which participants advance to the next epoch

**Power Source**: Based on historical PoC performance, preserved weights from previous epochs, and MLNode computational capacity.

### Power Flow and Synchronization

The two power systems are synchronized at specific points in the epoch lifecycle:

1. **During Epoch**: EpochGroup power governs internal operations and PoC validation
2. **At Epoch End**: PoC results are computed using EpochGroup weights for validation decisions
3. **Validator Update**: Successful participants' power is transferred to the staking module via `SetComputeValidators`
4. **New Epoch**: Updated consensus power becomes active while new EpochGroup power is established

This dual-power architecture ensures that blockchain consensus remains stable and secure while allowing flexible resource allocation and validation during computational epochs.

## Integration with Modified Cosmos SDK

### SetComputeValidators Function

The core integration point between PoC results and consensus is the `SetComputeValidators` function in the modified staking module:

**Function Location**: Referenced in `inference-chain/x/inference/types/expected_keepers.go`
**Purpose**: Updates validator voting power based on PoC computational results rather than bonded tokens

**Process**:
1. Receives ComputeResult objects containing public keys and computed power
2. Reconciles new results with existing validator set
3. Updates validator power indexing for CometBFT integration
4. Manages validator transitions without token movement

### Power Calculation Override

**Traditional Staking**: Voting power = bonded tokens ÷ PowerReduction
**PoC System**: Voting power = computed PoC score (with PowerReduction = 1)

The modified system in `cosmos_changes.md` ensures:
- No actual token bonding occurs for PoC-based validators
- `TotalBondedTokens` is calculated by summing validator power scores
- Slashing affects PoC scores rather than burning tokens
- Hook mechanisms safely trigger collateral module penalties

## Key Implementation Files

### Chain-side PoC Management
- `inference-chain/x/inference/module/module.go` - Main epoch and stage management
- `inference-chain/x/inference/module/chainvalidation.go` - PoC validation logic
- `inference-chain/x/inference/keeper/msg_server_submit_poc_batch.go` - Batch submission handler
- `inference-chain/x/inference/keeper/msg_server_submit_poc_validation.go` - Validation submission handler

### API Node Orchestration  
- `decentralized-api/internal/poc/node_orchestrator.go` - PoC workflow coordination
- `decentralized-api/broker/node_worker_commands.go` - ML node command execution
- `decentralized-api/internal/event_listener/new_block_dispatcher.go` - Chain event processing

### ML Node Computation
- `mlnode/packages/pow/src/pow/compute/compute.py` - Core PoC computation engine
- `mlnode/packages/pow/src/pow/compute/worker.py` - Worker process management
- `mlnode/packages/pow/src/pow/service/manager.py` - PoC service management

## Epoch and Participant Management

### Epoch Lifecycle
- **Creation**: New epochs are created at PoC start via `CreateEpochGroup` 
- **Tracking**: Current, upcoming, and previous epochs are maintained separately
- **Transitions**: Clean boundaries between epoch preparation and validator switching
- **Storage**: Epoch data is stored using both sequential indices and PoC start block heights

### Participant Selection
- **Preservation**: Previous epoch participants with inference allocation are preserved
- **Weight Calculation**: Total weight computed from MLNode PoC weights  
- **Model Assignment**: Participants receive model allocations for inference work
- **Registration**: Top miners are registered based on PoC performance

This design creates a robust computational consensus mechanism where validator voting power directly reflects demonstrated computational capability rather than economic stake, while maintaining the security and functionality of the underlying Cosmos SDK consensus engine.