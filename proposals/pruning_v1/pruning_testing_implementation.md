# Pruning Testing Implementation

This document summarizes the implementation of the PoC pruning testing functionality as described in the design document (`pruning_testing.md`).

## Implemented Features

### 1. CountPoCBatchesAtHeight Query

A new query has been implemented that returns the count of PoCBatch objects for a specific block height.

- **Query Name**: `CountPoCBatchesAtHeight`
- **Parameters**:
  - `blockHeight` (int64): The block height to count PoCBatch objects for
- **Return Value**:
  - `count` (uint64): The number of PoCBatch objects at the specified block height

### 2. CountPoCValidationsAtHeight Query

A new query has been implemented that returns the count of PoCValidation objects for a specific block height.

- **Query Name**: `CountPoCValidationsAtHeight`
- **Parameters**:
  - `blockHeight` (int64): The block height to count PoCValidation objects for
- **Return Value**:
  - `count` (uint64): The number of PoCValidation objects at the specified block height

## Implementation Details

### Protocol Buffer Definitions

The queries are defined in the `query.proto` file:

```protobuf
// Queries a list of CountPoCbatchesAtHeight items.
rpc CountPoCbatchesAtHeight (QueryCountPoCbatchesAtHeightRequest) returns (QueryCountPoCbatchesAtHeightResponse) {
  option (google.api.http).get = "/productscience/inference/inference/count_po_c_batches_at_height/{blockHeight}";
}

// Queries a list of CountPoCValidationsAtHeight items.
rpc CountPoCValidationsAtHeight (QueryCountPoCValidationsAtHeightRequest) returns (QueryCountPoCValidationsAtHeightResponse) {
  option (google.api.http).get = "/productscience/inference/inference/count_po_c_validations_at_height/{blockHeight}";
}

message QueryCountPoCbatchesAtHeightRequest {
  int64 blockHeight = 1;
}

message QueryCountPoCbatchesAtHeightResponse {
  uint64 count = 1;
}

message QueryCountPoCValidationsAtHeightRequest {
  int64 blockHeight = 1;
}

message QueryCountPoCValidationsAtHeightResponse {
  uint64 count = 1;
}
```

### Query Handlers

The query handlers are implemented in the following files:

1. `query_count_po_c_batches_at_height.go` - Implements the CountPoCBatchesAtHeight query
2. `query_count_po_c_validations_at_height.go` - Implements the CountPoCValidationsAtHeight query

The handlers use the existing `GetPoCBatchesByStage` and `GetPoCValidationByStage` functions to retrieve the PoCBatch and PoCValidation objects at the specified block height, and then count them.

### Unit Tests

Unit tests have been implemented for both queries in the following files:

1. `query_count_po_c_batches_at_height_test.go` - Tests for the CountPoCBatchesAtHeight query
2. `query_count_po_c_validations_at_height_test.go` - Tests for the CountPoCValidationsAtHeight query

The tests verify that the queries correctly count the number of PoCBatch and PoCValidation objects at different block heights.

## Usage

### CLI Commands

The queries can be accessed via the CLI with the following commands:

```
inferenced query inference count-poc-batches-at-height [block-height]
inferenced query inference count-poc-validations-at-height [block-height]
```

### Usage in E2E Tests

The queries can be used in e2e tests to verify that pruning has occurred correctly. The general flow would be:

1. Set up a test chain with a known pruning threshold
2. Create PoC data (PoCBatch and PoCValidation objects) at specific block heights
3. Advance the chain past the pruning threshold
4. Use the new queries to verify that the count of PoCBatch and PoCValidation objects is zero for block heights that should have been pruned
5. Use the new queries to verify that the count of PoCBatch and PoCValidation objects is non-zero for block heights that should not have been pruned

## Conclusion

The implementation of these queries enables e2e testing of PoC pruning, ensuring that the pruning logic is working correctly and that PoC data is being properly removed from the chain state after the pruning threshold has been reached.