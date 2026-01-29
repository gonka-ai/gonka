# Off-Chain PoC Validation - Phase 4

**Status: Complete**

## Overview

Phase 4 implements the off-chain validation flow where validators fetch proofs directly from participant APIs instead of relying on on-chain batch data.

## Implementation

### New Components

#### OffChainValidator (`decentralized-api/internal/pocv2/offchain_validator.go`)

Main orchestrator for off-chain validation:
- Queries chain for participants with store commits (`AllPoCV2StoreCommitsForStage`)
- Manages worker pool for parallel validation (configurable worker count)
- Handles queue-based retry mechanism for transient failures
- Samples deterministic leaf indices for each participant
- Coordinates proof fetching, verification, and ML node submission

Key configuration:
```go
type ValidationConfig struct {
    WorkerCount    int           // default: 10
    RequestTimeout time.Duration // default: 30s
    MaxRetries     int           // default: 3
    RetryBackoff   time.Duration // default: 5s
}
```

#### ProofClient (`decentralized-api/internal/pocv2/proof_client.go`)

Handles HTTP communication with participant APIs:
- Builds signed proof requests with timestamp
- Fetches MMR proofs from participant `/v1/poc/proofs` endpoint
- Verifies MMR proofs locally using `pocartifacts.VerifyProof`
- Returns typed errors for explicit error handling:
  - `ErrProofVerificationFailed` - permanent failure, proof didn't verify
  - `ErrDuplicateNonces` - fraud detected

### Chain Changes

#### Query Updates

Added `hex_pub_key` to `PoCV2StoreCommitWithAddress` proto message and query implementation:
- `inference-chain/proto/inference/inference/query.proto`
- `inference-chain/x/inference/keeper/query_poc_v2_commit.go`

Added `AllMLNodeWeightDistributionsForStage` query for fetching all weight distributions at once:
- Request/response messages: `QueryAllMLNodeWeightDistributionsForStageRequest`, `QueryAllMLNodeWeightDistributionsForStageResponse`, `MLNodeWeightDistributionWithAddress`

#### WeightCalculator Update

Updated `WeightCalculator` in `chainvalidation.go` to use off-chain store commits instead of on-chain batches:

- Replaced `Batches map[string][]types.PoCBatchV2` with `StoreCommits map[string]types.PoCV2StoreCommit`
- Added `NodeWeightDistributions map[string]types.MLNodeWeightDistribution` for per-node weights
- `getSortedParticipantKeys()` now iterates over store commits
- `calculateParticipantWeight()` uses `commit.Count` (scaled by weightScaleFactor) and distributions
- `ComputeNewWeights()` queries store commits and weight distributions via keeper methods
- `filterStoreCommitsFromInferenceNodes()` filters inference-serving nodes from weight calculations

Same pattern applied to `updateConfirmationWeightsV2()` in `confirmation_poc.go`.

#### Keeper Methods

Added to `inference-chain/x/inference/keeper/poc_v2.go`:
- `GetAllPoCV2StoreCommitsForStage()` - returns all store commits for a PoC stage
- `GetAllMLNodeWeightDistributionsForStage()` - returns all weight distributions for a stage
- `SetPoCV2StoreCommit()` and `SetMLNodeWeightDistribution()` helpers for testing

#### On-Chain Batch Submission Removed

Hard switch: removed `MsgSubmitPocBatchesV2` submission from DAPI artifact handler.

`postGeneratedArtifactsV2` in `decentralized-api/internal/server/mlnode/post_generated_artifacts_v2_handler.go`:
- Now only stores artifacts locally for off-chain proofs
- Store commits submitted separately by `CommitWorker`
- Weight distributions submitted at end of generation phase

#### Permissions

Added warm key permissions for off-chain messages:
- `MsgPoCV2StoreCommit` - for submitting store commits
- `MsgMLNodeWeightDistribution` - for submitting weight distribution

### Validation Flow

```
1. ValidateAll() called at start of validation phase
2. Query AllPoCV2StoreCommitsForStage to get participants with commits
3. For each participant (randomized order):
   a. Sample leaf indices deterministically
   b. Fetch proofs from participant API via ProofClient
   c. Verify MMR proofs locally
   d. Check for duplicate nonces (fraud detection)
   e. Send verified artifacts to ML node for statistical validation
   f. ML node calls back to /v2/poc-batches/validated
   g. Callback handler submits MsgSubmitPocValidationsV2 to chain
4. Retry failed participants via queue mechanism
```

### Weight Calculation Flow (End of Epoch)

```
1. Query AllPoCV2StoreCommitsForStage for all store commits
2. Query AllMLNodeWeightDistributionsForStage for per-node weights
3. Filter out inference-serving nodes from weight calculations
4. For each participant with commit:
   a. Total weight = commit.Count * weightScaleFactor
   b. Per-node weights from MLNodeWeightDistribution
   c. Validate via majority vote from validations
5. Set ActiveParticipants with computed weights
```

## Testing

- Unit tests: `decentralized-api/internal/pocv2/*_test.go`
- Chain tests: `inference-chain/x/inference/module/chainvalidation_test.go`
- E2E test: `testermint/src/test/kotlin/PoCOffChainTests.kt`

## Files Changed

| Component | Files |
|-----------|-------|
| OffChainValidator | `decentralized-api/internal/pocv2/offchain_validator.go` |
| ProofClient | `decentralized-api/internal/pocv2/proof_client.go` |
| Artifact Handler | `decentralized-api/internal/server/mlnode/post_generated_artifacts_v2_handler.go` |
| Query proto | `inference-chain/proto/inference/inference/query.proto` |
| Query impl | `inference-chain/x/inference/keeper/query_poc_v2_commit.go` |
| Keeper methods | `inference-chain/x/inference/keeper/poc_v2.go` |
| WeightCalculator | `inference-chain/x/inference/module/chainvalidation.go` |
| Confirmation PoC | `inference-chain/x/inference/module/confirmation_poc.go` |
| Permissions | `inference-chain/x/inference/permissions.go` |
| Tests | `chainvalidation_test.go`, `*_test.go`, `PoCOffChainTests.kt` |
