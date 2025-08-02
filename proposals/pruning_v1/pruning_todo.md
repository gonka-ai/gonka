# Pruning Implementation Plan

This document outlines a step-by-step approach to implementing the pruning system for the Gonka blockchain as described in the pruning design document. Each step is designed to be complete and includes testing requirements to ensure the implementation is robust and reliable.

**Important Notes:**
- This plan focuses exclusively on unit tests. Inference tests and performance tests are out of scope and will be handled separately.
- This execution plan is designed to be followed by an AI, with review by developers.

## Implementation Steps

### Step 1: Define Parameters

**Tasks:**
1. Add the `inference_pruning_epoch_threshold` parameter to `EpochParams` in `inference-chain/proto/inference/inference/params.proto`
2. Add the `poc_data_pruning_epoch_threshold` parameter to `PocParams` in `inference-chain/proto/inference/inference/params.proto`
3. Run `ignite generate proto-go` in the `inference-chain` directory to generate Go code

**Testing:**
1. Run ALL existing unit tests to ensure the proto changes don't break anything:
   ```
   cd inference-chain
   go test ./...
   ```
2. Write new unit tests for the parameter validation in `inference-chain/x/inference/types/params_test.go`
3. Ensure all tests pass before proceeding

### Step 2: Implement Inference Pruning Logic

**Tasks:**
1. Create a new file `inference-chain/x/inference/keeper/pruning.go`
2. Implement the `PruneInferences` method that:
   - Takes the current epoch and pruning threshold as parameters
   - Scans all inferences
   - Completely removes eligible inferences
   - Logs pruning activities
3. Add helper functions to determine if an inference is eligible for pruning

**Testing:**
1. Write comprehensive unit tests for the pruning logic in `inference-chain/x/inference/keeper/pruning_test.go`
2. Create test cases for different inference statuses and epoch differences
3. Ensure all tests pass before proceeding

### Step 3: Implement PoC Data Pruning Logic

**Tasks:**
1. Add the `PrunePoCData` method to `inference-chain/x/inference/keeper/pruning.go` that:
   - Takes the current epoch and pruning threshold as parameters
   - Scans all `PoCBatch` and `PoCValidation` records
   - Removes eligible records
   - Logs pruning activities
2. Add helper functions to determine if a PoC record is eligible for pruning

**Testing:**
1. Extend the unit tests in `inference-chain/x/inference/keeper/pruning_test.go` to cover PoC data pruning
2. Create test cases for different PoC record ages
3. Ensure all tests pass before proceeding

### Step 4: Integrate Pruning with EndBlock

**Tasks:**
1. Modify `EndBlock` in `inference-chain/x/inference/module/module.go` to call the pruning functions during the PoC phase
2. Add the following code to the `IsStartOfPocStage` condition:

```
// Prune old inferences
pruneErr := am.keeper.PruneInferences(ctx, currentEpoch.Index, am.keeper.GetParams(ctx).EpochParams.InferencePruningEpochThreshold)
if pruneErr != nil {
    am.LogError("Error pruning inferences", types.Inferences, "error", pruneErr)
}

// Prune old PoC data
pocErr := am.keeper.PrunePoCData(ctx, currentEpoch.Index, am.keeper.GetParams(ctx).PocParams.PocDataPruningEpochThreshold)
if pocErr != nil {
    am.LogError("Error pruning PoC data", types.PoC, "error", pocErr)
}
```

**Testing:**
1. Run ALL existing unit tests:
   ```
   cd inference-chain
   go test ./...
   ```
2. Write new unit tests for the pruning integration in `inference-chain/x/inference/module/module_test.go`
3. Create test cases that simulate multiple epochs and verify pruning occurs correctly
4. Ensure all tests pass before proceeding

### Step 5: Update Parameter Handling

**Tasks:**
1. Update the parameter validation in `inference-chain/x/inference/types/params.go` to include the new parameters
2. Add default values for the new parameters:
   - `inference_pruning_epoch_threshold`: 2 (configurable)
   - `poc_data_pruning_epoch_threshold`: 1 (configurable)
3. Update the parameter documentation

**Testing:**
1. Run ALL existing unit tests:
   ```
   cd inference-chain
   go test ./...
   ```
2. Write new unit tests for the parameter validation in `inference-chain/x/inference/types/params_test.go`
3. Ensure all tests pass before proceeding

### Step 6: Unit Test the Pruning System

**Tasks:**
1. Create a new test file `inference-chain/x/inference/keeper/pruning_system_test.go`
2. Implement comprehensive unit tests that:
   - Create multiple inferences across different epochs
   - Trigger the pruning process
   - Verify that eligible inferences are properly pruned
   - Verify that PoC data is properly pruned
   - Verify that statistics queries still work correctly using the existing InferenceStats table

**Testing:**
1. Run ALL existing unit tests:
   ```
   cd inference-chain
   go test ./...
   ```
2. Ensure all tests pass before proceeding

### Step 7: Update CLI and REST Endpoints

**Tasks:**
1. Update the CLI commands in `inference-chain/x/inference/client/cli/` to support the new parameters
2. Update the REST endpoints in `inference-chain/x/inference/client/rest/` if necessary
3. Add commands to query pruning statistics (e.g., number of pruned records)

**Testing:**
1. Run ALL existing unit tests:
   ```
   cd inference-chain
   go test ./...
   ```
2. Write new unit tests for the updated CLI commands in `inference-chain/x/inference/client/cli/query_test.go` and `inference-chain/x/inference/client/cli/tx_test.go`
3. Ensure all tests pass before proceeding

### Step 8: Update Documentation

**Tasks:**
1. Update the module documentation to include information about the pruning system
2. Add examples of how to configure the pruning parameters
3. Document the behavior of queries when accessing pruned data

**Testing:**
1. Review the documentation for accuracy and completeness
2. Ensure all examples are correct and up-to-date

### Step 9: Final Unit Testing

**Tasks:**
1. Review all unit tests to ensure comprehensive coverage
2. Add any missing test cases
3. Verify that all edge cases are properly tested

**Testing:**
1. Run ALL unit tests to ensure everything works correctly:
   ```
   cd inference-chain
   go test ./...
   ```
2. Ensure all tests pass before proceeding

## Testing Strategy

For each step in the implementation plan, the following testing approach should be followed:

1. **Run Existing Tests**: Before making any changes, run the existing tests to establish a baseline.
2. **Write New Tests**: For each new feature or modification, write comprehensive unit tests.
3. **Test Edge Cases**: Ensure tests cover edge cases such as:
   - Empty inferences
   - Inferences with missing fields
   - Boundary conditions for epoch thresholds
   - Concurrent pruning operations
4. **Integration with Existing Code**: Test how the pruning system interacts with other components of the blockchain through unit tests.

## Rollback Plan

If issues are discovered during implementation or testing, the following rollback plan should be followed:

1. Identify the specific component causing the issue
2. Revert the changes to that component
3. Run tests to verify the system is back to a stable state
4. Redesign the problematic component
5. Implement and test the redesigned component

## Conclusion

This implementation plan provides a step-by-step approach to implementing the pruning system for the Gonka blockchain. By following this plan and ensuring all unit tests pass at each step, we can ensure a robust and reliable implementation that optimizes storage usage while maintaining all necessary functionality.

Remember that this plan focuses exclusively on unit tests. Inference tests and performance tests will be handled separately by the development team.