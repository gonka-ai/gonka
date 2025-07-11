# Fix Participant Identification Inconsistency

## Problem

We have an inconsistency in how participants are identified in the network:

1. **Genesis validators**: Use account key addresses for identification
2. **Runtime validators**: Use consensus key addresses for identification

Each participant has two key pairs:
- **Account key pair**: Used for transactions and account operations
- **Consensus key pair**: Used for block validation and consensus

### Current Broken Flow

1. In `init-docker-genesis.sh`, genesis validators are created using account keys:
   ```bash
   $APP_NAME genesis gentx "$KEY_NAME" "1$MILLION_BASE"
   ```

2. In runtime, `SetComputeValidators()` creates validators using consensus keys:
   ```go
   // In createValidator function
   newValAddr, err := sdk.ValAddressFromHex(computeResult.ValidatorPubKey.Address().String())
   ```

3. This means:
   - Genesis validator has operator address derived from **account key**
   - Runtime validator has operator address derived from **consensus key**
   - Same participant = different operator addresses = broken identification

### Impact

`GetParticipantsFullStats` cannot correctly match participants to their validators because:
- It looks up validators by participant address (account-based)
- But runtime validators are indexed by consensus-key-based addresses
- This breaks validator lookup and statistics

## Solution

### Short-term Fix - [COMPLETED]

**Status**: Implemented in `GetParticipantsFullStats`

Modified the function to handle both address types by checking:
1. Account-derived validator address (for genesis validators)
2. Consensus-derived validator address (for runtime validators)

The fix ensures correct validator lookup regardless of how the validator was created.

### Long-term Fix - [COMPLETED]

1. **Use consistent addressing**: Always use account keys for validator operator addresses

2. **Fix `createValidator` in cosmos-sdk**:
   ```go
   // Instead of using consensus key address:
   // newValAddr, err := sdk.ValAddressFromHex(computeResult.ValidatorPubKey.Address().String())
   
   // Use the provided operator address directly (it's already a valoper address):
   newValAddr, err := sdk.ValAddressFromBech32(computeResult.OperatorAddress)
   if err != nil {
       return nil, err
   }
   ```

3. **Ensure `ComputeResult.OperatorAddress` contains validator operator address**:
   ```go
   // In GetComputeResults, convert account address to valoper address
   computeResults = append(computeResults, keeper.ComputeResult{
       Power:           getWeight(member),
       ValidatorPubKey: &pubKey,
       OperatorAddress: sdk.ValAddress(member.Member.Address).String(), // Convert to valoper
   })
   ```

4. **Update genesis creation**: Ensure genesis validators use same addressing pattern

## API Design Consideration - [UNDER REVIEW]

**Question**: Should `ActiveParticipant.index` be the validator operator address instead of account address?

**Current**:
```proto
message ActiveParticipant {
  string index = 1;              // account address (gonka1abc...)
  string validator_key = 2;      // consensus public key
}
```

**Alternative**:
```proto
message ActiveParticipant {
  string index = 1;              // validator operator address (gonkavaloper1abc...)
  string validator_key = 2;      // consensus public key
}
```

**Pros of using validator operator address**:
- Semantically correct (ActiveParticipant represents validators)
- Eliminates client-side address derivation
- Consistent with validator identity
- Simpler API for consumers

**Cons**:
- Breaking change requiring updates throughout codebase
- Participants currently identified by account address everywhere

**Decision**: Consider for long-term fix after testnet restart.

## Implementation Priority

1. **[COMPLETED]** Short-term fix for `GetParticipantsFullStats`
2. **[TODO]** Implement long-term fix with consistent addressing (Next testnet)
3. **[TODO]** Consider API design change for `ActiveParticipant.index`
4. **[TODO]** Verify all participant lookups work correctly across genesis and runtime validators
