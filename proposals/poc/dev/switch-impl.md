# PoC v2 Implementation

> **Scope**: PoC v2 implementation details.
> See [switch-plan.md](./switch-plan.md) for design.
> **Note**: v1 code has been removed. v2 is the only implementation.

## Files Created/Modified

### inference-chain (Parameters + Weight Calculation)

#### Modified: `proto/inference/inference/params.proto`

Added `PoCv2Params` message:
```protobuf
message PoCv2Params {
  option (gogoproto.equal) = true;
  bool enabled = 1;
  string model_id = 2;
  int64 seq_len = 3;
}
```

Added to `Params`:
```protobuf
PoCv2Params poc_v2_params = 13;
```

#### Modified: `x/inference/types/params.go`

Added defaults:
```go
func DefaultPocV2Params() *PoCv2Params {
    return &PoCv2Params{
        Enabled: false,
        ModelId: "",
        SeqLen:  256,
    }
}
```

Added validation:
```go
func (p *PoCv2Params) Validate() error {
    if p.Enabled {
        if p.ModelId == "" {
            return fmt.Errorf("poc_v2_params.model_id must be set when enabled")
        }
        if p.SeqLen <= 0 {
            return fmt.Errorf("poc_v2_params.seq_len must be positive when enabled")
        }
    }
    return nil
}
```

#### Regenerated: `x/inference/types/params.pb.go`

Via `ignite generate proto-go`. Contains generated `PoCv2Params` struct.

Note: Proto generates field name `PocV2Params` (lowercase 'o') but type name `PoCv2Params`.

#### New File: `x/inference/module/pocv2_chainvalidation.go`

V2 weight calculation separated from v1:

```go
func (am AppModule) ComputeNewWeightsV2(ctx context.Context, upcomingEpoch types.Epoch) []*types.ActiveParticipant

type OrchestratorChainBridgeV2 interface {
    PoCv2ArtifactBatchesForStage(height int64) ([]*types.PoCArtifactBatchV2, error)
    PoCv2ValidationsForStage(height int64) ([]*types.PoCValidationV2, error)
}

func calculateParticipantWeightV2(validations []*types.PoCValidationV2) int64
func pocValidatedV2(validation *types.PoCValidationV2) bool // validated_weight > 0
```

Key semantics:
- `validated_weight > 0` → valid vote
- TODO comment for explicit weight derivation once artifacts are off-chain

#### Modified: `x/inference/module/module.go`

Routes to v2:
```go
func (am AppModule) ComputeNewWeights(...) []*types.ActiveParticipant {
    return am.ComputeNewWeightsV2(ctx, upcomingEpoch)
}
```

#### Modified: `x/inference/module/confirmation_poc.go`

Uses v2 weight calculation:
```go
confirmationParticipants = am.calculateConfirmationWeightsV2(ctx, event, currentValidatorWeights)
```

Helper functions:
- `calculateConfirmationWeightsV2`
- `getPoCArtifactBatchesV2ForConfirmation`
- `getPoCValidationsV2ForConfirmation`

---

### decentralized-api

#### Modified: `internal/event_listener/new_block_dispatcher.go`

Uses v2 orchestrator:
```go
type OnNewBlockDispatcher struct {
    // ... existing fields ...
    nodePocOrchestratorV2  pocv2.NodePoCOrchestratorV2
}

// In handlePhaseTransitions:
if epochContext.IsStartOfPoCValidationStage(blockHeight) {
    d.nodePocOrchestratorV2.ValidateReceivedArtifacts(epochContext.PocStartBlockHeight)
}
```

#### Modified: `broker/broker.go`

PoC params:
```go
type pocParams struct {
    startPoCBlockHeight int64
    startPoCBlockHash   string
    modelParams         *types.PoCModelParams
    v2ModelId string
    v2SeqLen  int64
}
```

V2 callback URL helper:
```go
const PoCv2ArtifactsBasePath = "/v2/poc-batches"

func GetPocArtifactsV2GeneratedCallbackUrl(callbackUrl string) string
```

