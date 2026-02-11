# Phase 6: Chain Migration

## Summary

Stop spending on-chain space by not passing payload data. Proto fields remain deprecated, chain fallback stays for pre-upgrade inferences.

## Changes

### Chain Calculation Layer

**inference_state.go** - Removed payload assignments:
```go
// ProcessStartInference - removed:
currentInference.PromptPayload = startMessage.PromptPayload
currentInference.OriginalPrompt = startMessage.OriginalPrompt

// ProcessFinishInference - removed:
currentInference.ResponsePayload = finishMessage.ResponsePayload
currentInference.OriginalPrompt = finishMessage.OriginalPrompt
```

### Message Validation

**message_start_inference.go**:
- Removed: `prompt_payload is required`
- Removed: `original_prompt is required`
- Added: `original_prompt_hash is required`

**message_finish_inference.go**:
- Removed: `response_payload is required`
- Removed: `original_prompt is required`
- Added: `prompt_hash is required`
- Added: `original_prompt_hash is required`

### API Layer

**post_chat_handler.go** - Stopped populating deprecated fields:

MsgStartInference:
- Removed: `PromptPayload`
- Removed: `OriginalPrompt`

MsgFinishInference:
- Removed: `ResponsePayload`
- Removed: `OriginalPrompt`

### Tests

**inference_state_test.go**:
- Removed `PromptPayload` and `ResponsePayload` from test struct literals
- Removed assertions on payload fields

## What Stays

- Proto fields remain with `[deprecated = true]` for backward compatibility
- Chain fallback `retrievePayloadsFromChain` for pre-upgrade inferences
- Testermint data classes keep fields (chain accepts but ignores them)

## Validation Summary

| Field | MsgStartInference | MsgFinishInference |
|-------|-------------------|-------------------|
| `prompt_hash` | required | required |
| `response_hash` | - | required |
| `original_prompt_hash` | required | required |
| `prompt_payload` | ignored | - |
| `response_payload` | - | ignored |
| `original_prompt` | ignored | ignored |

## Result

- New inferences: ~500 bytes (hashes + metadata only)
- Pre-upgrade inferences: validated via chain fallback
- ~200x transaction size reduction

## Files Modified

| Component | File |
|-----------|------|
| Chain | `calculations/inference_state.go` |
| Chain | `types/message_start_inference.go` |
| Chain | `types/message_finish_inference.go` |
| Chain | `calculations/inference_state_test.go` |
| API | `server/public/post_chat_handler.go` |

