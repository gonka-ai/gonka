## Model Name Usage in Inference Flow

### Model Preloading During Phase Transitions

**Automatic Model Loading:**
Models are proactively preloaded to MLNodes during epoch phase transitions, specifically when the system transitions from PoC validation phase to Inference phase. This ensures models are ready before inference requests arrive.

**Phase Transition Detection:**
The `handlePhaseTransitions` function in `decentralized-api/internal/event_listener/new_block_dispatcher.go` monitors blockchain state and detects when the PoC validation phase ends. When this transition occurs, it queues an `InferenceUpAllCommand` to the broker.

**Broker Command Processing:**
The `InferenceUpAllCommand` in `decentralized-api/broker/state_commands.go` sets the intended status of all operational nodes to `HardwareNodeStatus_INFERENCE`, triggering the reconciliation process that will ensure all nodes transition to inference mode.

**Model Selection and Loading:**
The actual model preloading occurs in `InferenceUpNodeCommand.Execute` in `decentralized-api/broker/node_worker_commands.go`. This command performs several key operations:

**Accesses Broker's Stored Models**: The command uses the `worker.node.Node.Models` map that was populated during node registration via `RegisterNode.Execute`. This map contains all the models configured for each specific MLNode.

**Selects First Available Model**: The current implementation uses the first model from the node's configured model list. This means if a node supports multiple models, only the first one in the configuration will be preloaded during phase transitions.

**Calls MLNode Client**: The command executes the MLNode client's `InferenceUp` method with the selected model name and its configured arguments to actually load the model on the hardware node.

**Preloading vs Request-Time Loading:**
The system uses two different approaches for model availability:
- **Preloading**: Models are loaded automatically during phase transitions using the first available model from each node's configuration
- **Request-Time**: When inference requests arrive, the system uses exact model name matching for node selection, but the model should already be loaded from the preloading phase

This preloading mechanism ensures that MLNodes are ready to serve inference requests immediately when the Inference phase begins, rather than experiencing delays from loading models on-demand when the first requests arrive.

### Inference Request Routing

**Client Request Processing:**
When clients submit inference requests to the public API at `/v1/chat/completions`, the request includes a `model` field specifying which AI model should handle the inference. This model name comes directly from the client's OpenAI-compatible request.

**Executor Selection Process:**
The `getExecutorForRequest` function in `decentralized-api/internal/server/public/post_chat_handler.go` uses the requested model name to query the blockchain for an appropriate executor. It calls `queryClient.GetRandomExecutor` with the specific model name from the client request.

**Node Availability Checking:**
The broker's `nodeAvailable` function in `decentralized-api/broker/broker.go` performs exact string matching between the requested model name and the model identifiers stored in each broker's MLNode's `Models` map. A node is only considered available if it contains the exact model string as a key in its Models map.

**Model Name Consistency Requirement:**
**Critical:** The model name in the client's inference request must exactly match one of the model identifiers configured in an MLNode's configuration. For example, if an MLNode is configured with "Qwen/Qwen2.5-7B-Instruct", then inference requests must specify this exact string in their model field.

### Inference Blockchain Record

**Start Inference Transaction:**
When an inference begins, the `MsgStartInference` transaction records the model name from the client request in its `Model` field. This creates a permanent blockchain record associating the inference with the specific model that was requested.

**Inference State Storage:**
The blockchain stores the model name in the `Inference` structure in `inference-chain/x/inference/types/inference.pb.go`. This model field is populated during the `StartInference` message handling and remains part of the inference record throughout its lifecycle.

**Finish Inference Transaction:**
The `MsgFinishInference` transaction does NOT include a model field. The model information is already recorded in the inference state from the start transaction, so only the inference results need to be submitted.

**Model Validation During Recording:**
Currently, there is no validation that the model name recorded in the blockchain corresponds to a governance-approved model. The system accepts and records whatever model string was provided in the client request.

### Inference Request Model Name Flow Summary

The complete flow of model names through the system:

1. **Client Request**: Client specifies model name in OpenAI request (e.g., "Qwen/Qwen2.5-7B-Instruct")
2. **Executor Selection**: System queries blockchain for executors supporting that exact model name
3. **Node Matching**: Broker checks MLNode configurations for exact string match in Models map
4. **Blockchain Recording**: `MsgStartInference` permanently records the requested model name
5. **Inference Execution**: Selected MLNode loads the model using the configured arguments from its Models map

**Key Insight:** Model names serve as the critical link between client requests, MLNode capabilities, and blockchain records. The system requires exact string matching at all levels, making model name consistency essential for proper operation.

## Model Names in Inference Validation

### Overview

