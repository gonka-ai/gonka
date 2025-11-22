# [IMPLEMENTED]: Random Confirmation PoC

## Problem

Two constraints prevent reliable compute capacity verification:

1. **Bandwidth limits**: Chain can't achieve full throughput yet. `MsgStartInference` and `MsgFinishInference` transactions are large - we can't stress test with high inference volume to verify nodes maintain computational resources.

2. **Low user volume**: Without enough real inference requests, we lack data to verify nodes maintain the computational capacity they demonstrated during regular PoC phase.

Potential attack: miner can disable part of node during the inference phase and real inference load on-chain is not high enough (even during the stress test) for stat stest to detect it

## Proposal

Add Random Confirmation PoC mechanism to verify inference-serving nodes maintain computational capacity. It's additional to inference tracking until real usage will high and data bandwidh problem will be solved

### Mechanism

**Trigger**: Random block height(s) during inference phase with expected frequency once per epoch (Poisson distribution: 0, 1, or 2+ times possible)
=> threat of validation all the time

**Constraint**: Confirmation PoC never overlaps with regular PoC phase - only triggers during inference phase.

**Execution**: When triggered, nodes which not preserved to serve inference during POC (`POC_SLOT=false`) switch to PoC mode:
- Generate PoC nonces using standard computational parameters with block hash from (generation_start_height - 1)
- Submit `MsgSubmitPoCBatch` transactions
- Continue for confirmation window duration  
- Validators sample and verify nonces
- Record validation results via `MsgSubmitPoCValidation`
- After validation completes, return to inference mode

This proves nodes maintain the computational capacity they claimed during regular PoC phase.

**ML Nodes** with `POC_SLOT=true` continue serving inference - their compute capacity is not verified during this event.

**POC_SLOT Timing and Weight Verification**: Confirmation PoC uses the current epoch's POC_SLOT allocation to determine which nodes participate. During epoch N's inference phase:
- Verification targets: Nodes with `POC_SLOT=false` in current epoch N (scheduled to do PoC at start of epoch N+1)
- This verifies BOTH types of weights:
  - Fresh weights: Computed during epoch N's PoC phase (these nodes had `POC_SLOT=false` at start of epoch N)
  - Preserved weights: Carried forward from earlier epochs (these nodes had `POC_SLOT=true` at start of epoch N, but now have `POC_SLOT=false`)

**Question**: Using current epoch's POC_SLOT means we verify preserved weights that were NOT re-computed during this epoch's PoC phase. Alternative approach: use previous epoch's POC_SLOT to verify only weights that were just computed during epoch N. Current implementation verifies all weights contributing to rewards regardless of when computed.

### Confirmation Weight Lifecycle

The confirmation weight follows a clear lifecycle from initialization through settlement:

**1. Initialization (Epoch Formation)**
- When EpochMember created: `confirmation_weight = sum(POC_SLOT=false weights)`
- This baseline represents the weight subject to verification
- All participants start with their full non-preserved weight as confirmation_weight

**2. During Epoch (Confirmation PoC Events)**
- If confirmation PoC triggered: Calculate actual weight from batches/validations
- Update: `confirmation_weight = min(current, calculated)`
- Multiple events: Take minimum across all confirmation events
- This captures the lowest verified capacity during the epoch

**3. Slashing Check (After Each Event)**
- Compare: `final_confirmation_weight vs alpha * initial_confirmation_weight`
- If below threshold (e.g., `confirmationWeight <= 0.70 * initialWeight`): Slash and jail participant
- Alpha tolerates minor compute degradation or temporary issues

**4. Settlement (Epoch End)**
- Recompute: `effectiveWeight = preservedWeight + confirmation_weight`
- Where: `preservedWeight = sum(POC_SLOT=true weights)`
- Apply power capping to effectiveWeight
- Distribute rewards proportionally to capped weights

**Key Property**: When no confirmation PoC occurs (or exact match):
```
capped(preservedWeight + confirmation_weight) == capped(total_weight)
```

This ensures nodes cannot earn rewards based on compute capacity they no longer possess or maintain.

### Confirmation Weight and Power Capping

Power capping (30% network limit) applies at TWO stages:

1. **During PoC Phase** (`ComputeNewWeights`):
   - Calculates weights from PoC batches/validations
   - Stores per-MLNode weights **UNCAPPED** in `ValidationWeights.MlNodes[].PocWeight`
   - Applies power capping to total participant weight
   - Stores **CAPPED** total in `ValidationWeights.Weight`

