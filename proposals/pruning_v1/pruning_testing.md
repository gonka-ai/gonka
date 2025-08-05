# Pruning Testing Design Document

## Problem Statement

The Gonka blockchain implements pruning of Proof of Computation (PoC) data to maintain chain efficiency and reduce storage requirements. PoC data includes PoCBatch and PoCValidation objects that are stored on the chain. After a certain threshold of epochs, this data is pruned (removed) from the chain state.

Currently, there is no straightforward way to verify that pruning has occurred correctly in end-to-end (e2e) tests. We need a mechanism to confirm that PoC data has been properly pruned after the pruning threshold has been reached.

## Current Pruning Implementation

The current pruning implementation is handled by the `PrunePoCData` function in the keeper package. This function:

1. Checks if pruning is enabled (threshold > 0)
2. Iterates through previous epochs
3. Skips epochs that aren't old enough to be pruned (based on the pruning threshold)
4. For eligible epochs, retrieves and removes all PoCBatches and PoCValidations

The pruning process removes PoCBatch and PoCValidation objects from the store but doesn't provide a way to verify that pruning has occurred correctly.

## Proposed Solution

To enable e2e testing of PoC pruning, we propose implementing two new queries:

1. **CountPoCBatchesAtHeight** - Returns the count of PoCBatch objects for a specific block height
2. **CountPoCValidationsAtHeight** - Returns the count of PoCValidation objects for a specific block height

These queries will be exposed via the CLI, allowing e2e tests to verify that pruning has occurred correctly by checking that the count of PoCBatch and PoCValidation objects is zero after the pruning threshold has been reached.

### Query Specifications

#### CountPoCBatchesAtHeight

- **Parameters**:
  - `block_height` (int64): The block height to count PoCBatch objects for
- **Return Value**:
  - `count` (uint64): The number of PoCBatch objects at the specified block height

#### CountPoCValidationsAtHeight

- **Parameters**:
  - `block_height` (int64): The block height to count PoCValidation objects for
- **Return Value**:
  - `count` (uint64): The number of PoCValidation objects at the specified block height

### CLI Commands

The queries will be exposed via the CLI with the following commands:

```
inferenced query inference count-poc-batches-at-height [block-height]
inferenced query inference count-poc-validations-at-height [block-height]
```

### Usage in E2E Tests

The e2e tests in the testermint package will use these queries to verify that pruning has occurred correctly. The general flow will be:

1. Set up a test chain with a known pruning threshold
2. Create PoC data (PoCBatch and PoCValidation objects) at specific block heights
3. Advance the chain past the pruning threshold
4. Use the new queries to verify that the count of PoCBatch and PoCValidation objects is zero for block heights that should have been pruned
5. Use the new queries to verify that the count of PoCBatch and PoCValidation objects is non-zero for block heights that should not have been pruned

This approach will allow us to verify that the pruning logic is working correctly and that PoC data is being properly removed from the chain state after the pruning threshold has been reached.

## Implementation Approach

The implementation will use the Ignite CLI to scaffold the new queries:

```
ignite scaffold query countPoCBatchesAtHeight blockHeight:int64 --response count:uint64 --module inference
ignite scaffold query countPoCValidationsAtHeight blockHeight:int64 --response count:uint64 --module inference
```

This will generate the necessary boilerplate code for the queries, which will then be implemented to count the PoCBatch and PoCValidation objects at the specified block height.

## Conclusion

By implementing these new queries, we will enable e2e testing of PoC pruning, ensuring that the pruning logic is working correctly and that PoC data is being properly removed from the chain state after the pruning threshold has been reached. This will improve the reliability and maintainability of the Gonka.