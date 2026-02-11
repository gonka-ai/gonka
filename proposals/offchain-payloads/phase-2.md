# Phase 2: Dual Write

## Summary

Executor now stores inference payloads both on-chain (existing) and to local storage (Phase 1 PayloadStorage). Consistency verification validates hashes match after storage.

## Changes

**Server struct** (`internal/server/public/server.go`):
- Added `payloadStorage payloadstorage.PayloadStorage`
- Added `phaseTracker *chainphase.ChainPhaseTracker`
- Initialized `FileStorage` with base path `/root/.dapi/data/inference`

**Executor flow** (`internal/server/public/post_chat_handler.go`):
- `handleExecutorRequest`: Computes `promptPayload` via `utils.CanonicalizeJSON(modifiedRequestBody.NewBody)`
- `sendInferenceTransaction`: Added `promptPayload` parameter, calls `storePayloadsToStorage` after transaction broadcast
- `storePayloadsToStorage`: Gets epoch from `phaseTracker`, stores both payloads
- `verifyStoredPayloads`: Retrieves stored payloads, computes hashes, logs warnings on mismatch

## Storage Call

After `s.recorder.FinishInference(message)`:
1. Get epoch: `s.phaseTracker.GetCurrentEpochState().LatestEpoch.EpochIndex`
2. Store: `s.payloadStorage.Store(ctx, inferenceId, epochId, promptPayload, responsePayload)`
3. Verify: Retrieve and compare hashes (temporary, marked for deletion)

## Key Decisions

**Non-blocking**: Storage failures are logged but don't fail inference. On-chain storage remains primary.

**Epoch from phaseTracker**: Uses cached epoch state for efficiency, avoids chain queries.

**Consistency check temporary**: Verification validates the dual write implementation. Will be removed once confidence is established.

## Tests

- `TestStorePayloadsToStorage_Success`: Verifies storage called with correct payloads
- `TestStorePayloadsToStorage_NilStorage`: Handles nil storage gracefully
- `TestStorePayloadsToStorage_NilPhaseTracker`: Handles nil tracker gracefully
- `TestVerifyStoredPayloads_HashMatch`: Validates hash comparison
- `TestVerifyStoredPayloads_RetrieveError`: Handles retrieve errors
- `TestDualWriteIntegration`: Full cycle with real FileStorage