2. **During Settlement** (`CalculateParticipantBitcoinRewards`):
   - Recomputes from **UNCAPPED** MLNode weights: `effectiveWeight = preservedWeight + confirmation_weight`
   - Re-applies power capping to `effectiveWeight`
   - Distributes rewards based on capped weights

**Key Property**: When no confirmation PoC occurs (or exact match):
```
capped(preservedWeight + confirmation_weight) = ValidationWeights.Weight
```

Equality holds **after capping**, not before. This works because:
- MLNode weights stored uncapped allow recomposition
- Re-applying same capping algorithm produces same result
- Confirmed capacity properly integrated into reward calculation

## Implementation

### Code Reuse from Regular PoC

Confirmation PoC achieves ~90% code reuse from existing regular PoC infrastructure:

**Core validation logic** (`inference-chain/x/inference/module/chainvalidation.go`):
- `WeightCalculator` struct and `Calculate()` method - Reuse identical logic for computing confirmation weights from batches and validations
- `calculateParticipantWeight()` - Reuse to calculate weight from confirmation PoC batches (lines 671-699)
- `pocValidated()` - Reuse to verify majority validation (lines 615-664)
- `calculateValidationOutcome()` - Reuse to aggregate valid/invalid weights (lines 724-740)
- `calculateTotalWeight()` - Reuse for validator weight calculations (lines 702-717)

**Message handlers** - Reuse existing `MsgSubmitPoCBatch` and `MsgSubmitPoCValidation` with routing logic:
```go
// In msg_server_submit_poc_batch.go
func (k msgServer) SubmitPocBatch(ctx context.Context, msg *types.MsgSubmitPocBatch) (*types.MsgSubmitPocBatchResponse, error) {
  if activeEvent := k.GetActiveConfirmationPoCEvent(ctx); activeEvent != nil {
    // Use activeEvent.trigger_height as storage key
    // Validate using activeEvent.poc_seed_block_hash
    // Filter to POC_SLOT=false nodes only
    return k.handleConfirmationPoCBatch(ctx, msg, activeEvent)
  }
  // Regular PoC handling
  return k.handleRegularPoCBatch(ctx, msg)
}
```

**Storage** - Reuse existing collections with different keys:
- `PoCBatches: Map[(height, participant, batch_id) -> PoCBatch]`
  - Regular PoC uses `poc_start_block_height` as key
  - Confirmation PoC uses `trigger_height` as key
  - Same data structure, different keys naturally partition data
- `PoCValidations: Map[(height, participant, validator) -> PoCValidation]`
  - Regular PoC uses `poc_start_block_height` as key
  - Confirmation PoC uses `trigger_height` as key
- New collections only for confirmation PoC state:
  - `ConfirmationPoCEvents: Map[(epoch_index, event_sequence) -> ConfirmationPoCEvent]`
  - `ActiveConfirmationPoCEvent: Item[ConfirmationPoCEvent]` - Singleton for current active event

**Random trigger decision** - Reuse `deterministicFloat()` pattern from `calculations/should_validate.go`:

Pattern used for inference validation sampling (lines 32-33):
```go
randFloat := deterministicFloat(seed, inferenceDetails.InferenceId)
shouldValidate := randFloat.LessThan(ourProbability)
```

Applied to confirmation PoC trigger:
```go
// Get block hash from H-1 as randomness source
prevBlockHash := ctx.HeaderInfo().Hash  // Block hash at H-1 evaluating at H
blockHashSeed := int64(binary.BigEndian.Uint64(prevBlockHash[:8]))

// Calculate trigger probability with decimal.Decimal precision
triggerWindowBlocks := calculateTriggerWindowBlocks(ctx, params)
expectedConfirmations := decimal.NewFromInt(int64(params.ExpectedConfirmationsPerEpoch))
windowBlocks := decimal.NewFromInt(triggerWindowBlocks)
triggerProbability := expectedConfirmations.Div(windowBlocks)

// Apply deterministicFloat pattern
randFloat := deterministicFloat(blockHashSeed, "confirmation_poc_trigger")
shouldTrigger := randFloat.LessThan(triggerProbability)
```

Implementation details:
- `deterministicFloat()` uses SHA256 to generate deterministic random float [0,1) (lines 43-59)
- Block hash at H-1 provides unpredictable randomness at H (same pattern used throughout chain)
- Probability calculation uses `decimal.Decimal` for precision (same as validation calculations)
- Only `deterministicFloat()` needed - weighted random selection (`epochgroup/random.go:selectRandomParticipant`) NOT used since trigger is deterministic for all nodes

