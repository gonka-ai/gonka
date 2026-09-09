# Phase 3: SMST Implementation

Implemented Sparse Merkle Sum Tree (SMST) for duplicate-proof artifact storage.

## Files Created

1. `smst.go` - Core tree operations
2. `smst_store.go` - Store implementing ArtifactStore interface  
3. `smst_verify.go` - Proof verification
4. `smst_test.go` - Unit tests

## SMST Design

The tree is a fixed-depth binary tree where nonce bits determine the path from root to leaf.

Node structure:
```go
type smstNode struct {
    hash  []byte   // SHA-256 hash
    count uint32   // sum of leaf counts in subtree
    left  *smstNode
    right *smstNode
}
```

Sum property enables dense index navigation in a sparse tree:
```
if index < left.count: go left
else: go right, index -= left.count
```

Hash computation includes the count:
```go
hash = SHA256(0x01 || left_hash || right_hash || count_le32)
```

## Key Features

Dynamic depth: Default depth is 24 (supports nonces up to 16.7M). Larger nonces automatically expand the tree depth up to 32.

Duplicate prevention: Inserting the same nonce returns `ErrDuplicateNonce`. This is the core property that prevents the duplicate attack.

Dense indexing: Validators sample using dense indices [0, count), which the tree navigates using sum-based traversal.

Snapshot support: The store tracks committed roots in `flushedRoots` map and can rebuild trees at historical counts via `rebuildTreeAt()`.

## Proof Format

Proofs contain sibling hashes with their subtree counts, enabling verification without inferring counts:

```go
type SMSTProofElement struct {
    SiblingHash  []byte  // 32 bytes
    SiblingCount uint32  // 4 bytes
}
```

Transport format: 36 bytes per level (32 hash + 4 count).

## Verification

Verification reconstructs the root by hashing from leaf to root:

1. Start with `leafHash = SHA256(0x00 || leafData)`
2. At each level, combine with sibling using path direction
3. Include count in hash: `SHA256(0x01 || left || right || count)`
4. Compare final hash and count with expected root

## Interface Compliance

`SMSTArtifactStore` implements the full `ArtifactStore` interface:
- Add/AddWithNode - insert with duplicate rejection
- GetRoot/GetRootAt/GetFlushedRoot - root hashes
- GetArtifact - retrieval by dense index
- GetProof - proof generation
- Node distribution tracking
- Persistence via artifacts.data (same format as MMR)

## Test Coverage

13 new tests covering:
- Empty tree, insert, count
- Duplicate rejection
- Dense index navigation
- Root consistency (insertion order independent)
- Depth expansion
- Store basics and recovery
- Proof generation and verification
- Proof encoding/decoding