`getCommandForState` returns v2 commands:
```go
case PocStatusGenerating:
    return StartPoCNodeCommandV2{...}
```

Validation is handled by the v2 orchestrator, not the broker.

#### Modified: `broker/node_worker_commands.go`

V2 command struct (no Stop() calls during generation):
```go
type StartPoCNodeCommandV2 struct {
    BlockHeight int64
    BlockHash   string
    PubKey      string
    CallbackUrl string
    TotalNodes  int
    Model       string // model_id from chain params
    SeqLen      int64  // seq_len from chain params
}
```

Validation is handled by the v2 orchestrator via `StopPowV2` + `GenerateV2`.

#### File: `broker/node_worker_commands_v2_test.go`

Unit tests for v2 commands:
- `TestStartPoCNodeCommandV2_Success` - Verifies v2 generation works without Stop()
- `TestStartPoCNodeCommandV2_AlreadyGenerating` - Idempotency check

#### Modified: `internal/pocv2/node_orchestrator_v2.go`

Implemented real chain query for v2 artifact batches:
```go
func (b *OrchestratorChainBridgeV2Impl) PoCv2ArtifactBatchesForStage(startPoCBlockHeight int64) (*PoCArtifactBatchesV2Response, error) {
    queryClient := b.cosmosClient.NewInferenceQueryClient()
    resp, err := queryClient.PocV2BatchesForStage(ctx, &types.QueryPocV2BatchesForStageRequest{
        BlockHeight: startPoCBlockHeight,
    })
    // Transform chain response to orchestrator format
    // ...
}
```

Relaxed node selection for v2 validation:
```go
func filterNodesForV2Validation(nodes []broker.NodeResponse) []broker.NodeResponse {
    // Accept nodes in POC status (any sub-status) or INFERENCE status
    // Excludes only FAILED, UNKNOWN, and administratively disabled nodes
}
```

Added `StopPowV2` call at validation stage transition:
```go
func (o *NodePoCOrchestratorV2Impl) ValidateReceivedArtifacts(pocStageStartBlockHeight int64) {
    // ...
    nodes = filterNodesForV2Validation(nodes)
    
    // Stop PoC v2 generation on all nodes before starting validation.
    // This is called once per validation stage transition (not per batch).
    o.stopGenerationOnAllNodes(nodes)
    
    // Then proceed with validation requests...
}

func (o *NodePoCOrchestratorV2Impl) stopGenerationOnAllNodes(nodes []broker.NodeResponse) {
    // Calls StopPowV2 on each node (best-effort, logs errors but continues)
}
```

#### Modified: `main.go`

V2 orchestrator initialization:
```go
nodePocOrchestratorV2 := pocv2.NewNodePoCOrchestratorV2ForCosmosChain(
    participantInfo.GetPubKey(),
    nodeBroker,
    config.GetApiConfig().PoCCallbackUrl,
    config.GetChainNodeConfig().Url,
    recorder,
    chainPhaseTracker,
)
listener := event_listener.NewEventListener(config, nodePocOrchestratorV2, ...)
```

#### New File: `mlnodeclient/poc_v2_requests.go`

Request/response DTOs:
```go
type PoCInitGenerateRequestV2 struct {
    BlockHash   string      `json:"block_hash"`
    BlockHeight int64       `json:"block_height"`
    PublicKey   string      `json:"public_key"`
    NodeId      int         `json:"node_id"`
    NodeCount   int         `json:"node_count"`
    Params      PoCParamsV2 `json:"params"`
    URL         string      `json:"url"`
}

type PoCGenerateRequestV2 struct {
    BlockHash   string         `json:"block_hash"`
    BlockHeight int64          `json:"block_height"`
    PublicKey   string         `json:"public_key"`
    NodeId      int            `json:"node_id"`
    NodeCount   int            `json:"node_count"`
    Nonces      []int64        `json:"nonces"`
    Params      PoCParamsV2    `json:"params"`
    URL         string         `json:"url"`
    Validation  *ValidationV2  `json:"validation,omitempty"`
}

type PoCParamsV2 struct {
    Model  string `json:"model"`
    SeqLen int64  `json:"seq_len"`
}
```

