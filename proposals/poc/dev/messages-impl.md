# PoC v2 Messages Implementation (Base Layer Only)

> **Scope**: This document covers the **implementation of proto messages and keeper stubs** only.
> Full PoC v2 integration (flow orchestration, epoch transitions, v1→v2 switch) is NOT included.

This is the foundational layer. The messages and storage are in place, but:
- No code triggers v2 flows instead of v1
- No epoch-level logic uses v2 data for consensus/rewards
- No migration path from v1 to v2 is implemented

## Files Created/Modified

### inference-chain (Proto + Types)

#### New File: `proto/inference/inference/poc_v2.proto`

Contains the core v2 message types:

- `PoCArtifactV2`: Single artifact with `nonce` (int64) and `vector` (bytes)
- `PoCArtifactBatchV2`: Batch of artifacts with `participant_address`, `poc_stage_start_block_height`, `node_id`, and `artifacts`
- `PoCValidationV2`: Validation attestation with `participant_address`, `validator_participant_address`, `poc_stage_start_block_height`, and `validated_weight`

**validated_weight semantics:**
- `-1` = reject/fraud
- `>=0` = accept (chain derives actual weight from committed nonce count)

#### Modified: `proto/inference/inference/tx.proto`

Added to Msg service:
```protobuf
rpc SubmitPocArtifactBatchesV2       (MsgSubmitPocArtifactBatchesV2) returns (MsgSubmitPocArtifactBatchesV2Response);
rpc SubmitPocValidationsV2           (MsgSubmitPocValidationsV2) returns (MsgSubmitPocValidationsV2Response);
```

New message definitions:
- `MsgSubmitPocArtifactBatchesV2`: Submit multiple artifact batches from multiple nodes (creator, repeated PoCArtifactBatchV2)
- `MsgSubmitPocValidationsV2`: Submit batch of validations (creator, repeated PoCValidationV2)

Note: Both RPCs use batch submission for efficiency - a participant can submit batches from all their nodes in a single transaction.

#### Modified: `proto/inference/inference/query.proto`

Added to Query service:
```protobuf
rpc PocV2BatchesForStage (QueryPocV2BatchesForStageRequest) returns (QueryPocV2BatchesForStageResponse);
rpc PocV2ValidationsForStage (QueryPocV2ValidationsForStageRequest) returns (QueryPocV2ValidationsForStageResponse);
```

New message definitions:
- `QueryPocV2BatchesForStageRequest/Response`: Query artifact batches by block_height
- `QueryPocV2ValidationsForStageRequest/Response`: Query validations by block_height
- `PoCBatchesWithParticipantsV2`: Wrapper with participant info + batches
- `PoCValidationsWithParticipantsV2`: Wrapper with participant info + validations

#### New File: `x/inference/keeper/msg_server_submit_poc_v2.go`

Message server handlers for v2:
- `SubmitPocArtifactBatchesV2`: Validates PoC window for each batch, stores artifact batches from multiple nodes
- `SubmitPocValidationsV2`: Validates each validation, stores with validator address

#### New File: `x/inference/keeper/poc_v2.go`

Storage functions for v2:
- `SetPocArtifactBatchV2`: Stores artifact batch keyed by (height, participant, node_id)
- `SetPocValidationV2`: Stores validation keyed by (height, participant, validator)
- `GetPoCArtifactBatchesV2ByStage`: Retrieves batches by stage height
- `GetPoCValidationsV2ByStage`: Retrieves validations by stage height

#### New File: `x/inference/keeper/query_poc_v2.go`

Query handlers for v2:
- `PocV2BatchesForStage`: Returns artifact batches with participant info
- `PocV2ValidationsForStage`: Returns validations with participant info

#### Modified: `x/inference/keeper/keeper.go`

Added v2 collections:
```go
PoCArtifactBatchesV2 collections.Map[collections.Triple[int64, sdk.AccAddress, string], types.PoCArtifactBatchV2]
PoCValidationsV2     collections.Map[collections.Triple[int64, sdk.AccAddress, sdk.AccAddress], types.PoCValidationV2]
```

#### Modified: `x/inference/types/keys.go`

Added v2 prefixes:
```go
PoCArtifactBatchV2Prefix  = collections.NewPrefix(37)
PoCValidationV2Prefix     = collections.NewPrefix(38)
```

---

### decentralized-api (Callbacks + DTOs)

#### New File: `mlnodeclient/poc_v2.go`

v2 DTOs matching MLNode API:

```go
type ArtifactV2 struct {
    Nonce     int64  `json:"nonce"`
    VectorB64 string `json:"vector_b64"` // base64-encoded fp16 little-endian
}

type GeneratedArtifactBatchV2 struct {
    BlockHash   string       `json:"block_hash"`
    BlockHeight int64        `json:"block_height"`
    PublicKey   string       `json:"public_key"`
    NodeId      int          `json:"node_id"`
    Artifacts   []ArtifactV2 `json:"artifacts"`
    Encoding    *EncodingV2  `json:"encoding,omitempty"`
    RequestId   string       `json:"request_id,omitempty"`
}

type ValidatedResultV2 struct {
    PublicKey      string  `json:"public_key,omitempty"`
    BlockHeight    int64   `json:"block_height,omitempty"`
    NTotal         int64   `json:"n_total"`
    NMismatch      int64   `json:"n_mismatch"`
    MismatchNonces []int64 `json:"mismatch_nonces"`
    PValue         float64 `json:"p_value"`
    FraudDetected  bool    `json:"fraud_detected"`
}

func (v *ValidatedResultV2) ToValidatedWeight() int64
```

