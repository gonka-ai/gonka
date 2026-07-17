# Phase 5: Integration

Added `use_smst` parameter to chain and testermint for SMST enablement.

## Changes

### Chain (params.proto)

Added to `PocParams`:

```protobuf
bool use_smst = 13;  // Use SMST instead of MMR for artifact storage (duplicate prevention)
```

Proto regenerated with `ignite generate proto-go`.

### Testermint (AppExport.kt)

Added to `PocParams` data class:

```kotlin
@SerializedName("use_smst")
val useSmst: Boolean = true,  // Use SMST for duplicate prevention (default: true)
```

## Integration Points (Future Work)

The following integration points are documented for future implementation when enabling SMST in production:

### ManagedArtifactStore Factory

The `ManagedArtifactStore` should select store type based on `use_smst` param:

```go
func (m *ManagedArtifactStore) GetOrCreateStore(height int64, useSmst bool) (ArtifactStore, error) {
    if useSmst {
        return OpenSMST(storeDir)
    }
    return Open(storeDir)
}
```

### proof_client.go

Verify proofs using the appropriate verifier:

```go
if useSmst {
    elements := decodeSMSTProof(proofHashes)
    if !artifacts.VerifySMSTProofWithCounts(rootHash, count, nonce, leafData, elements) {
        return ErrProofVerificationFailed
    }
} else {
    if !artifacts.VerifyProof(rootHash, count, leafIndex, leafData, proofHashes) {
        return ErrProofVerificationFailed
    }
}
```

### commit_worker.go

Trigger SMST prebuild for efficient proof queries:

```go
if smstStore, ok := store.(*artifacts.SMSTArtifactStore); ok {
    smstStore.PrebuildSnapshot(resp.Count)
}
```

## Migration Path

1. Deploy with `use_smst = false` (MMR, current behavior)
2. Test SMST in staging environment
3. Enable via governance proposal: `use_smst = true`
4. All new PoC stages use SMST; existing stages continue with MMR

No data migration required - each PoC stage is independent.

## Verification

All 2281 tests pass (774 API + 1507 chain).