Client methods:
- `InitGenerateV2(req PoCInitGenerateRequestV2) (*PoCInitGenerateResponseV2, error)`
- `GenerateV2(req PoCGenerateRequestV2) (*PoCGenerateResponseV2, error)`
- `GetPowStatusV2() (*PoCStatusResponseV2, error)`
- `StopPowV2() (*PoCStopResponseV2, error)` - stops generation on all backends

Note: No `k_dim` parameter (removed for v2)

---

### testermint (v2 Route Compatibility)

#### Modified: `mock_server/src/main/kotlin/.../routes/PowV2Routes.kt`

Relaxed state preconditions to support "no Stop()" flow:

`handleInitGenerateV2`:
- Was: Required `ModelState.STOPPED`
- Now: Allowed if not `ModelState.GENERATING`

`handleGenerateV2`:
- Was: Required specific states
- Now: Allowed in any state, transitions to `POW_VALIDATING`

#### Modified: `src/main/kotlin/MockServerInferenceMock.kt`

Implemented v2 mock methods:
```kotlin
override fun setPocV2Response(weight: Long, hostName: String?, scenarioName: String) {
    // Log for v2 PoC generation mock setup
}

override fun setPocV2ValidationResponse(weight: Long, scenarioName: String) {
    // Log for v2 PoC validation mock setup
}
```

#### Modified: `src/main/kotlin/data/AppExport.kt`

Added `PocV2Params` data class:
```kotlin
data class PocV2Params(
    val enabled: Boolean,
    @SerializedName("model_id")
    val modelId: String,
    @SerializedName("seq_len")
    val seqLen: Long,
)
```

Included in `InferenceParams`:
```kotlin
data class InferenceParams(
    // ... existing params
    @SerializedName("poc_v2_params")
    val pocV2Params: PocV2Params? = null,
)
```

#### Modified: `internal/event_listener/integration_test.go` (decentralized-api)

Updated test mocks to include `PocV2Params`:
```go
pocV2Params := &types.PoCv2Params{
    Enabled: true,
    ModelId: "test-model-v2",
    SeqLen:  128,
}
mockQueryClient.On("Params", ...).Return(&types.QueryParamsResponse{
    Params: types.Params{
        ValidationParams: validationParams,
        PocV2Params:      pocV2Params,
    },
}, nil)
```

---

## Key Implementation Notes

### Naming Convention

Proto generates:
- Type: `PoCv2Params` (preserves message name)
- Field: `PocV2Params` (from snake_case `poc_v2_params`)

Use `params.PocV2Params` for field access, `*types.PoCv2Params` for type.

### No k_dim

V2 requests intentionally omit `k_dim`. The MLNode determines dimensions from the model.

### Stop() Semantics

**During generation**: No `Stop()` call before `InitGenerateV2`. Nodes handle concurrent generation requests.

**At validation transition**: The v2 orchestrator calls `StopPowV2()` once on all nodes **before** sending validation requests.

### validated_weight Semantics

- `validated_weight > 0` → valid vote
- `validated_weight <= 0` → invalid/reject

---

## Configuration

Governance proposal sets:
```json
{
  "poc_v2_params": {
    "enabled": true,
    "model_id": "model-name",
    "seq_len": 256
  }
}
```

---

## Implementation Status

| Component | Status |
|-----------|--------|
| Chain params (`poc_v2_params`) | Done |
| Chain weight calculation | Done |
| Chain confirmation weights | Done |
| Chain gRPC query v2 batches | Done |
| DAPI v2 orchestrator | Done |
| DAPI v2 callback URLs | Done |
| DAPI v2 commands | Done |
| DAPI StopPowV2 client | Done |
| v2 command unit tests | Done |
| testermint v2 mocks | Done |
| Integration tests | Done |
| v1 code removal | Done |

All components implemented. v1 code removed.