**PoC seed capture**: When transitioning from GRACE_PERIOD to GENERATION phase, capture block hash from `generation_start_height - 1` as PoC seed. This prevents precomputation during grace period.

**Comparing non-preserved weight** - Reuse filtering logic:
- `getInferenceServingNodeIds()` - Identifies POC_SLOT=true nodes (lines 743-771)
- For confirmation PoC: Only include batches from POC_SLOT=false nodes
- For slashing comparison:
```go
// Sum weight only from POC_SLOT=false nodes
totalPocSlotFalseWeight := 0
for _, mlNode := range participant.ml_nodes {
  if mlNode.timeslot_allocation[POC_SLOT_INDEX] == false {
    totalPocSlotFalseWeight += mlNode.poc_weight
  }
}

// Compare confirmation_weight against non-preserved PoC weight
if confirmation_weight <= alpha * totalPocSlotFalseWeight {
  // Slash participant
}
```

This ensures comparison is between like-for-like: confirmation PoC weight (from POC_SLOT=false nodes during confirmation) vs regular PoC weight (from same POC_SLOT=false nodes during regular PoC phase).

**Summary of reuse**:
- Weight calculation: 100% reuse of `WeightCalculator` and validation functions
- Message handlers: Add routing logic, reuse validation
- Storage: Reuse collections with different keys (no schema changes)
- Random sampling: Reuse `deterministicFloat()` for trigger decision
- Node filtering: Reuse `getInferenceServingNodeIds()` for POC_SLOT logic

Minimal new code required:
- Trigger decision logic (~50 lines)
- Confirmation event state management (~100 lines)
- Routing in message handlers (~50 lines per handler)
- Slashing comparison logic (~100 lines)

### Timing

**Valid trigger window**:
```
[GetSetNewValidatorsStage(), NextPoCStart - InferenceValidationCutoff - ConfirmationWindowDuration]
```

This ensures confirmation completes before next epoch's PoC phase begins.

**Trigger probability** for expected N confirmations per epoch:
```
p = N / (trigger window length in blocks)
```
where N = 1 (governance parameter).

**Randomness source**: Block hash from height H-1 when evaluating at height H. Deterministic and unpredictable before finalization.

**Grace period**: Trigger decision at block H, Confirmation PoC starts at H + InferenceValidationCutoff. Allows nodes to complete in-flight inference requests.
During this time `api` service not schedule inferences to MLNodes with POC_SLOT=false


### API Service Integration

**Efficiency**: Confirmation PoC state is included in the existing `EpochInfo` query that runs every block, requiring zero additional queries.

The active confirmation PoC event flows through:
1. `QueryEpochInfoResponse` → includes `active_confirmation_poc_event` and `is_confirmation_poc_active`
2. `ChainPhaseTracker` → caches event in thread-safe `EpochState`
3. Broker → accesses via `phaseTracker.GetCurrentEpochState()`

See `broker-integration.md` for complete implementation details.

### Storage

**ValidationWeight extension** - Add `confirmation_weight` field to existing `ValidationWeight` in `epoch_group_data.proto`:

```protobuf
message ValidationWeight {
  string member_address = 1;
  int64 weight = 2;
  int32 reputation = 3;
  repeated MLNodeInfo ml_nodes = 4;
  int64 confirmation_weight = 5; // NEW - final confirmed weight for epoch
}
```

**ConfirmationPoCEvent** - New message type for tracking confirmation PoC events:

```protobuf
message ConfirmationPoCEvent {
  uint64 epoch_index = 1;                 // Which epoch this belongs to
  uint64 event_sequence = 2;              // 0, 1, 2... for multiple events per epoch
  
  int64 trigger_height = 3;               // Block where trigger was decided
  int64 generation_start_height = 4;      // trigger_height + grace_period
  int64 generation_end_height = 5;        // generation_start + generation_duration
  int64 validation_start_height = 6;      // generation_end + 1
  int64 validation_end_height = 7;        // validation_start + validation_duration
  
  ConfirmationPoCPhase phase = 8;
  string poc_seed_block_hash = 9;         // Hash from (generation_start_height - 1)
}

enum ConfirmationPoCPhase {
  CONFIRMATION_POC_INACTIVE = 0;
  CONFIRMATION_POC_GRACE_PERIOD = 1;      // Waiting for nodes to finish inference
  CONFIRMATION_POC_GENERATION = 2;        // Generating PoC nonces
  CONFIRMATION_POC_VALIDATION = 3;        // Validating nonces
  CONFIRMATION_POC_COMPLETED = 4;         // Event completed
}
```