#### New File: `internal/server/mlnode/post_generated_artifacts_v2_handler.go`

Handlers for v2 callbacks:

- `postGeneratedArtifactsV2`: Receives `GeneratedArtifactBatchV2`, decodes base64 vectors, submits `MsgSubmitPocArtifactBatchesV2` (batch with single entry per callback)
- `postValidatedArtifactsV2`: Receives `ValidatedResultV2`, converts to `validated_weight`, submits `MsgSubmitPocValidationsV2` (batch with single entry per callback)

#### Modified: `internal/server/mlnode/server.go`

Registered v2 endpoints:
```go
e.POST("/v2/poc-artifacts/generated", s.postGeneratedArtifactsV2)
e.POST("/v2/poc-artifacts/validated", s.postValidatedArtifactsV2)
```

#### Modified: `cosmosclient/cosmosclient.go`

Added interface methods:
```go
SubmitPocArtifactBatchesV2(transaction *inference.MsgSubmitPocArtifactBatchesV2) error
SubmitPocValidationsV2(transaction *inference.MsgSubmitPocValidationsV2) error
```

Added implementations using `SendTransactionAsyncWithRetry`.

#### Modified: `cosmosclient/mock_cosmos_message_client.go`

Added mock implementations for the two new interface methods.

---

### testermint (Mocks)

#### WireMock Mappings (standard)

New files in `src/main/resources/mappings/`:
- `generate_poc_v2.json`: Stub for `/api/v1/inference/pow/init/generate` → webhook to `/generated`
- `validate_poc_v2.json`: Stub for `/api/v1/inference/pow/generate` → webhook to `/validated`
- `poc_state_v2.json`: Status endpoint stubs for v2 states
- `pow_stop_v2.json`: Stop endpoint stub

#### WireMock Mappings (alternative with KEY_NAME)

New files in `src/main/resources/alternative-mappings/`:
- `generate_poc_v2.json`
- `validate_poc_batch_v2.template.json`: Uses `{{KEY_NAME}}-api:9100` for callback URL

#### Ktor mock_server

**New File: `mock_server/src/main/kotlin/com/productscience/mockserver/routes/PowV2Routes.kt`**

Routes with dual-path support (unversioned + `/{version}/...`):
- `POST /api/v1/inference/pow/init/generate` → `handleInitGenerateV2`
- `POST /api/v1/inference/pow/generate` → `handleGenerateV2` (validation flow)
- `GET /api/v1/inference/pow/status` → `handlePowStatusV2`
- `POST /api/v1/inference/pow/stop` → `handlePowStopV2`

**Modified: `mock_server/src/main/kotlin/com/productscience/mockserver/service/WebhookService.kt`**

Added v2 webhook handlers:
- `processGeneratePocV2Webhook`: Generates deterministic artifacts, sends to callback URL
- `processValidatePocV2Webhook`: Sends validation result (happy path: no fraud)

**Modified: `mock_server/src/main/kotlin/com/productscience/mockserver/Application.kt`**

Registered `powV2Routes(webhookService)` in routing configuration.

#### InferenceMock.kt

Added interface methods and implementations:
```kotlin
fun setPocV2Response(weight: Long, hostName: String? = null, scenarioName: String = "ModelState")
fun setPocV2ValidationResponse(weight: Long, scenarioName: String = "ModelState")
```

---

## Mock Payload Generation

All v2 mocks use **simple, deterministic** payload generation:

### Artifacts (generation)
```kotlin
// 24 bytes (12 fp16 values), pattern based on nonce
val vectorBytes = ByteArray(24) { i -> ((nonce * 2 + i) % 256).toByte() }
val vectorB64 = Base64.getEncoder().encodeToString(vectorBytes)
```

### Validation (happy path)
```json
{
  "n_total": <weight>,
  "n_mismatch": 0,
  "mismatch_nonces": [],
  "p_value": 1.0,
  "fraud_detected": false
}
```

---

## PoC Window Semantics

The v2 messages reuse existing PoC window logic:

1. **Batch submission window**: Same as v1, enforced by `CheckPoCMessageTooLate` in antehandler
2. **Validation submission window**: Same as v1
3. **Participant access gating**: Same as v1, checked in message server handlers

No changes to `poc_period_validation.go` or antehandler logic required; v2 messages will use the same window semantics by calling the existing validation functions.

---

## Next Steps (Not Covered Here)

The following are required for full PoC v2 integration (separate design docs):

1. **Flow orchestration**: When to trigger v2 generation/validation vs v1
2. **Epoch integration**: Use v2 validations in epoch reward calculations
3. **v1→v2 migration**: Strategy for transitioning live network
4. **Off-chain artifacts**: Move artifact storage off-chain (IPFS, S3)
5. **Consensus logic**: Majority/median algorithm for `validated_weight`

---

## Notes on Off-Chain Transition

The current implementation stores `PoCArtifactBatchV2` on-chain. In a future iteration:

1. Artifact batches will be stored off-chain
2. Only `SubmitPocValidationsV2` messages will be submitted on-chain
3. The `SubmitPocArtifactBatchesV2` RPC can be deprecated
4. Validators will fetch artifact batches from off-chain storage

The `validated_weight` field in `PoCValidationV2` is designed to support this transition.
