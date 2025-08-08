INTRODUCTION
This document is our worksheet for MLNode proposal implementation. That part of documentation contains only task, their statuses and details.

NEVER delete this introduction

All tasks should be in format:
[STATUS]: Task
    Description

STATUS can be:
- [TODO]
- [WIP]
- [DONE]

You can work only at the task marked [WIP]. You need to solve this task in clear, simple and robust way and propose all solution minimalistic, simple, clear and concise. Write minimal code!

All tasks implementation should not break tests.

## Quick Start Examples

### 1. Build Project
```bash
make build-docker    # Build all Docker containers
make local-build     # Build binaries locally  
./local-test-net/stop.sh # Clean old containers
```

### 2. Run Tests
```bash
cd testermint && ./gradlew :test --tests "TestClass" -DexcludeTags=unstable,exclude  # Specific class, stable only
cd testermint && ./gradlew :test --tests "TestClass.test method name"    # Specific test method
```

NEVER RUN MANY TESTERMINT TESTS AT ONCE

Current implementation plan is in `proposals/keys/flow.md`
High-level overview is in `proposals/keys/README.md`

------

## Implementation Tasks: Simplified Bandwidth Limiter

This implementation follows **Option 1 (Predictive Estimation)** with bandwidth released upon request completion. Tasks should be completed sequentially in the order listed.

**Key Requirements:**
- Use bandwidth estimation: `Total_KB = Input_tokens × kb_per_input_token + Output_tokens × kb_per_output_token` (coefficients from chain parameters)
- Proactive control: Check limits before accepting requests  
- Efficiency: Release bandwidth immediately when request completes
- Thread-safe concurrent access
- Minimal, clean implementation

---

### Protocol Buffer and Parameters

[TODO]: Add BandwidthLimitsParams message to protobuf with all bandwidth parameters
    Modify `inference-chain/proto/inference/inference/params.proto` to add new BandwidthLimitsParams message with: `uint64 estimated_limits_per_block_kb = 1`, `string kb_per_input_token = 2`, `string kb_per_output_token = 3` (using string for decimal precision)

[TODO]: Regenerate Go files from protobuf definitions  
    Run `ignite generate proto-go` in inference-chain directory to update Go type definitions

[TODO]: Update parameter store logic for new BandwidthLimitsParams
    Modify `inference-chain/x/inference/keeper/params.go` to handle the new BandwidthLimitsParams with getter/setter methods for all bandwidth-related parameters

### BandwidthLimiter Core Implementation  

[DONE]: Create BandwidthLimiter struct and basic methods
    ✅ Created `decentralized-api/internal/bandwidth_limiter.go` with struct containing map[int64]float64 for bandwidth tracking per block height, mutex for thread safety, and limit configuration

[DONE]: Implement CanAcceptRequest method
    ✅ Added method that calculates estimated KB using configurable coefficients and checks average bandwidth usage across request lifespan period (corrected logic: checks sum([current:current+requestLifespanBlocks]) / requestLifespanBlocks)

[DONE]: Implement RecordRequest method  
    ✅ Added method to reserve bandwidth by adding estimated KB to completion block (startBlock + requestLifespanBlocks) - corrected from original single-block approach

[DONE]: Implement ReleaseRequest method
    ✅ Added method to free reserved bandwidth by subtracting estimated KB from the completion block where it was originally recorded

[DONE]: Add cleanup goroutine for old block entries
    ✅ Implemented background goroutine that periodically removes entries for old blocks to prevent memory leaks, with 2x lifespan buffer

[DONE]: Add constructor and configuration methods
    ✅ Created NewBandwidthLimiter constructor that accepts bandwidth limit and coefficient parameters (kb_per_input_token, kb_per_output_token) and starts cleanup goroutine

### API Server Integration

[DONE]: Instantiate BandwidthLimiter in server setup
    Modify main server initialization to create BandwidthLimiter instance with all bandwidth parameters (limit and coefficients) fetched from chain's BandwidthLimitsParams

[DONE]: Implement weight-based bandwidth allocation per Transfer Agent  
    ✅ Added calculateWeightBasedBandwidthLimit function that fetches all participants, finds current node's weight, calculates total weight, and applies the formula: taEstimatedLimitsPerBlockKb = EstimatedLimitsPerBlockKb * (nodeWeight / totalWeight). Enhanced with epoch-based automatic weight refresh, robust error handling for negative weights/totalWeight, and fallback to default limits when participant data is unavailable. Function now updates every epoch to handle participant weight changes properly.
    ✅ PERFORMANCE FIX: Added EpochGroupDataCache to prevent expensive RPC calls on every request. Cache only fetches new EpochGroupData when epoch changes (from blockchain RPC every ~few hours instead of every request). BandwidthLimiter now uses cached data via ChainPhaseTracker integration, dramatically reducing load from potentially 1000s of queries/minute to 1 query/epoch. Minimalistic implementation with thread-safe double-checked locking pattern.
    ✅ COMPUTATION OPTIMIZATION: Added cached weight-based limit calculation. Now skips weight computation loop entirely if same epoch, avoiding redundant math on every request. Only recalculates when epoch changes, providing near-zero overhead for repeated requests within same epoch.

[DONE]: Add bandwidth checking to request handler before proxying
    Modify `post_chat_handler.go` to get current block height, check CanAcceptRequest, reject with 429 error if over limit, otherwise RecordRequest

[DONE]: Add bandwidth release after request completion  
    Modify request handler to call ReleaseRequest using defer statement to ensure bandwidth is freed even if request fails mid-stream

### Testing Implementation

[DONE]: Create unit tests for BandwidthLimiter
    ✅ Created `decentralized-api/internal/bandwidth_limiter_test.go` with comprehensive tests for: under-limit acceptance, over-limit rejection, correct record/release behavior with completion-block logic, thread safety under concurrent load, and cleanup functionality. All tests passing.

[DONE]: Create integration test in testermint
    ✅ Created comprehensive BandwidthLimiterTests.kt with proper error handling for bandwidth limiter rejections. Test now correctly catches and counts both successful requests and bandwidth rejections (error message "Transfer Agent capacity reached") by using manual thread-based parallel requests with synchronized counting. Verifies bandwidth limiting functionality through proper error response handling and tests bandwidth release after waiting for completion blocks.

---

## Implementation Notes

**✅ CORRECTED LOGIC**: The initial implementation plan had a flaw in bandwidth accounting. Original approach tracked bandwidth at start block, but this created artificial bottlenecks. 

**IMPROVED APPROACH IMPLEMENTED**: 
- Check average bandwidth across request lifespan: `sum([current:current+requestLifespanBlocks]) / requestLifespanBlocks`
- Record/release bandwidth at completion block: `startBlock + requestLifespanBlocks`

This provides:
- More accurate resource modeling (bandwidth attributed when actually consumed)
- Better capacity utilization (staggered requests don't artificially compete)
- Realistic accounting reflecting actual network load distribution
