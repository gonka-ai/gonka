# PoC Cleanup and V2 Batching Implementation Notes

This document describes all changes made during the PoC cleanup and V2 batching implementation session.

## Part 1: V1 PoC API Cleanup

### Motivation
- PoC V2 is the only supported flow going forward
- V1 submission paths should be explicitly rejected (not just silently deprecated)
- Remove dead code from the API layer to simplify maintenance
- Clients calling V1 endpoints should receive clear guidance to migrate

### Changes

#### 1. V1 Callback Handlers → 410 Gone Rejection

**File:** `decentralized-api/internal/server/mlnode/post_generated_batches_handler.go`

**Before:** Full V1 implementation with `SubmitPocBatch` and `SubmitPoCValidation` calls.

**After:** Simple rejection handlers returning HTTP 410 Gone:
```go
const deprecationMessage = "PoC v1 callbacks are deprecated; use /v2/poc-batches/generated"

func (s *Server) postGeneratedBatches(ctx echo.Context) error {
    logging.Warn("postGeneratedBatches: V1 PoC callback is deprecated", ...)
    return echo.NewHTTPError(http.StatusGone, deprecationMessage)
}

func (s *Server) postValidatedBatches(ctx echo.Context) error {
    logging.Warn("postValidatedBatches: V1 PoC callback is deprecated", ...)
    return echo.NewHTTPError(http.StatusGone, deprecationMessage)
}
```

**Motivation:** V1 routes remain registered for backward compatibility, but explicitly reject requests with migration guidance rather than silently failing.

---

#### 2. CosmosMessageClient Interface Cleanup

**File:** `decentralized-api/cosmosclient/cosmosclient.go`

**Removed from interface:**
- `SubmitPocBatch(*inference.MsgSubmitPocBatch) error`
- `SubmitPoCValidation(*inference.MsgSubmitPocValidation) error`

**Removed implementations:**
- `func (icc *InferenceCosmosClient) SubmitPocBatch(...)`
- `func (icc *InferenceCosmosClient) SubmitPoCValidation(...)`

**Motivation:** These methods are no longer callable since V1 handlers reject all requests. Removing them prevents accidental usage.

---

#### 3. Mock Client Cleanup

**File:** `decentralized-api/cosmosclient/mock_cosmos_message_client.go`

**Removed:**
- `func (m *MockCosmosMessageClient) SubmitPocBatch(...)`
- `func (m *MockCosmosMessageClient) SubmitPoCValidation(...)`

**Motivation:** Keep mock in sync with interface.

---

#### 4. TX Deadline Config Cleanup

**File:** `decentralized-api/cosmosclient/tx_manager/tx_deadline_config.go`

**Before:**
```go
var deadlineByMsgType = map[string]int64{
    "/inference.inference.MsgSubmitPocBatch":             240,
    "/inference.inference.MsgSubmitPocValidation":        240,
    "/inference.inference.MsgSubmitPocArtifactBatchesV2": 240,
    "/inference.inference.MsgSubmitPocValidationsV2":     240,
    ...
}
```

**After:**
```go
var deadlineByMsgType = map[string]int64{
    "/inference.inference.MsgSubmitPocBatchesV2":     240,
    "/inference.inference.MsgSubmitPocValidationsV2": 240,
    ...
}
```

**Motivation:** Remove V1 message types, update V2 name from `MsgSubmitPocArtifactBatchesV2` to `MsgSubmitPocBatchesV2`.

---

#### 5. Batch Consumer V1 Removal

**File:** `decentralized-api/cosmosclient/tx_manager/batch_consumer.go`

**Removed:**
- Constants: `batchPocBatchConsumer`, `batchPocValidationConsumer`
- Struct fields: `pocBatchBatch`, `pocValidationBatch`, `pocBatchMu`, `pocValidationMu`, `pocBatchCreatedAt`, `pocValidationCreatedAt`
- Methods: `handlePocBatchMsg`, `handlePocValidationMsg`, `checkAndFlushPocBatch`, `checkAndFlushPocValidation`, `flushPocBatch`, `flushPocValidation`, `PublishPocBatch`, `PublishPocValidation`
- Stream subscriptions in `Start()`
- V1 batch handling in `flushLoop()` and `extendAckDeadlines()`

**Motivation:** V1 batching infrastructure is dead code since handlers reject V1 requests.

---

#### 6. Batch Consumer Test Updates

**File:** `decentralized-api/cosmosclient/tx_manager/batch_consumer_test.go`

