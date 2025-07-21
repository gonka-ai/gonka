# InferenceStatsStorage Implementation

## Overview

The InferenceStatsStorage implementation optimizes the storage of inference data in the Inference-Ignite blockchain by creating a new data structure that retains only the fields necessary for the statistics system, while allowing the deletion of payload fields during the PoC phase. This approach significantly reduces the blockchain's storage requirements while maintaining all functionality needed for analytics and reporting.

## Key Components

### 1. InferenceStatsStorage Structure

The `InferenceStatsStorage` message contains only the fields used by the statistics system:

```protobuf
message InferenceStatsStorage {
  string index = 1;
  string inference_id = 2;
  InferenceStatus status = 3;
  int64 start_block_timestamp = 4;
  int64 end_block_timestamp = 5;
  uint64 epoch_id = 6;
  uint64 epoch_poc_start_block_height = 7;
  uint64 prompt_token_count = 8;
  uint64 completion_token_count = 9;
  int64 actual_cost = 10;
  string requested_by = 11;
  string executed_by = 12;
  string transferred_by = 13;
  string model = 14;
}
```

### 2. Transparent Storage and Retrieval

The implementation makes the existence of InferenceStatsStorage transparent to most of the codebase by modifying the keeper methods:

#### Key Functions

- `InferenceStatsStorageKeyPrefix`: The prefix used to retrieve all InferenceStatsStorage objects
- `InferenceStatsStorageKey`: Returns the store key to retrieve an InferenceStatsStorage from the index fields

#### Modified Keeper Methods

- `SetInference`: Sets a specific inference in the store and creates/updates its stats
- `SetInferenceWithoutDevStatComputation`: Sets a specific inference in the store without computing developer stats, and creates/updates its stats
- `GetInference`: Returns an inference from its index, falling back to stats if full inference is not found
- `RemoveInference`: Removes an inference from the store with an option to convert to stats-only before removal
- `GetInferenceDirectFromStore`: Returns an inference directly from the store without falling back to stats (used for testing)
- `GetInferenceStatsStorage`: Returns an inference stats storage from its index

### 3. Helper Functions for Conversion

- `CreateInferenceStatsStorage`: Extracts the relevant fields from an Inference to create InferenceStatsStorage
- `InferenceFromStatsStorage`: Creates an Inference from InferenceStatsStorage with empty payload fields

### 4. Automatic Cleanup Process During PoC Phase

The `cleanupInferencesForPoc` function runs during the PoC phase to convert full inferences to stats-only when they are in FINISHED, VALIDATED, or INVALIDATED state. This function is called from the `EndBlock` function when `epochContext.IsStartOfPocStage(blockHeight)` is true.

## Usage

### Converting Inferences to Stats-Only

To convert an inference to stats-only:

```go
keeper.RemoveInference(ctx, inferenceIndex, true)
```

### Retrieving Inferences

The `GetInference` method will automatically fall back to stats if the full inference is not found:

```go
inference, found := keeper.GetInference(ctx, inferenceIndex)
```

To disable the fallback (used in tests):

```go
inference, found := keeper.GetInference(ctx, inferenceIndex, true)
```

### Retrieving Stats Directly

To retrieve the stats storage directly:

```go
stats, found := keeper.GetInferenceStatsStorage(ctx, inferenceIndex)
```

## Benefits

1. **Reduced Storage Requirements**: By removing payload data during the PoC phase, we significantly reduce the blockchain's storage footprint.
2. **Maintained Functionality**: All statistics and analytics features continue to work as before.
3. **Transparent Implementation**: Most of the codebase doesn't need to be aware of the change, as the keeper methods handle the complexity.
4. **Improved Performance**: Smaller data structures lead to faster queries and better overall performance.

## Testing

The implementation includes comprehensive tests:

- `TestInferenceStatsStorage`: Tests the creation, retrieval, and conversion of InferenceStatsStorage
- Existing tests have been updated to work with the new functionality

## Automatic Cleanup

During the PoC phase, the system automatically converts full inferences to stats-only when they are in FINISHED, VALIDATED, or INVALIDATED state. This happens at the start of each PoC stage, as determined by `epochContext.IsStartOfPocStage(blockHeight)`.