# Pruning Design for Inference-Ignite Blockchain

## Overview

This document outlines the design for a comprehensive pruning system in the Inference-Ignite blockchain. The pruning system aims to optimize storage usage by removing unnecessary data while preserving essential information for statistics, analytics, and system integrity.

The design addresses two main types of data that consume significant storage space:

1. **Inference Records**: Large records containing prompt and response payloads that are only needed during execution and validation
2. **PoC Data**: Proof of Computation data (PoCBatch and PoCValidation) that is only needed during the validation process

## Inference Pruning Design

### Problem Statement

Inference records contain large payload fields (prompt and response payloads) that consume significant storage space but are only needed during execution and validation. After a certain number of epochs, these payload fields can be safely removed while preserving the statistical information needed for analytics and reporting.

### Solution Design

#### InferenceStats Structure

We will create a new `InferenceStats` structure in the chain that contains only the fields used by the statistics system:

```
message InferenceStats {
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

#### Storage and Retrieval

The implementation will make the existence of `InferenceStats` transparent to most of the codebase by modifying the keeper methods:

1. **Key Prefix Definition**: Define separate key prefixes for `Inference` and `InferenceStats` records
2. **SetInference**: Modified to store both the full inference and its stats version
3. **GetInference**: Modified to fall back to stats if full inference is not found
4. **RemoveInference**: Enhanced with an option to convert to stats-only before removal
5. **GetAllInference**: Remains unchanged, returning only full inferences

#### Pruning Process

The pruning process will run during the PoC phase of each epoch:

1. Scan all inferences
2. For each inference, check if:
   - The inference's `EpochId` is at least `inference_pruning_epoch_threshold` epochs older than the current epoch
   - The inference has a status of FINISHED, VALIDATED, INVALIDATED, or EXPIRED
3. If these conditions are met:
   - Create an `InferenceStats` record
   - Remove the full inference record

#### Configuration Parameter

A new parameter `inference_pruning_epoch_threshold` will be added to `EpochParams` to determine how many epochs must pass before an inference is eligible for pruning:

```
message EpochParams {
  // Existing fields...
  uint64 inference_pruning_epoch_threshold = 9; // Number of epochs after which inferences can be pruned
}
```

## PoC Data Pruning Design

### Problem Statement

PoC data (PoCBatch and PoCValidation) is only needed during the validation process and can be safely removed after a certain number of epochs.

### Solution Design

#### Pruning Process

The PoC data pruning process will also run during the PoC phase of each epoch:

1. Scan all `PoCBatch` and `PoCValidation` records
2. For each record, check if:
   - The record's `poc_stage_start_block_height` corresponds to an epoch that is at least `poc_data_pruning_epoch_threshold` epochs older than the current epoch
3. If this condition is met, completely remove the record

#### Configuration Parameter

A new parameter `poc_data_pruning_epoch_threshold` will be added to `PocParams` to determine how many epochs must pass before PoC data is eligible for pruning:

```
message PocParams {
  // Existing fields...
  uint64 poc_data_pruning_epoch_threshold = 3; // Number of epochs after which PoC data can be pruned (default: 1)
}
```

## Implementation Details

### Integration with EndBlock

The pruning system will be integrated with the existing `EndBlock` function in the module implementation:

1. During the PoC phase (detected by `IsStartOfPocStage`), call the pruning functions
2. Implement separate functions for inference pruning and PoC data pruning
3. Use the configuration parameters to determine which records are eligible for pruning

### Keeper Modifications

The following keeper methods will need to be modified or added:

1. **SetInference**: Modified to store both the full inference and its stats version
2. **GetInference**: Modified to fall back to stats if full inference is not found
3. **RemoveInference**: Enhanced with an option to convert to stats-only before removal
4. **GetInferenceStats**: New method to retrieve stats directly
5. **PruneInferences**: New method to handle the inference pruning process
6. **PrunePoCData**: New method to handle the PoC data pruning process

### Key Prefixes and Storage

New key prefixes will be defined for the `InferenceStats` records:

```
const (
    InferenceKeyPrefix = "Inference/value/"
    InferenceStatsKeyPrefix = "InferenceStats/value/"
)
```

### Helper Functions

Helper functions will be implemented for:

1. Converting between `Inference` and `InferenceStats`
2. Determining if a record is eligible for pruning based on epoch thresholds
3. Calculating epoch differences

## Governance Considerations

The pruning parameters (`inference_pruning_epoch_threshold` and `poc_data_pruning_epoch_threshold`) will be configurable through the blockchain's governance system. This allows the community to adjust retention policies based on network needs and storage considerations.

When proposing changes to these parameters, validators and stakeholders should consider:

1. **Data Retention Needs**: How long data needs to be retained for analytical, debugging, or auditing purposes
2. **Storage Constraints**: The current and projected storage usage of the blockchain
3. **Performance Impact**: How retention policies affect query performance and overall system responsiveness

## Benefits

1. **Reduced Storage Requirements**: By pruning unnecessary data, the blockchain's storage footprint will be significantly reduced
2. **Maintained Functionality**: All statistics and analytics features will continue to work as before, as essential data is preserved in `InferenceStats`
3. **Configurable Retention**: The configurable epoch thresholds allow the network to adjust retention policies through governance voting
4. **Improved Performance**: Smaller data structures lead to faster queries and better overall performance
5. **Sustainable Growth**: The pruning system ensures the blockchain can grow sustainably over time without excessive storage requirements

## Security and Data Integrity

The pruning system is designed to maintain data integrity while optimizing storage:

1. **Statistical Integrity**: All data needed for statistics and analytics is preserved in `InferenceStats`
2. **Transparent Access**: The existing API methods continue to work transparently, with fallbacks to stats-only data when full records have been pruned
3. **Epoch-Based Pruning**: By using epoch-based thresholds rather than time-based ones, the system ensures that pruning decisions are consistent across all nodes in the network

## Future Considerations

As the network evolves, additional pruning strategies may be considered:

1. **Selective Pruning**: Retaining certain inferences based on importance or usage patterns
2. **Archival Nodes**: Supporting specialized nodes that maintain complete historical data
3. **Compression Techniques**: Implementing compression for retained data to further reduce storage requirements