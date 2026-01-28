# Off-Chain PoC Artifacts: Phase 1 Implementation

Phase 1 implements local artifact storage with MMR-based merkle commitments for the off-chain PoC proposal.

## Overview

Replaces on-chain artifact storage with a local append-only store that supports:
- High-throughput ingestion (~3.3k artifacts/sec)
- Merkle root computation for chain commits
- Proof generation for validator verification
- Snapshot proofs for historical states

## Architecture

```
MLNode -> API (postGeneratedArtifactsV2) -> ArtifactStore -> Disk
                                                |
                                                v
                                           In-Memory MMR
```

### Storage Layout

**Single-file design** with in-memory state rebuild on recovery:

| File | Format | Purpose |
|------|--------|---------|
| `artifacts.data` | `[LE32 len][LE32 nonce][vector]...` | Raw artifact payloads |

**In-memory state** (~80 MB for 1M artifacts):
- Offsets array: O(1) lookup by leaf index
- MMR nodes: all hashes for proof generation
- Nonce map: uniqueness enforcement

**Trade-off**: Recovery requires reading and re-hashing the data file (~2-3 sec for 1M artifacts). This is acceptable given recovery happens only on restart.

## MMR Specification

- **Hash**: SHA-256
- **Domain separation**: `0x00` prefix for leaves, `0x01` for internal nodes
- **Leaf encoding**: `LE32(nonce) || vector`
- **Root**: Bag peaks right-to-left
- **Snapshot proofs**: All historical MMR nodes retained, enabling proofs at any `(leafIndex, snapshotCount)` where `snapshotCount <= currentCount`

## Files Changed

### New Package: `decentralized-api/pocartifacts/`

| File | Lines | Description |
|------|-------|-------------|
| `store.go` | ~385 | ArtifactStore with Open/Close/Add/Flush/GetRoot/GetRootAt/GetFlushedRoot/GetArtifact/GetProof |
| `mmr.go` | ~379 | MMR operations: append, bag peaks, generate/verify proofs |
| `store_test.go` | ~1200 | 40+ tests covering all operations |

### Modified Files

| File | Changes |
|------|---------|
| `internal/server/mlnode/server.go` | Add optional `artifactStore` field, `WithArtifactStore` option |
| `internal/server/mlnode/post_generated_artifacts_v2_handler.go` | Dual-write: store locally when configured |

## Key Implementation Details

### Capacity Protection

```go
const MaxLeafCount = (1 << 30) - 1  // ~1B, ensures 2*n fits in int32
```

Enforced in `Add()` and `recover()`.

### Truncation Recovery

On startup, if a partial record is detected (crash mid-write), the store truncates to the last complete record and continues.

### Dual-Write Mode

When `ArtifactStore` is configured, the handler stores artifacts locally AND submits to chain. This allows gradual rollout before disabling chain storage.

```go
// Usage
store, _ := pocartifacts.Open("/path/to/artifacts")
server := mlnode.NewServer(recorder, broker, mlnode.WithArtifactStore(store))
```

### Snapshot Proofs

MMR is append-only, so all historical nodes are retained. Proofs can be generated for any `(leafIndex, snapshotCount)` where `snapshotCount <= currentCount`.

### GetFlushedRoot

Returns root and count of only **persisted** (flushed) artifacts. Safe to report externally since it survives process crashes.

```go
count, root := store.GetFlushedRoot()
// count = number of flushed artifacts
// root = MMR root at flushed count (nil if count=0)
```

## Test Coverage

| Category | Tests |
|----------|-------|
| Basic operations | Open, Add, Count, Flush, GetArtifact |
| Uniqueness | Duplicate nonce rejection |
| Recovery | Normal recovery, truncated record recovery, no index file |
| MMR correctness | Size calculation, peak positions, leaf positions |
| Proof generation | Single-peak, multi-peak, various tree sizes (1-256) |
| Proof verification | Valid proofs pass, tampered data/proof/root/index rejected |
| Snapshot proofs | Historical snapshots, proofs after recovery |
| GetRootAt | Historical roots, snapshot binding validation |
| GetFlushedRoot | Empty store, partial flush, recovery |
| Edge cases | Empty vector, large vector (1MB), negative nonces, closed store |

## Status

✅ **Complete** - merged in commit `4876fe608`
