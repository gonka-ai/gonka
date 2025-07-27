# Pruning Testing Implementation Todo

This document outlines the step-by-step process for implementing the PoC pruning testing functionality as described in the design document (`pruning_testing.md`). Each step is designed to be atomic and executable by an AI agent.

## Implementation Steps

### 1. Scaffold the CountPoCBatchesAtHeight Query

1. Navigate to the inference-chain directory:
   ```
   cd inference-chain
   ```

2. Use Ignite CLI to scaffold the CountPoCBatchesAtHeight query:
   ```
   ignite scaffold query countPoCBatchesAtHeight blockHeight:int64 --response count:uint64 --module inference
   ```
3. THIS WILL FAIL. But that's ok. Follow it up with `ignite generate proto-go` and everything will be functional.
3. Verify that the necessary files have been created or modified in the types and keeper directories.

4. Run unit tests to ensure the basic scaffolding is working correctly.

### 2. Implement the CountPoCBatchesAtHeight Query

1. Locate the newly created query file in the keeper directory.

2. Implement the query function to count PoCBatch objects at the specified block height:
   - For perf reasons, create a new keeper method to only count entries
   - Return the count in the response

3. Run unit tests to verify the implementation works correctly.

### 3. Scaffold the CountPoCValidationsAtHeight Query

1. Use Ignite CLI to scaffold the CountPoCValidationsAtHeight query:
   ```
   ignite scaffold query countPoCValidationsAtHeight blockHeight:int64 --response count:uint64 --module inference
   ```
3. THIS WILL FAIL. But that's ok. Follow it up with `ignite generate proto-go` and everything will be functional.

2. Verify that the necessary files have been created or modified in the types and keeper directories.

3. Run unit tests to ensure the basic scaffolding is working correctly.

### 4. Implement the CountPoCValidationsAtHeight Query

1. Locate the newly created query file in the keeper directory.

2. Implement the query function to count PoCValidation objects at the specified block height:
   - For perf reasons, create a new keeper method to only count entries
   - Return the count in the response

3. Run unit tests to verify the implementation works correctly.

### 5. Write Unit Tests

1. Create unit tests for the CountPoCBatchesAtHeight query:
   - Test with no PoCBatch objects
   - Test with multiple PoCBatch objects
   - Test with invalid block height
   - Test error handling

2. Create unit tests for the CountPoCValidationsAtHeight query:
   - Test with no PoCValidation objects
   - Test with multiple PoCValidation objects
   - Test with invalid block height
   - Test error handling

3. Run the unit tests to verify they pass.

## Conclusion

Following these steps will implement the PoC pruning testing functionality as described in the design document. The implementation will provide the necessary queries to enable e2e testing of PoC pruning. Note that the actual e2e tests in the testermint package will be implemented separately.

Remember to run unit tests after each implementation step to catch issues early and ensure the code remains stable throughout the development process.