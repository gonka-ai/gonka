# Phase 3: Signature Migration

## Summary

This phase migrates signatures from full payloads to cryptographic hashes, reducing signature computation overhead while maintaining security.

### Changes

- **Developer signature**: Signs `SHA256(original_prompt) + timestamp + ta_address`
- **Transfer Agent signature**: Signs `SHA256(prompt_payload) + timestamp + ta_address + executor_address`
- **Executor signature**: Signs `SHA256(prompt_payload) + timestamp + ta_address + executor_address`

### Terminology

| Term | Description |
|------|-------------|
| `original_prompt` | Raw user request body |
| `original_prompt_hash` | `SHA256(original_prompt)` - what developer signs |
| `prompt_payload` | Modified request (original_prompt + seed) |
| `prompt_hash` | `SHA256(prompt_payload)` - what TA/Executor sign |

## Implementation Details

### Proto Changes

**tx.proto** - Transaction messages:
- `MsgStartInference`: Added `original_prompt_hash` (field 16)
- `MsgFinishInference`: Added `prompt_hash` (field 15), `original_prompt_hash` (field 16)
- Deprecated: `original_prompt`, `prompt_payload`, `response_payload`

**inference.proto** - Stored state:
- `Inference`: Added `original_prompt_hash` (field 33)
- Deprecated: `original_prompt`, `prompt_payload`, `response_payload`

### Chain Changes

**msg_server_start_inference.go**:
- `getDevSignatureComponents`: Returns components with `original_prompt_hash` as payload
- `getTASignatureComponents`: Returns components with `prompt_hash` as payload
- `verifyKeys`: Verifies dev and TA signatures separately with different components

**msg_server_finish_inference.go**:
- `getFinishDevSignatureComponents`: Returns components with `original_prompt_hash` as payload
- `getFinishTASignatureComponents`: Returns components with `prompt_hash` as payload
- `verifyFinishKeys`: Verifies dev, TA, and executor signatures with appropriate components

### API Changes

**utils.go**:
- `validateTransferRequest`: Validates user signature against `SHA256(request.Body)` instead of raw body
- `validateExecuteRequestWithGrantees`: Validates TA signature against `prompt_hash` header

**post_chat_handler.go**:
- `createInferenceStartRequest`: TA signs `prompt_hash`, populates `original_prompt_hash`
- `sendInferenceTransaction`: Computes both hashes, executor signs `prompt_hash`
- Added `X-Prompt-Hash` header for executor hash validation

**entities.go**:
- `ChatRequest`: Added `PromptHash` field for receiving hash from TA

### Testermint Changes

**data/inference.kt**:
- `MsgStartInference`: Added `originalPromptHash`
- `MsgFinishInference`: Added `promptHash`, `originalPromptHash`
- `InferencePayload`: Added `originalPromptHash`

**InferenceTests.kt**:
- Added `sha256()` utility function
- All tests updated to create signatures over hashes instead of full payloads

## Issues Found During Implementation

### 1. Separate Signature Components Required

The initial plan assumed a single `SignatureComponents` struct could be used for all signatures. However, since dev and TA sign different data (original_prompt_hash vs prompt_hash), we needed separate component functions:
- `getDevSignatureComponents` / `getFinishDevSignatureComponents`
- `getTASignatureComponents` / `getFinishTASignatureComponents`

### 2. Executor Hash Validation

Added explicit hash validation in executor to catch mismatches early:
- TA sends `X-Prompt-Hash` header to executor
- Executor computes its own hash from modified request body
- Mismatch returns HTTP 400 before ML inference

### 3. Direct Executor Flow Fallback

In `validateExecuteRequestWithGrantees`, added fallback for when `PromptHash` header is empty:
```go
payload := request.PromptHash
if payload == "" {
    payload = utils.GenerateSHA256Hash(string(request.Body))
}
```
This handles the direct executor flow where the client acts as both dev and TA, sending the original_prompt as the body. The hash is computed from the body, which correctly validates the signature. Will be removed in Phase 6 when payload fields are eliminated.

## Signature Verification Architecture

