# Phase 4-5: Validation Flow Diagram

## Overview

This document describes the payload retrieval and validation flow for Phase 4 (Validator Retrieval) and Phase 5 (Validation Migration).

## Validation Flow

```
                         VALIDATOR NODE
                              │
                              ▼
            ┌─────────────────────────────────┐
            │  validateInferenceAndSendVal    │
            │  Message(inference)             │
            └─────────────────────────────────┘
                              │
                              ▼
            ┌─────────────────────────────────┐
            │  retrievePayloadsWithRetry()    │
            │  (max 30 retries, 2min each)    │
            └─────────────────────────────────┘
                              │
                              ▼
            ┌─────────────────────────────────┐
            │  RetrievePayloadsFromExecutor() │
            └─────────────────────────────────┘
                              │
                              ▼
     ┌────────────────────────────────────────────────┐
     │  HTTP GET /v1/inference/{id}/payloads          │
     │  (id is base64url encoded: +→- /→_)            │
     │  Headers: X-Validator-Address, X-Timestamp,    │
     │           X-Epoch-Id, Authorization            │
     └────────────────────────────────────────────────┘
                              │
                              ▼
                       EXECUTOR NODE
                              │
                              ▼
            ┌─────────────────────────────────┐
            │  Validate request:              │
            │  - Timestamp (60s window)       │
            │  - Active participant check     │
            │  - EpochId verification         │
            │  - Signature verification       │
            └─────────────────────────────────┘
                              │
                              ▼
            ┌─────────────────────────────────┐
            │  Retrieve from PayloadStorage   │
            │  Sign response (non-repudiation)│
            └─────────────────────────────────┘
                              │
                              ▼
     ┌────────────────────────────────────────────────┐
     │  Response: { inference_id, prompt_payload,     │
     │              response_payload, executor_sig }  │
     └────────────────────────────────────────────────┘
                              │
                              ▼
                       VALIDATOR NODE
                              │
                              ▼
            ┌─────────────────────────────────┐
            │  1. Verify executor signature   │◄─────────┐
            │     (FIRST - before hash check) │          │
            └─────────────────────────────────┘          │
                       │                                 │
           ┌───────────┴───────────┐                     │
           ▼                       ▼                     │
     ┌──────────┐           ┌──────────────┐             │
     │ INVALID  │           │    VALID     │             │
     │ signature│           │   signature  │             │
     └──────────┘           └──────────────┘             │
           │                       │                     │
           ▼                       ▼                     │
     ┌──────────┐           ┌──────────────┐             │
     │  RETRY   │──────────►│ 2. Check hash│             │
     │  (error) │  exhaust  │    vs chain  │             │
     └──────────┘           └──────────────┘             │
           │                       │                     │
           ▼               ┌───────┴───────┐             │
     ┌───────────────┐     ▼               ▼             │
     │ ErrPayload    │ ┌─────────┐   ┌───────────┐       │
     │ Unavailable   │ │ MATCH   │   │ MISMATCH  │       │
     │ (invalidate)  │ └─────────┘   └───────────┘       │
     └───────────────┘     │               │             │
                           ▼               ▼             │
                     ┌──────────┐   ┌───────────────┐    │
                     │ Continue │   │ ErrHashMismatch│   │
                     │ to       │   │ (immediate     │   │
                     │ validate │   │  invalidation) │   │
                     └──────────┘   └───────────────┘    │
                           │                             │
                           ▼                             │
            ┌─────────────────────────────────┐          │
            │  validateWithPayloads()         │          │
            │  - Run inference with enforced  │          │
            │    tokens                       │          │
            │  - Compare logits               │          │
            └─────────────────────────────────┘          │
                           │                             │
                           ▼                             │
            ┌─────────────────────────────────┐          │
            │  Submit MsgValidation to chain  │          │
            │  (value = similarity score)     │          │
            └─────────────────────────────────┘          │
```

## Decision Matrix

| Signature | Hash Check | Action |
|-----------|------------|--------|
| Invalid | - | Retry → after exhaustion → unavailability invalidation |
| Valid | Mismatch | Immediately vote invalid (no retry) |
| Valid | Match | Proceed to validation |

## Key Security Points

1. **Signature verification FIRST**: Validates the executor actually signed these payloads
2. **Hash check SECOND**: Only after confirming payload is signed by executor
3. **No retry on hash mismatch**: Executor definitively signed wrong data
4. **Retry on signature failure**: Could be network issue, corrupted data

## Storage Order (Executor Side)

```
┌─────────────────────────────────┐
│ 1. Store payloads to storage    │  ← FIRST
└─────────────────────────────────┘
              │
              ▼
┌─────────────────────────────────┐
│ 2. Broadcast MsgFinishInference │  ← SECOND
└─────────────────────────────────┘
```

Rationale: If storage fails after broadcast, validators cannot retrieve payloads, leading to unjust invalidation.

## Error Types

- `ErrPayloadUnavailable`: Retries exhausted, no on-chain fallback (post-upgrade inference)
- `ErrHashMismatch`: Executor served wrong payload with valid signature

## TODO

- Phase 7: Use executor's signed proof for fast invalidation (without voting) on hash mismatch