The inference validation system is model-aware, requiring exact model name matching throughout the validation process. Model names flow through three distinct phases of validation, ensuring consistency from private validator selection to final compliance verification.

**Note**: This section focuses on model-specific aspects of the validation process. For a comprehensive overview of the complete inference validation flow including secret seed mechanisms, validator assignment, and retroactive verification, see [`inference-validation-flow.md`](/docs/specs/inference-validation-flow.md).

### Phase 1: Model-Based Private Validation Selection

**Model-Specific Validator Pool:**
During private validation selection, the system uses model-specific epoch subgroups to determine validator eligibility. When `SampleInferenceToValidate` in `decentralized-api/internal/validation/inference_validation.go` is called with finished inference IDs, the system:

1. Extracts Model Information: Reads the model field from each inference record (stored during `MsgStartInference`)
2. **IMPORTANT**: Calls `GetInferenceValidationParameters` in `inference-chain/x/inference/keeper/query_get_inference_validation_parameters.go` which retrieves validator weights from the **main current epoch group**, not model-specific subgroups
3. Model-Specific Weight Calculation: The `ShouldValidate` algorithm in `inference-chain/x/inference/calculations/should_validate.go` uses the `TotalPower` from model-specific subgroup and validator's weight from main epoch group
4. Deterministic Model-Aware Selection: Secret seed combined with inference ID and model-specific power determines validation assignment

**Key Implementation Detail:**
The `InferenceValidationDetails` structure in `inference-chain/x/inference/types/inference_validation_details.pb.go` contains:
- `Model` field: Records which model the inference used
- `TotalPower` field: Populated from model-specific epoch subgroup's `TotalWeight` (from `inference-chain/x/inference/keeper/msg_server_finish_inference.go`)
- `ValidatorPower` field: Retrieved from main epoch group weights, not model subgroup

**Model Matching for Execution:**
When validators are selected to validate specific inferences, they must use MLNodes supporting the same model:

- Calls `broker.LockNode` with the inference's exact model name in `decentralized-api/internal/validation/inference_validation.go`
- Performs identical string matching used during original inference execution via `nodeAvailable` in `decentralized-api/broker/broker.go`
- Ensures verification uses same model arguments from Models map

### Phase 2: Model-Consistent Validation Publication

**Model-Aware Verification Execution:**
During validation result publication, model consistency is maintained through, selected MLNode runs the exact same model as the original inference using the `lockNodeAndValidate` function.

**Validation Result Recording:**
Published `MsgValidation` transactions include, doesn't include model id, but implicit model information through inference ID reference via `inference-chain/x/inference/keeper/msg_server_validation.go`.

### Phase 3: Model-Aware Compliance Verification

**Model-Specific Retroactive Checking:**
During seed revelation and claim verification, the system performs model-aware compliance checks in `getMustBeValidatedInferences` function in `inference-chain/x/inference/keeper/msg_server_claim_rewards.go`:

**Model Weight Map Creation:**
The system creates separate weight maps for each model:
- **Main epoch group**: All participants regardless of model support (retrieved via `getEpochGroupWeightData` with empty model ID)
- **Model subgroups**: Individual weight maps per model containing only supporting participants (retrieved via `getEpochGroupWeightData` with specific model ID)

**Model-Based Validation Requirements:**
For each inference requiring validation:
1. Model Identification: Extracts model field from `InferenceValidationDetails.Model`
2. Appropriate Weight Map Selection: Uses model-specific weight map for `totalWeight` calculations
3. Model Subgroup Membership Check: Verifies both validator and executor support the model through weight map presence
4. Model-Specific ShouldValidate: Runs calculation using model subgroup total weight, but validator weight from main epoch group

**Model Compliance Enforcement:**
- Exact Match Requirement: Validator must have validated all inferences for models they support
- Model Specialization Tracking: System tracks validation performance per model through the weight map filtering
- Cross-Model Prevention: Weight map filtering prevents validation assignments across unsupported models

### Model Name Consistency Mechanisms

**End-to-End Model Tracking:**
1. Client Request: Specifies exact model name
2. Original Execution: MLNode uses exact model string for inference
3. Blockchain Storage: Model name permanently recorded in inference record
4. Validator Selection: Uses model name for subgroup filtering and weight calculations
5. Verification Execution: Same model string ensures identical re-execution environment
6. Compliance Verification: Model name used for retroactive validation requirement calculation

**Critical Model Dependencies:**
- String Matching: All phases require identical model name strings
- Subgroup Membership: Participants must be in correct model-specific groups
- Configuration Consistency: MLNodes must have matching model arguments across participants
- Validation Thresholds: Model-specific criteria must be maintained in validation logic (see `ModelToPassValue` map in `inference-chain/x/inference/keeper/msg_server_validation.go`)