**Changed:** `TestBatchConsumer_SeparateQueues` - updated expected batch calls from 4 to 2 (only start/finish queues remain).

**Motivation:** Test was validating V1 queue behavior that no longer exists.

---

#### 7. NATS Server Stream Cleanup

**File:** `decentralized-api/internal/nats/server/server.go`

**Removed:**
- `TxsBatchPocBatchStream = "txs_batch_poc_batch"`
- `TxsBatchPocValidationStream = "txs_batch_poc_validation"`
- Corresponding stream creation calls

**Motivation:** V1 streams no longer used.

---

## Part 2: V2 PoC Message Batching

### Motivation
- Start/finish inference messages use NATS batching to aggregate multiple callbacks into single chain transactions
- V2 PoC messages (`MsgSubmitPocBatchesV2`, `MsgSubmitPocValidationsV2`) were submitted immediately without batching
- Inconsistent behavior - V2 should also benefit from batching for efficiency

### Changes

#### 1. NATS Server Stream Constants

**File:** `decentralized-api/internal/nats/server/server.go`

**Added:**
```go
TxsBatchPocV2Stream        = "txs_batch_poc_v2"
TxsBatchValidationV2Stream = "txs_batch_validation_v2"
```

**Added streams to `createJetStreamTopics` call.**

**Motivation:** New NATS streams for V2 PoC message aggregation.

---

#### 2. Batch Consumer V2 Support

**File:** `decentralized-api/cosmosclient/tx_manager/batch_consumer.go`

**Added constants:**
```go
batchPocV2Consumer        = "batch-poc-v2-consumer"
batchValidationV2Consumer = "batch-validation-v2-consumer"
```

**Added struct fields:**
```go
pocV2Batch        []pendingMsg
validationV2Batch []pendingMsg
pocV2Mu           sync.Mutex
validationV2Mu    sync.Mutex
pocV2CreatedAt    time.Time
validationV2CreatedAt time.Time
```

**Added methods:**
- `handlePocV2Msg(msg *nats.Msg)` - receives messages from NATS, accumulates in batch
- `handleValidationV2Msg(msg *nats.Msg)` - same for validation messages
- `checkAndFlushPocV2()` - timeout-based flush check
- `checkAndFlushValidationV2()` - timeout-based flush check
- `flushPocV2()` - sends accumulated batch to chain
- `flushValidationV2()` - sends accumulated batch to chain
- `PublishPocBatchV2(msg sdk.Msg) error` - publishes to NATS stream
- `PublishPocValidationV2(msg sdk.Msg) error` - publishes to NATS stream

**Updated:**
- `NewBatchConsumer()` - initializes V2 batch slices
- `Start()` - subscribes to V2 streams
- `flushLoop()` - calls V2 check methods
- `extendAckDeadlines()` - extends V2 batch deadlines

**Motivation:** Mirror existing start/finish batching pattern for V2 PoC messages.

---

#### 3. CosmosClient Batching Integration

**File:** `decentralized-api/cosmosclient/cosmosclient.go`

**Before:**
```go
func (icc *InferenceCosmosClient) SubmitPocBatchesV2(transaction *inference.MsgSubmitPocBatchesV2) error {
    transaction.Creator = icc.Address
    _, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
    return err
}
```

**After:**
```go
func (icc *InferenceCosmosClient) SubmitPocBatchesV2(transaction *inference.MsgSubmitPocBatchesV2) error {
    transaction.Creator = icc.Address
    if icc.batchingEnabled {
        return icc.batchConsumer.PublishPocBatchV2(transaction)
    }
    _, err := icc.manager.SendTransactionAsyncWithRetry(transaction)
    return err
}
```

**Same pattern applied to `SubmitPocValidationsV2`.**

**Motivation:** When batching is enabled, V2 PoC messages go through NATS for aggregation instead of direct chain submission.

---

#### 4. Test Stream Updates and New Tests

**File:** `decentralized-api/cosmosclient/tx_manager/batch_consumer_test.go`

**Changed stream names in `startTestNatsServer`:**
- `txs_batch_poc_batch` → `txs_batch_poc_v2`
- `txs_batch_poc_validation` → `txs_batch_validation_v2`

**Added tests:**
- `TestBatchConsumer_PocV2Batching` - verifies PoC V2 batch messages are aggregated
- `TestBatchConsumer_ValidationV2Batching` - verifies validation V2 messages are aggregated
- `TestBatchConsumer_AllQueuesIndependent` - verifies all 4 queue types (start, finish, poc_v2, validation_v2) flush independently

