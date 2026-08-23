# Phase 2: Interface Extraction

Extracted `ArtifactStore` interface to enable alternative implementations.

## Changes

1. Created `interface.go` with `ArtifactStore` interface
2. Renamed `store.go` -> `mmr_store.go`
3. Renamed struct `ArtifactStore` -> `MMRArtifactStore`
4. Updated `managed_store.go` to use interface type in map

## Interface Definition

```go
type ArtifactStore interface {
    Add(nonce int32, vector []byte) error                           // deprecated
    AddWithNode(nonce int32, vector []byte, nodeId string) error
    GetRoot() []byte
    GetRootAt(snapshotCount uint32) ([]byte, error)
    GetFlushedRoot() (count uint32, root []byte)
    Count() uint32
    GetArtifact(denseIndex uint32) (nonce int32, vector []byte, err error)
    GetProof(denseIndex uint32, snapshotCount uint32) ([][]byte, error)
    GetNodeDistribution() map[string]uint32
    GetNodeCounts() map[string]uint32
    Flush() error
    Close() error
}
```

## Design Notes

The interface uses `denseIndex` terminology to clarify that indexes are sequential [0, count), regardless of underlying tree structure. For MMR this maps directly to leaf index. For SMST this will require sum-based navigation.

`Add()` is kept for backwards compatibility with existing tests but is deprecated in favor of `AddWithNode()` which tracks per-node contribution.

Compile-time interface assertion ensures `MMRArtifactStore` implements the interface:

```go
var _ ArtifactStore = (*MMRArtifactStore)(nil)
```

## Verification

All 2264 tests pass (757 API + 1507 chain).