**Keeper collections**:
- `ConfirmationPoCEvents: Map[(epoch_index, event_sequence) -> ConfirmationPoCEvent]` - All events
- `ActiveConfirmationPoCEvent: Item[ConfirmationPoCEvent]` - Currently active event singleton
- Reuse existing `PoCBatches` and `PoCValidations` collections - use `trigger_height` as key instead of `poc_start_block_height` to distinguish confirmation PoC from regular PoC

**Multiple triggers per epoch**: Take minimum across all confirmation events for that epoch when computing final `confirmation_weight`

**Preserved nodes (POC_SLOT=true)**: Use regular `weight` for these nodes - they continue serving inference and aren't subject to confirmation PoC testing or slashing

**Pruning**: Extend `PruningState` with `confirmation_events_pruned_epoch` field. Prune old confirmation events and their associated batches/validations by trigger_height.

### Message Handling

**Approach**: Reuse `MsgSubmitPoCBatch` and `MsgSubmitPoCValidation`. Message handlers check for active confirmation PoC event and route accordingly.

**Handler logic**:
```go
// In MsgSubmitPoCBatch handler
if activeEvent, found := k.GetActiveConfirmationPoCEvent(ctx); found {
  if activeEvent.Phase == CONFIRMATION_POC_GENERATION {
    // Validate using poc_seed_block_hash from activeEvent
    // Store batch using trigger_height as key
    // Apply to POC_SLOT=false nodes only
  }
} else {
  // Regular PoC logic using poc_start_block_height
}
```

**Security**: PoC seed block hash is captured at `generation_start_height - 1`, preventing precomputation during grace period. The hash is set when transitioning from GRACE_PERIOD to GENERATION phase.

**Important heights for confirmation PoC**:
- `trigger_height`: Block where trigger decision made → used as storage key in PoCBatches/PoCValidations
- `generation_start_height - 1`: Block hash source → used for PoC nonce generation seed
- API service must query block hash at correct height when nodes start generation

### Slashing and Rewards

**Evaluation timing**: After confirmation validation window closes and validation messages recorded.

**Slashing logic** (participant-level):
```go
// Sum weight only from POC_SLOT=false nodes
totalPocSlotFalseWeight := 0
for _, mlNode := range participant.ml_nodes {
  if mlNode.timeslot_allocation[POC_SLOT_INDEX] == false {  // POC_SLOT=false
    totalPocSlotFalseWeight += mlNode.poc_weight
  }
}

// Compare against confirmation_weight
if participant.confirmation_weight <= alpha * totalPocSlotFalseWeight {
  // Slash entire participant (all nodes, all collateral)
  // Jail for jail_duration_epochs
  // Exclude from next epoch
}
```

**Important**: Slashing is at participant level, not individual node level. If a participant's confirmed weight falls below threshold, entire participant is penalized.

**POC_SLOT=true nodes**: Not subject to confirmation PoC. Use regular `weight` for these nodes in reward calculations.

**Reward settlement**: Apply at epoch end in `onEndOfPoCValidationStage`:
```go
// If confirmation_weight was set during epoch
effectiveWeight := min(participant.Weight, participant.ConfirmationWeight)
rewards := calculateRewards(effectiveWeight, ...)
```

Caps rewards at demonstrated compute capacity rather than claimed capacity.

### Parameters

**Timing**: Reuse existing PoC timing parameters from `EpochParams`:
- `poc_generation_duration` - Duration for confirmation PoC generation phase
- `poc_validation_duration` - Duration for confirmation validation phase  
- `InferenceValidationCutoff` - Grace period before generation starts

**Feature and enforcement parameters** (new `ConfirmationPoCParams`):
```protobuf
message ConfirmationPoCParams {
  bool enabled = 1;                                       // Feature toggle
  uint64 expected_confirmations_per_epoch = 2;           // N in probability formula (e.g., 1)
  string alpha_threshold = 3 [                           // Minimum confirmed weight ratio (e.g., "0.70")
    (cosmos_proto.scalar) = "cosmos.Dec",
    (gogoproto.customtype) = "cosmossdk.io/math.LegacyDec",
    (gogoproto.nullable) = false
  ];
  string slash_fraction = 4 [                            // Collateral slashed for failure (e.g., "0.10")
    (cosmos_proto.scalar) = "cosmos.Dec",
    (gogoproto.customtype) = "cosmossdk.io/math.LegacyDec",
    (gogoproto.nullable) = false
  ];
}
```