**Motivation:** Validate new V2 batching functionality.

---

## Part 3: V2 Test Migration and Allowlist Fix

### Motivation
- After consolidating `ComputeNewWeights` to use V2 logic, existing tests using V1 data structures failed
- The V2 weight calculation was missing allowlist filtering that existed in V1

### Changes

#### 1. Added Allowlist Filtering to V2 ComputeNewWeights

**File:** `inference-chain/x/inference/module/pocv2_chainvalidation.go`

Added allowlist check before processing participant batches:
```go
for _, participantAddress := range sortedBatchKeys {
    // Check participant allowlist
    if !am.keeper.IsParticipantAllowed(ctx, epochStartBlockHeight, participantAddress) {
        am.LogInfo("ComputeNewWeights: Participant not in allowlist, skipping", ...)
        continue
    }
    // ... rest of participant processing
}
```

**Motivation:** V2 logic was missing allowlist filtering that V1 had. Without this, participants not on the allowlist would still be accepted.

---

#### 2. Migrated Tests to V2 Data Structures

**File:** `inference-chain/x/inference/module/chainvalidation_test.go`

Replaced all V1 data structure usage with V2 equivalents:

**Before:**
```go
k.SetPocBatch(ctx, types.PoCBatch{
    ParticipantAddress:       participantA,
    PocStageStartBlockHeight: 100,
    Nonces:                   []int64{1, 2, 3},
})
k.SetPoCValidation(ctx, types.PoCValidation{
    ParticipantAddress:          participantA,
    ValidatorParticipantAddress: validatorAddr,
    PocStageStartBlockHeight:    100,
    FraudDetected:               false,
})
```

**After:**
```go
k.SetPocBatchV2(ctx, types.PoCBatchV2{
    ParticipantAddress:       participantA,
    PocStageStartBlockHeight: 100,
    NodeId:                   "node-a",
    Artifacts:                []*types.PoCArtifactV2{{Nonce: 1}},
})
k.SetPocValidationV2(ctx, types.PoCValidationV2{
    ParticipantAddress:          participantA,
    ValidatorParticipantAddress: validatorAddr,
    PocStageStartBlockHeight:    100,
    ValidatedWeight:             100,  // In V2, 0 = fraud detected
})
```

**Tests Updated:**
- `TestComputeNewWeightsWithStakingValidators`
- `TestComputeNewWeights` (multiple test cases)
- `TestComputeNewWeights_AllowlistExcludesParticipant`

**Motivation:** Tests were using V1 `SetPocBatch`/`SetPoCValidation` but `ComputeNewWeights` now reads V2 data from `GetPoCBatchesV2ByStage`/`GetPoCValidationsV2ByStage`.

---

## Summary of All Modified Files

### decentralized-api/
1. `internal/server/mlnode/post_generated_batches_handler.go` - V1 handlers → 410 Gone
2. `cosmosclient/cosmosclient.go` - Removed V1 interface methods, added V2 batching
3. `cosmosclient/mock_cosmos_message_client.go` - Removed V1 mock methods
4. `cosmosclient/tx_manager/tx_deadline_config.go` - Removed V1 message types
5. `cosmosclient/tx_manager/batch_consumer.go` - Removed V1 batching, added V2 batching
6. `cosmosclient/tx_manager/batch_consumer_test.go` - Updated streams, added V2 tests
7. `internal/nats/server/server.go` - Removed V1 streams, added V2 streams

### inference-chain/
8. `x/inference/module/chainvalidation.go` - Consolidated from two files, removed V1 dead code
9. `x/inference/module/pocv2_chainvalidation.go` - **DELETED** (merged into chainvalidation.go)
10. `x/inference/module/chainvalidation_test.go` - Migrated tests from V1 to V2 data structures
11. `x/inference/module/confirmation_poc.go` - Updated `NewWeightCalculatorV2` → `NewWeightCalculator`

---

## Part 4: File Consolidation

### Motivation
- Had two files: `chainvalidation.go` (V1 + shared utilities) and `pocv2_chainvalidation.go` (V2)
- V1 code in `chainvalidation.go` was dead after V2 became the only supported flow
- Messy to maintain two files with duplicate patterns

### Changes

**Merged into `chainvalidation.go`:**
- `WeightCalculator` struct (was `WeightCalculatorV2`)
- `NewWeightCalculator` (was `NewWeightCalculatorV2`)
- `ComputeNewWeights` function
- `filterPoCBatchesFromInferenceNodes` (was `filterPoCBatchesV2FromInferenceNodes`)
- All calculator methods with "V2" suffix removed

