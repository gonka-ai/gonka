# PoC v2 Messages Design (Base Layer Only)

> **Scope**: This document covers only the **proto messages and keeper stubs** for PoC v2.
> It does NOT cover the full PoC v2 switch, end-to-end integration, or migration from v1.

This is the foundational layer for artifact-based proof of compute. The full integration
(triggering v2 flows, epoch transitions, consensus logic) will be designed separately.

## What This Covers

- Proto message definitions (`PoCArtifactV2`, `PoCArtifactBatchV2`, `PoCValidationV2`)
- Chain RPCs for submission and querying
- Keeper storage and retrieval functions
- decentralized-api callback endpoints and DTOs
- testermint mock mappings

## What This Does NOT Cover

- When/how v2 is triggered vs v1
- Epoch-level PoC phase transitions
- End-to-end validation flow orchestration
- Migration strategy from v1 to v2
- Consensus/reward calculation using v2 data

## Design Goals

1. **Minimal on-chain footprint** - Validations carry only `validated_weight`, no arrays
2. **Batched submissions** - Validators can submit many participant validations in one tx
3. **Same semantics as v1** - Submission windows, access gating, and antehandler checks unchanged
4. **Additive changes only** - All existing v1 endpoints and messages remain untouched

## Proto Surface (inference-chain)

### New Types

```protobuf
// poc_v2.proto

message PoCArtifactV2 {
  int64 nonce = 1;
  bytes vector = 2;  // Raw bytes; protocol convention: k_dim=12, fp16, little-endian
}

message PoCArtifactBatchV2 {
  string participant_address = 1;
  int64 poc_stage_start_block_height = 2;
  string node_id = 3;
  repeated PoCArtifactV2 artifacts = 4;
}
// Note: No batch_id field. Keeper uses internal sequence for uniqueness.
// Note: Current iteration stores on-chain; later iteration moves fully off-chain.

message PoCValidationV2 {
  string participant_address = 1;
  string validator_participant_address = 2;
  int64 poc_stage_start_block_height = 3;
  int64 validated_weight = 4;  // -1 = reject; >=0 = accept
}
```

### New Tx Messages

```protobuf
// tx.proto additions

rpc SubmitPocArtifactBatchV2(MsgSubmitPocArtifactBatchV2) returns (MsgSubmitPocArtifactBatchV2Response);
rpc SubmitPocValidationV2(MsgSubmitPocValidationV2) returns (MsgSubmitPocValidationV2Response);
rpc SubmitPocValidationsV2(MsgSubmitPocValidationsV2) returns (MsgSubmitPocValidationsV2Response);

message MsgSubmitPocArtifactBatchV2 {
  option (cosmos.msg.v1.signer) = "creator";
  string creator = 1;
  int64 poc_stage_start_block_height = 2;
  string node_id = 3;
  repeated PoCArtifactV2 artifacts = 4;
}

message MsgSubmitPocValidationV2 {
  option (cosmos.msg.v1.signer) = "creator";
  string creator = 1;
  string participant_address = 2;
  int64 poc_stage_start_block_height = 3;
  int64 validated_weight = 4;
}

message MsgSubmitPocValidationsV2 {
  option (cosmos.msg.v1.signer) = "creator";
  string creator = 1;
  repeated PoCValidationV2 validations = 2;
}
```

### New Queries

```protobuf
// query.proto additions

rpc PocV2BatchesForStage(QueryPocV2BatchesForStageRequest) returns (QueryPocV2BatchesForStageResponse);
rpc PocV2ValidationsForStage(QueryPocV2ValidationsForStageRequest) returns (QueryPocV2ValidationsForStageResponse);
```

## Submission Window Semantics

V2 messages must enforce the **same rules** as v1:

1. **Batch submission** - Only valid during PoC exchange window (or confirmation PoC generation+exchange window)
2. **Validation submission** - Only valid during validation exchange window (or confirmation PoC validation window)
3. **Participant access gating** - Blocklist/allowlist checks apply to v2 submissions
4. **Antehandler/CheckTx** - Reuse `Keeper.CheckPoCMessageTooLate(...)` for v2 messages

## Chain-Side `validated_weight` Semantics

- `validated_weight == -1` → Reject/fraud signal
- `validated_weight >= 0` → Accept; chain may derive actual weight from committed nonce count
- Future: majority/median algorithm across validators

## decentralized-api Integration

### New Callback Endpoints

```
POST /v2/poc-artifacts/generated   # Receives artifact batches from MLNode
POST /v2/poc-artifacts/validated   # Receives validation results from MLNode
```

V1 endpoints (`/v1/poc-batches/{generated,validated}`) remain unchanged.

### Callback → Chain Mapping

| Callback | Chain Message |
|----------|---------------|
| `/v2/poc-artifacts/generated` | `MsgSubmitPocArtifactBatchV2` |
| `/v2/poc-artifacts/validated` | `MsgSubmitPocValidationsV2` (batched, default) |

Validation mapping:
- `fraud_detected == true` → `validated_weight = -1`
- `fraud_detected == false` → `validated_weight = n_total`

## testermint Integration

### WireMock Mappings (add to both `mappings/` and `alternative-mappings/`)

| MLNode Endpoint | Callback Target |
|-----------------|-----------------|
| `POST /api/v1/inference/pow/init/generate` | `{{KEY_NAME}}-api:9100/v2/poc-artifacts/generated` |
| `POST /api/v1/inference/pow/generate` | `{{KEY_NAME}}-api:9100/v2/poc-artifacts/validated` |
| `GET /api/v1/inference/pow/status` | Returns status JSON |
| `POST /api/v1/inference/pow/stop` | Returns 200 OK |

### mock_server Ktor Routes (versioned + unversioned)

Add dual routes for all v2 endpoints:
- `POST /api/v1/inference/pow/init/generate` AND `POST /{version}/api/v1/inference/pow/init/generate`
- `POST /api/v1/inference/pow/generate` AND `POST /{version}/api/v1/inference/pow/generate`
- `GET /api/v1/inference/pow/status` AND `GET /{version}/api/v1/inference/pow/status`
- `POST /api/v1/inference/pow/stop` AND `POST /{version}/api/v1/inference/pow/stop`

### Mock Payload Generation

- **Artifacts**: k_dim=12 vectors as base64-encoded predictable bytes
- **Validation**: `n_total = len(nonces)`, `fraud_detected` controllable by scenario

## Migration Notes

- All changes are additive; no breaking changes to v1
- decentralized-api should default to batched `MsgSubmitPocValidationsV2` for efficiency
- On-chain artifact batches are transitional; future iteration moves them fully off-chain
