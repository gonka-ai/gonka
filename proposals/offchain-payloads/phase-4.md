# Phase 4: Validator Retrieval

## Summary

Validators retrieve inference payloads from executor REST API instead of querying chain state. Provides cryptographic verification of retrieved payloads and executor signature for non-repudiation.

### Key Features

- Executor serves payloads via REST endpoint with signed response
- Validator verifies both prompt and response hashes match on-chain commitment
- Executor signature enables fast invalidation without voting (Phase 7)
- Fallback to chain retrieval for pre-upgrade inferences

## Implementation Details

### New Files

**payload_handlers.go** - Executor endpoint:
- `GET /v1/inference/:inferenceId/payloads` - serves payloads to validators
- Request validation: timestamp (60s window), active participant check, signature verification
- Response includes `ExecutorSignature` for non-repudiation

**payload_retrieval.go** - Validator client:
- `RetrievePayloadsFromExecutor` - fetches payloads from executor REST API
- HTTP client with 30s timeout
- Verifies both `PromptHash` and `ResponseHash` match on-chain
- Verifies executor signature before returning payloads

### Endpoint: Executor Payload Serving

```
GET /v1/inference/{inferenceId}/payloads

Headers:
  X-Validator-Address: <validator_address>
  X-Timestamp: <unix_nano>
  X-Epoch-Id: <epoch_id>
  Authorization: <signature>

Response:
  {
    "inference_id": "...",
    "prompt_payload": "...",
    "response_payload": "...",
    "executor_signature": "..."
  }
```

### URL Encoding

InferenceId uses base64url encoding (RFC 4648) in the URL path:
- `+` replaced with `-`
- `/` replaced with `_`

Standard base64 contains `/` which breaks HTTP path routing. Conversion happens at the HTTP layer only - chain storage remains standard base64.

### Authentication Protocol

**Request (Validator -> Executor):**
- Validator signs: `inferenceId + timestamp + validatorAddress`
- Signature type: Developer (reuses Phase 3 pattern)
- Supports warm keys via `getAllowedPubKeys`

**Response (Executor -> Validator):**
- Executor signs: `inferenceId + SHA256(prompt_payload) + SHA256(response_payload)`
- Timestamp: 0 (non-repudiation only, replay protection at request level)
- Signature provides cryptographic proof for fast invalidation

### Security Measures

1. **Timestamp validation**: Reject requests >60s old or >10s in future
2. **Participant verification**: Only active validators at inference epoch can request
3. **Signature verification**: Request must be signed by validator's key or grantees
4. **Hash verification**: Both prompt and response hashes must match on-chain commitment
5. **Executor signature**: Non-repudiable proof for Phase 7 invalidation

### Hash Computation

Uses same functions as chain to ensure consistency:
- `payloadstorage.ComputePromptHash` - canonicalizes JSON then hashes
- `payloadstorage.ComputeResponseHash` - hashes message content only

### Validation Flow Changes

**inference_validation.go**:
- `retrievePayloadsWithRetry` - retries up to 10 times (2min intervals, ~20min total)
- Falls back to deprecated chain retrieval after retries exhausted
- `validateWithPayloads` - validates using retrieved payloads

### Caching

**epoch_group_cache.go** - Multi-epoch cache for participant lookups:
- `IsActiveParticipant` - O(1) lookup using prebuilt address set
- Keeps max 2 epochs cached
- Auto-prunes old epochs

### New Headers

```go
XValidatorAddressHeader = "X-Validator-Address"  // Phase 4
XEpochIdHeader          = "X-Epoch-Id"           // Phase 4
```

## Testing

Unit tests in `payload_handlers_test.go`:
- Valid/invalid signature verification
- Timestamp validation (too old, future)
- Multiple grantees (warm key support)
- Executor signature format and hash mismatch detection

## Rollback

If issues arise:
1. Validators automatically fall back to chain retrieval after retry exhaustion
2. Chain still stores payload fields (removed in Phase 6)
3. No breaking changes to existing flows