**Removed from `chainvalidation.go`:**
- V1 `WeightCalculator` struct and all its methods
- V1 `calculateValidationOutcome` (V1 used `FraudDetected` bool, V2 uses `ValidatedWeight`)
- V1 `filterPoCBatchesFromInferenceNodes` (V1 used `PoCBatch`, V2 uses `PoCBatchV2`)

**Deleted:**
- `pocv2_chainvalidation.go` - entire file

**Updated references:**
- `confirmation_poc.go` - Changed `NewWeightCalculatorV2` to `NewWeightCalculator`

---

## Data Flow After Changes

```
HTTP Callback (/v2/poc-batches/generated)
    ↓
postGeneratedArtifactsV2 handler
    ↓
s.recorder.SubmitPocBatchesV2(msg)
    ↓
if batchingEnabled:
    batchConsumer.PublishPocBatchV2(msg)
        ↓
    NATS JetStream (txs_batch_poc_v2)
        ↓
    handlePocV2Msg (accumulates in pocV2Batch)
        ↓
    flushPocV2 (on size threshold or timeout)
        ↓
    txManager.SendBatchAsyncWithRetry(msgs)
else:
    manager.SendTransactionAsyncWithRetry(msg)
```

---

## Part 5: Final Cleanup

### Motivation
- Review all changes against baseline commit `a0cdbf64f6ac05f86f9edede1770c614a4cfc228`
- Remove code duplication, unused helpers, and inconsistent patterns
- Ensure code follows `.cursorrules/rules.md` (minimal, simple, clean)

### Changes

#### 1. Deduplicate PoC Params Fetching

**File:** `decentralized-api/broker/broker.go`

`queryCurrentPoCParams` had duplicated logic for fetching chain params. Now calls `enrichWithPocParams(params)` instead.

---

#### 2. Simplify Proto Comments

**File:** `inference-chain/proto/inference/inference/poc_v2.proto`

- Removed speculative "future iteration moves fully off-chain" notes
- Changed `vector` comment from hardcoded encoding details to "opaque bytes"
- Simplified `validated_weight` semantics to one-line comment

---

#### 3. Remove Unused Helper

**File:** `inference-chain/x/inference/keeper/msg_server_submit_poc_validation.go`

Removed unused `toPoCValidation` helper function. Kept deprecated `SubmitPocValidation` stub that returns `ErrDeprecated`.

---

#### 4. Add Empty Vector Validation

**File:** `decentralized-api/internal/server/mlnode/post_generated_artifacts_v2_handler.go`

Added validation after base64 decoding:
```go
if len(vectorBytes) == 0 {
    return echo.NewHTTPError(http.StatusBadRequest, "empty artifact vector")
}
```

**File:** `inference-chain/x/inference/keeper/msg_server_submit_poc_v2.go`

Added validation for each artifact:
```go
if len(artifact.Vector) == 0 {
    return nil, sdkerrors.Wrap(types.ErrPocArtifactVectorEmpty, "artifact vector is empty")
}
```

**File:** `inference-chain/x/inference/types/errors.go`

Added new error: `ErrPocArtifactVectorEmpty = sdkerrors.Register(ModuleName, 1158, "artifact vector is empty")`

---

#### 5. Simplify Variable Assignment

**File:** `inference-chain/x/inference/module/module.go`

**Before:**
```go
var activeParticipants []*types.ActiveParticipant
activeParticipants = am.ComputeNewWeights(ctx, *upcomingEpoch)
```

**After:**
```go
activeParticipants := am.ComputeNewWeights(ctx, *upcomingEpoch)
```

---

#### 6. Align Keys Prefix Definitions

**File:** `inference-chain/x/inference/types/keys.go`

Aligned PoC v2 prefix definitions with existing style:
```go
PoCBatchV2Prefix                  = collections.NewPrefix(37)
PoCValidationV2Prefix             = collections.NewPrefix(38)
ParamsKey                         = []byte("p_inference")
```

---

#### 7. Remove Redundant File Versioning

**File:** `decentralized-api/internal/pocv2/node_orchestrator_v2.go`

Renamed to `node_orchestrator.go` - the `_v2` suffix was redundant since the file is already in the `pocv2` package.

---

## Verification

- `go build ./...` passes in both `inference-chain/` and `decentralized-api/`
- `go test ./...` passes in both directories
- V1 callbacks return HTTP 410 Gone with migration message
- V2 callbacks use batching when enabled
- Empty artifact vectors are rejected at both API and chain level
