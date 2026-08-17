# Phase 6: Security Verification

Verified SMST implementation provides cryptographic guarantees against duplicate attacks.

## Security Properties

### Duplicate Prevention

The SMST structure inherently prevents duplicates by using the nonce as the path determinant.

- `Insert(nonce, ...)` checks `hasNonce` map for immediate duplicate rejection
- Structurally, a nonce `N` maps to a unique path `P`. It is impossible to store two different values for nonce `N` in the same tree
- Verification `VerifySMSTProofWithCounts` reconstructs the path based on the claimed nonce. If an attacker tries to store nonce `N` at path `P'` (to duplicate it), the verification will fail because it will attempt to traverse path `P`

### Count Inflation Prevention

The attack where a malicious participant claims a higher count (e.g., 100) than actual artifacts (e.g., 50) is prevented by the Merkle Sum Tree properties.

1. Count Commitment: The root hash includes the total `count` of leaves via `H_root = Hash(left, right, count)`. To claim `Count=100`, the attacker must provide a root hash that corresponds to a tree with 100 items.

2. Index Binding: The verifier requests an artifact at a specific dense index `i`. `VerifySMSTProofWithDenseIndex` verifies that the provided proof actually leads to the `i`-th non-empty leaf by summing the counts of left siblings along the path.

3. Since sibling counts are committed in the node hashes, the attacker cannot forge the index without changing the root hash.

### Verified Index Logic

The function `VerifySMSTProofWithDenseIndex` in `smst_verify.go` implements the critical check:

```go
computedIndex := uint32(0)
for i := 0; i < depth; i++ {
    if path[i] {
        computedIndex += elements[i].SiblingCount
    }
}
return computedIndex == denseIndex
```

This ensures that if the validator asks for the 90th artifact, the proof must correspond to the 90th artifact in the tree committed to by `root_hash`.

## Hardening Tests

Added explicit attack scenario tests in `smst_test.go`:

| Test | What it verifies |
|:-----|:-----------------|
| `TestSMSTCountInflationAttackFails` | Claiming higher count than actual leaves fails |
| `TestSMSTProofWithWrongCountFails` | count-1, count+1 variations all fail verification |
| `TestSMSTNonceHasUniqueDenseIndex` | Each nonce maps to exactly ONE dense index |
| `TestSMSTNegativeNonces` | Negative int32 nonces handled correctly (depth expands to 32) |
| `TestSMSTCountBoundaries` | Edge cases: count=0, denseIndex >= count, single leaf |

These tests document the security properties and provide regression protection.

## Verification

All 44 SMST tests pass including 5 new security hardening tests.

## Implementation Details

### Proof Format

Each proof element is exactly 36 bytes:
```
[sibling_hash (32 bytes)] [sibling_subtree_count (4 bytes, Little Endian)]
```

Proof elements are ordered from root to leaf. The verifier infers tree depth from proof length.

### Root Reconstruction

1. Start with `currentHash = hash(0x00 || leafData)`, `currentCount = 1`
2. For each proof level (leaf to root):
   - Determine direction from nonce path bit
   - Combine: `currentHash = hash(0x01 || leftHash || rightHash || combinedCount)`
   - Accumulate: `currentCount = myCount + siblingCount`
3. Verify: `currentCount == claimed_count` AND `currentHash == claimed_root`

### Dense Index Binding

After root verification, compute the dense index from sibling counts:
```
computedIndex = 0
for level in 0..depth:
    if nonce_path[level] is RIGHT:
        computedIndex += sibling_count[level]
return computedIndex == claimed_dense_index
```

The `computedIndex` equals the count of leaves with nonces numerically smaller than the current nonce. Sibling counts are committed by the root hash, making index binding cryptographically sound.

### Snapshot Consistency

The proof endpoint must serve artifacts and proofs from the same snapshot tree state. Unlike MMR where leaf positions were stable, SMST dense indices change as leaves are added.

The interface enforces this with a single read method:
```go
GetArtifactAndProof(denseIndex, snapshotCount uint32) (nonce int32, vector []byte, proof [][]byte, error)
```

Separate `GetArtifact`/`GetProof` methods are intentionally omitted to prevent mixing tree states.

### Verifier Hardening

`VerifySMSTProofWithDenseIndex` uses:
- `uint64` accumulator for overflow protection
- Explicit `count == 0` rejection
- Defense-in-depth bounds checking during accumulation

## Conclusion

The SMST implementation provides cryptographic guarantees against:

- Duplicate Nonces: Structural impossibility (one slot per nonce)
- Count Inflation: Detected by sibling count commitment in root hash
- Index Swapping: Detected by path-to-nonce verification