**Dual Verification Model:**
The system uses both off-chain and on-chain signature verification for security and user experience:

**Off-chain verification (API layer):**
- Transfer Agent verifies dev signature before accepting request
- Executor verifies dev + TA signatures before running inference
- Purpose: Early rejection with clear error messages, reduces wasted computation
- Location: `decentralized-api/internal/server/public/utils.go`

**On-chain verification (canonical security):**
- Chain verifies dev + TA + executor signatures in transaction messages
- Purpose: Cryptographic proof that request is authentic, prevents malicious TAs
- Location: `inference-chain/x/inference/keeper/msg_server_*.go`

Both layers are required: off-chain provides UX, on-chain provides security guarantees.

## Breaking Change

This is a breaking change requiring coordinated deployment of:
- Chain nodes (inference-chain)
- API nodes (decentralized-api)
- Client SDKs (signature creation)

## Test Coverage

All inference-related testermint tests verify:
- Valid signatures with hash-based components
- Invalid dev signatures rejected
- Invalid TA signatures rejected
- Invalid executor signatures rejected
- Timestamp validation still works
- Duplicate request rejection still works

Run tests with:
```bash
./gradlew :test --tests "InferenceTests"
```

## Files Modified

| Component | File | Change |
|-----------|------|--------|
| Chain | `proto/.../tx.proto` | Add hash fields, deprecate payloads |
| Chain | `proto/.../inference.proto` | Add original_prompt_hash, deprecate payloads |
| Chain | `keeper/msg_server_start_inference.go` | Separate components for dev vs TA |
| Chain | `keeper/msg_server_finish_inference.go` | Separate components for dev vs TA/executor |
| API | `utils/api_headers.go` | Add X-Prompt-Hash header constant |
| API | `internal/server/public/utils.go` | Validate sig with original_prompt_hash |
| API | `internal/server/public/post_chat_handler.go` | TA/executor sign prompt_hash, hash validation |
| API | `internal/server/public/entities.go` | Add PromptHash to ChatRequest |
| Testermint | `data/inference.kt` | Add hash fields to data classes |
| Testermint | `InferenceTests.kt` | Update signatures to use hashes |

## Test Results

### Initial Results (Commit a6bdf12)

**17 tests, 3 failures, 14 passed**

**Failures:**
1. `valid inference()` - HTTP 401 Unauthorized  
2. `valid direct executor request()` - Executor signature mismatch
3. `repeated request rejected()` - HTTP 401 Unauthorized

**Root causes:**
1. Tests using manual string concatenation for signature instead of helper with named parameters
2. Direct executor test expecting executor signature to match TA signature (they sign different hashes)

### Final Results (After Fixes)

**All tests passing ✅**

**Fixes applied:**
1. `repeated request rejected` - Fixed signature creation to use named parameters instead of manual concatenation
2. `valid direct executor request` - Removed assertion checking executor signature matches TA signature (they sign different hashes: executor signs `promptHash` after modifications, TA signs `originalPromptHash`)
3. `InferenceAccountingTests.kt` - Updated to sign hash instead of full payload (one line change: `sha256(payload)`)

**Key insight:** Executor creates its own signature over the modified request (with seed/logprobs), which differs from what test computes. This is expected behavior - test now validates inference completes successfully without asserting exact executor signature match.

### Test Helper Method Added

**File:** `testermint/src/main/kotlin/ApplicationCLI.kt`

Added `signRequest()` method to auto-hash requests before signing:
```kotlin
fun signRequest(request: String, accountAddress: String? = null, 
                timestamp: Long? = null, endpointAccount: String? = null): String {
    val hash = sha256(request)
    return signPayload(hash, accountAddress, timestamp, endpointAccount)
}
```

**Pattern:**
- Use `signRequest()` when signing inference requests (auto-hashes)
- Use `signPayload()` for manual signature component construction

**Updated methods:**
- `LocalInferencePair.makeInferenceRequest()` - Uses `signRequest()`
- `LocalInferencePair.streamInferenceRequest()` - Uses `signRequest()`

All test files now use `signRequest()` consistently.

