# Phase 1: Storage Layer

## Summary

Implemented `payloadstorage` module in `decentralized-api/` for storing inference payloads offchain.

## Module Structure

```
decentralized-api/payloadstorage/
  storage.go           # Interface definition
  hash.go              # Hash computation helpers
  file_storage.go      # File-based implementation
  file_storage_test.go # Unit tests
```

## Interface

```go
type PayloadStorage interface {
    Store(ctx context.Context, inferenceId string, epochId uint64, promptPayload, responsePayload string) error
    Retrieve(ctx context.Context, inferenceId string, epochId uint64) (promptPayload, responsePayload string, err error)
    PruneEpoch(ctx context.Context, epochId uint64) error
}
```

Payloads are raw JSON strings (inference artifacts). IDs are parameters for organization. Retrieve requires epochId for O(1) direct file lookup.

## Key Decisions

**Atomic writes**: Write to temp file + `os.Rename()`. Atomic on POSIX. Prevents partial writes on crash.

**Directory structure**: `{baseDir}/{epochId}/{inferenceId}.json`. Epoch-based organization enables efficient pruning via `os.RemoveAll(epochDir)`.

**Hash computation**: Reuses existing functions to ensure consistency:
- Prompt: `utils.CanonicalizeJSON` + `utils.GenerateSHA256Hash` (matches `getPromptHash`)
- Response: `completionapi.CompletionResponse.GetHash()` (hashes message content only)

**Interface abstraction**: Clean separation allows future PostgreSQL backend without changing callers.

## Base Directory

Container path: `/root/.dapi/data/inference`

## Tests

- Store/Retrieve round-trip
- Not found error handling
- Wrong epoch returns ErrNotFound
- Epoch pruning
- Atomic write verification
- Hash reproducibility
- Full cycle hash integrity (json -> hash1, json -> store -> load -> hash2 == hash1)

