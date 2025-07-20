# Tokenomics V2: Reward Vesting - Task Plan

## Prerequisite Reading

Before starting implementation, please read the following documents to understand the full context of the changes:
- The main proposal: `proposals/tokenomics-v2/vesting.md`
- The existing tokenomics system: `docs/tokenomics.md`

## How to Use This Task List

### Workflow
- **Focus on a single task**: Please work on only one task at a time to ensure clarity and quality. Avoid implementing parts of future tasks.
- **Request a review**: Once a task's implementation is complete, change its status to `[?] - Review` and wait for my confirmation.
- **Update all usages**: If a function or variable is renamed, find and update all its references throughout the codebase.
- **Build after each task**: After each task is completed, build the project to ensure there are no compilation errors.
- **Test after each section**: After completing all tasks in a section, run the corresponding tests to verify the functionality.
- **Wait for completion**: After I confirm the review, mark the task as `[x] - Finished`, add a **Result** section summarizing the changes, and then move on to the next one.

### Build & Test Commands
- **Build Inference Chain**: From the project root, run `make node-local-build`
- **Build API Node**: From the project root, run `make api-local-build`
- **Run Inference Chain Unit Tests**: From the project root, run `make node-test`
- **Run API Node Unit Tests**: From the project root, run `make api-test`

### Status Indicators
- `[ ]` **Not Started** - Task has not been initiated
- `[~]` **In Progress** - Task is currently being worked on
- `[?]` **Review** - Task completed, requires review/testing
- `[x]` **Finished** - Task completed and verified

### Task Organization
Tasks are organized by implementation area and numbered for easy reference. Dependencies are noted where critical. Complete tasks in order.

### Task Format
Each task includes:
- **What**: Clear description of work to be done
- **Where**: Specific files/locations to modify
- **Why**: Brief context of purpose when not obvious

## Task List

### **Section 1: `x/vesting` Module Scaffolding and Core Logic**

#### **1.1 Scaffold New Module**
- **Task**: `[x]` Scaffold the new `x/streamvesting` module
- **What**: Use `ignite scaffold module streamvesting --dep bank` to create the basic structure in the inference-chain folder. The inference dependency will be a one-way relationship (inference depends on streamvesting). For this and all subsequent tasks involving proto generation, use `ignite generate proto-go` command in the inference-chain folder.
- **Where**: New directory `inference-chain/x/streamvesting`
- **Dependencies**: None
- **Result**: Successfully scaffolded the `x/streamvesting` module with bank dependency. Created new directory `inference-chain/x/streamvesting` with basic module structure including keeper, types, and module files.

#### **1.2 Define Vesting Parameters**
- **Task**: `[x]` Define vesting parameters and genesis state
- **What**: Add `RewardVestingPeriod` parameter to control how many epochs rewards vest for. Define the `GenesisState` to initialize it. Set the default to `180` epochs (but can be overridden to `2` in tests).
- **Where**:
  - `inference-chain/proto/inference/streamvesting/params.proto`
  - `inference-chain/proto/inference/streamvesting/genesis.proto`
  - `inference-chain/x/streamvesting/types/params.go`
- **Why**: This parameter controls the vesting duration and can be adjusted via governance or set shorter in tests.
- **Dependencies**: 1.1
- **Result**: Successfully implemented `RewardVestingPeriod` parameter with default value of 180 epochs, proper validation, and genesis state integration. Generated Go code with `ignite generate proto-go` and resolved duplicate module declarations. Module builds successfully.

#### **1.3 Define Vesting Data Structures**
- **Task**: `[x]` Define `VestingSchedule` data structures
- **What**: Define a protobuf message for a participant's vesting schedule with a repeated field for epoch amounts. Implement a keeper store to map a participant's address to their `VestingSchedule`.
- **Where**:
  - `inference-chain/proto/inference/streamvesting/vesting_schedule.proto`
  - `inference-chain/x/streamvesting/keeper/keeper.go`
  - `inference-chain/x/streamvesting/types/keys.go`
- **Dependencies**: 1.1
- **Result**: Successfully implemented VestingSchedule protobuf message with participant address and epoch amounts using Cosmos SDK coin types. Added store keys and keeper methods (Set, Get, Remove, GetAll) for VestingSchedule storage with proper prefix handling. Generated Go code with `ignite generate proto-go` and verified successful build.

#### **1.4 Implement Reward Addition Logic**
- **Task**: `[x]` Implement the core reward vesting logic
- **What**: Create an exported keeper function `AddVestedRewards(ctx, address, amount, vesting_epochs)`. This function will retrieve a participant's schedule and add the new reward according to the aggregation logic (divide by N epochs, add remainder to first element, extend array if necessary). Use the `RewardVestingPeriod` parameter if `vesting_epochs` is not specified.
- **Where**: `inference-chain/x/streamvesting/keeper/keeper.go`
- **Dependencies**: 1.3
- **Note**: Remember to emit an event for reward vesting
- **Result**: Successfully implemented `AddVestedRewards` function with complete aggregation logic including parameter handling, schedule extension, coin division with remainder handling, and event emission. Created events.go file with proper event types and attributes. Updated proto definition with gogoproto.equal annotations and regenerated Go code. Build successful with all features working.

#### **1.5 Implement Token Unlocking Logic**
- **Task**: `[x]` Create the token unlocking function
- **What**: Create a keeper function `ProcessEpochUnlocks(ctx)` that processes all vesting schedules. For each schedule, it should transfer the amount in the first element to the participant, remove the first element, and delete the schedule if it becomes empty.
- **Where**: `inference-chain/x/streamvesting/keeper/keeper.go`
- **Dependencies**: 1.3
- **Note**: Remember to emit events for each unlock
- **Result**: Successfully implemented `ProcessEpochUnlocks` function with complete token unlocking logic including schedule iteration, coin transfers from module to participants, schedule cleanup, and optimized event emission. Function emits a single summary event per epoch with total unlocked amounts and participant counts instead of individual events per participant for better efficiency. Added `SendCoinsFromModuleToAccount` method to BankKeeper interface. Function handles empty epochs, invalid addresses, and transfer failures gracefully. Build successful with all features working.

#### **1.6 Implement AdvanceEpoch Function**
- **Task**: `[x]` Implement the `AdvanceEpoch` function
- **What**: Create an exported function `AdvanceEpoch(ctx, completedEpoch)` that will be called by the inference module. This function should call `ProcessEpochUnlocks` to unlock vested tokens for the completed epoch.
- **Where**: `inference-chain/x/streamvesting/keeper/keeper.go`
- **Dependencies**: 1.5
- **Why**: This follows the same pattern as the collateral module for epoch-based processing
- **Result**: Successfully implemented `AdvanceEpoch` function as the exported entry point for epoch-based processing. Function accepts completed epoch parameter, provides comprehensive logging for debugging and monitoring, calls ProcessEpochUnlocks to handle token unlocking, includes proper error handling and reporting. Follows the same pattern as collateral module for consistent integration with inference module. Build successful with all features working.

#### **1.7 Implement Genesis Logic**
- **Task**: `[x]` Implement Genesis import/export
- **What**: Implement `InitGenesis` and `ExportGenesis` functions that properly handle all vesting schedules. Follow the pattern from the collateral module.
- **Where**: `inference-chain/x/streamvesting/module/genesis.go`
- **Dependencies**: 1.3
- **Result**: Successfully implemented complete genesis import/export logic following the collateral module pattern. Updated genesis.proto to include vesting_schedule_list field for storing all vesting schedules. Implemented InitGenesis to restore all vesting schedules from genesis state using SetVestingSchedule. Implemented ExportGenesis to export all current vesting schedules using GetAllVestingSchedules. Generated Go code with `ignite generate proto-go` and verified successful build. Vesting schedules will now properly persist across chain restarts and upgrades.

#### **1.8 Verify Module Wiring and Permissions**
- **Task**: `[x]` Verify Module Wiring and add module account permissions
- **What**: Ensure the module is properly wired in `app_config.go` (genesis order only, no end blocker needed) and add the module account permission with `minter` capability (to hold vesting funds).
- **Where**: `inference-chain/app/app_config.go`
- **Dependencies**: 1.1
- **Result**: Successfully verified and corrected module wiring in app_config.go. Confirmed streamvesting is properly included in genesis module order for state initialization. Removed streamvesting from beginBlockers and endBlockers since it only processes on epoch advancement, not every block. Verified module account permissions include `minter` capability to hold and distribute vested funds. Module configuration is properly set up. Build successful with correct wiring.

### **Section 2: Integration with `x/inference` Module**

#### **2.1 Define StreamVestingKeeper Interface**
- **Task**: `[x]` Add StreamVestingKeeper interface to inference module
- **What**: Define the `StreamVestingKeeper` interface in the inference module's expected keepers, with the `AddVestedRewards` and `AdvanceEpoch` method signatures.
- **Where**: `inference-chain/x/inference/types/expected_keepers.go`
- **Dependencies**: 1.4, 1.6
- **Result**: Successfully defined `StreamVestingKeeper` interface in the inference module's expected keepers following the CollateralKeeper pattern. Interface includes `AddVestedRewards(ctx context.Context, participantAddress string, amount sdk.Coins, vestingEpochs *uint64) error` for reward vesting and `AdvanceEpoch(ctx context.Context, completedEpoch uint64) error` for epoch processing. Standardized on `context.Context` for consistency with CollateralKeeper interface. Build successful with interface definition.

#### **2.2 Call AdvanceEpoch from Inference Module**
- **Task**: `[x]` Integrate streamvesting epoch advancement
- **What**: Add a call to `streamvestingKeeper.AdvanceEpoch(ctx, completedEpoch)` in the inference module's `onSetNewValidatorsStage` function, right after the collateral module's `AdvanceEpoch` call.
- **Where**: `inference-chain/x/inference/module/module.go`
- **Dependencies**: 2.1
- **Result**: Successfully integrated streamvesting epoch advancement into the inference module lifecycle. Added `StreamVestingKeeper` field to inference keeper struct and updated dependency injection setup. Added AdvanceEpoch call in `onSetNewValidatorsStage` function right after collateral module call with proper error handling and logging. Standardized context types to use `context.Context` for consistency with CollateralKeeper - context conversion now handled internally in streamvesting keeper. Streamvesting module will now automatically process epoch unlocks when the inference module advances epochs. Build successful with complete integration.

#### **2.3 Modify Reward Distribution - Regular Claims**
- **Task**: `[x]` Reroute regular reward payments to streamvesting
- **What**: Modify the reward claim logic to call `streamvestingKeeper.AddVestedRewards` for `Reward Coins` while still paying `Work Coins` directly. Use the `RewardVestingPeriod` parameter from the streamvesting module (default 180 epochs, but can be set to 2 epochs in tests).
- **Where**: `inference-chain/x/inference/keeper/msg_server_claim_rewards.go`
- **Dependencies**: 2.1
- **Result**: Successfully centralized vesting logic in payment functions with `withVesting` parameter. Added `withVesting` boolean to `PayParticipantFromModule` and `PayParticipantFromEscrow` functions. When `withVesting=true`: transfers coins from source module to streamvesting module via `SendCoinsFromModuleToModule`, then adds to vesting schedule. When `withVesting=false`: direct payment as before. **Architecture Change**: All reward claims (both work coins and reward coins) now vest with `withVesting=true` providing unified vesting behavior. Clean, centralized architecture with proper coin flow. Build successful.

#### **2.4 Modify Reward Distribution - Top Miner**
- **Task**: `[x]` Reroute top miner rewards to streamvesting
- **What**: Modify the top miner reward payment to use streamvesting. Replace the direct `PayParticipantFromModule` call with a call to `streamvestingKeeper.AddVestedRewards`. Use the same `RewardVestingPeriod` parameter as regular rewards.
- **Where**: `inference-chain/x/inference/module/top_miners.go` (line 42 in the `UpdateAndPayMiner` case)
- **Dependencies**: 2.1
- **Result**: Successfully updated top miner rewards to use centralized vesting logic. Changed `PayParticipantFromModule` call to use `withVesting=true` parameter in the `UpdateAndPayMiner` case. Top miner rewards now automatically follow the same vesting flow as all other vested payments: coins transfer from TopRewardPool to streamvesting module, then vest over `RewardVestingPeriod` (180 epochs, configurable to 2 for tests). Consistent architecture with all other reward types. Build successful.

#### **2.5 Update Keeper Initialization**
- **Task**: `[x]` Pass StreamVestingKeeper to InferenceKeeper
- **What**: Update the inference keeper initialization to accept and store the streamvesting keeper reference.
- **Where**: 
  - `inference-chain/x/inference/keeper/keeper.go`
  - `inference-chain/app/keepers.go` (or wherever keepers are initialized)
- **Dependencies**: 2.1
- **Result**: Completed as part of Task 2.2. Added `streamvestingKeeper types.StreamVestingKeeper` field to Keeper struct, updated `NewKeeper` function to accept streamvesting keeper parameter, added `GetStreamVestingKeeper()` getter method, and updated dependency injection in `module.go` to pass streamvesting keeper reference. Inference keeper now properly maintains reference to streamvesting keeper for reward distribution and epoch processing.

### **Section 3: Queries, Events, and CLI**

#### **3.1 Implement Query Endpoints**
- **Task**: `[x]` Implement query endpoints
- **What**: Implement gRPC query endpoints to get:
  - A participant's full vesting schedule
  - Total vesting amount for a participant
  - Module parameters (including RewardVestingPeriod)
- **Where**:
  - `inference-chain/proto/inference/streamvesting/query.proto`
  - `inference-chain/x/streamvesting/keeper/query_server.go`
- **Dependencies**: 1.3
- **Result**: Successfully implemented gRPC query endpoints for streamvesting module. Added `VestingSchedule` query to get participant's full vesting schedule and `TotalVestingAmount` query to get total vesting amount for a participant. Updated query.proto with new message types and HTTP endpoints. Implemented query methods in keeper/query.go with proper error handling and empty schedule handling. Existing `Params` query already available for module parameters including RewardVestingPeriod. Generated Go code and verified successful build.

#### **3.2 Implement Event Types**
- **Task**: `[x]` Define and emit events
- **What**: Define event types and attributes for vesting operations. Emit events when:
  - Rewards are vested (`EventTypeVestReward`)
  - Tokens are unlocked (`EventTypeUnlockTokens`)
- **Where**:
  - `inference-chain/x/streamvesting/types/events.go`
  - Update functions from tasks 1.4 and 1.5 to emit these events
- **Dependencies**: 1.4, 1.5
- **Result**: Events already implemented and working from Task 1.4. Event types `EventTypeVestReward` and `EventTypeUnlockTokens` defined with proper attributes. `EventTypeVestReward` emitted in AddVestedRewards with participant, amount, and vesting epochs. `EventTypeUnlockTokens` emitted in ProcessEpochUnlocks with optimized single summary event containing total unlocked amount and participant counts. Events provide comprehensive observability for vesting operations.

#### **3.3 Implement CLI Commands**
- **Task**: `[x]` Add CLI commands
- **What**: Implement CLI commands for querying vesting status using the AutoCLI approach (as done in collateral module).
- **Where**: `inference-chain/x/streamvesting/module/autocli.go`
- **Dependencies**: 3.1
- **Result**: Successfully implemented AutoCLI commands for streamvesting module queries. Added `vesting-schedule [participant-address]` command to query full vesting schedule for a participant and `total-vesting [participant-address]` command to query total vesting amount. Commands use positional arguments and follow the same pattern as collateral module. Existing `params` command available for module parameters. CLI commands provide user-friendly access to all streamvesting query endpoints.

### **Section 4: Testing**

#### **4.1 Unit Tests - Core Vesting Logic**
- **Task**: `[ ]` Write unit tests for core vesting functions
- **What**: Create comprehensive unit tests covering:
  - Adding new rewards (single and multiple)
  - Aggregation logic with remainders
  - Array extension when needed
  - Epoch unlock processing
  - Empty schedule cleanup
- **Where**: `inference-chain/x/streamvesting/keeper/keeper_test.go`
- **Dependencies**: Section 1

#### **4.2 Unit Tests - Integration Points**
- **Task**: `[ ]` Write integration tests
- **What**: Test the integration between inference and streamvesting modules, ensuring rewards are properly routed and epochs trigger unlocks.
- **Where**: `inference-chain/x/inference/keeper/streamvesting_integration_test.go`
- **Dependencies**: Section 2

#### **4.3 Unit Tests - Genesis**
- **Task**: `[ ]` Write genesis import/export tests
- **What**: Test that all vesting schedules are properly exported and can be imported correctly.
- **Where**: `inference-chain/x/streamvesting/keeper/genesis_test.go`
- **Dependencies**: 1.7

#### **4.4 Testermint E2E Tests**
- **Task**: `[ ]` Create comprehensive streamvesting E2E tests
- **What**: Create end-to-end tests following the pattern from `CollateralTests.kt`. Use a **2-epoch vesting period** in genesis configuration for faster testing (instead of 180 epochs). Integrate all test scenarios into **one comprehensive test** for efficiency.
- **Where**: `testermint/src/test/kotlin/StreamVestingTests.kt`
- **Genesis Configuration**: Set vesting period to 2 epochs in test genesis for quick validation
- **Comprehensive Test Scenarios** (all in one test):
    1. **Test Reward Vesting**: Verify that after a reward is claimed, a participant's spendable balance does *not* increase, but their vesting schedule is created correctly.
    2. **Test Epoch Unlocking**: Wait for epoch transitions and verify that vested tokens are released to the participant's spendable balance after 2 epochs.
    3. **Test Reward Aggregation**: Give a participant one reward with a 2-epoch vest. Then, give them a second reward and verify it's correctly aggregated into the existing 2-epoch schedule without extending it.
    4. **Test Top Miner Vesting**: Trigger top miner rewards and verify they also go through the vesting system.
- **Dependencies**: All previous sections

### **Section 5: Network Upgrade**

#### **5.1 Create Upgrade Package**
- **Task**: `[ ]` Create the upgrade package directory
- **What**: Create a new directory for the upgrade named `v2_streamvesting`.
- **Where**: `inference-chain/app/upgrades/v2_streamvesting/`
- **Dependencies**: All previous sections

#### **5.2 Implement Upgrade Handler**
- **Task**: `[ ]` Implement the upgrade handler
- **What**: Create an `upgrades.go` file with a `CreateUpgradeHandler` function. Since we're using epoch-based processing, no special initialization is needed.
- **Where**: `inference-chain/app/upgrades/v2_streamvesting/upgrades.go`
- **Dependencies**: 5.1

#### **5.3 Register Upgrade Handler**
- **Task**: `[ ]` Register the upgrade handler and store
- **What**: Register the upgrade handler and add the new module store following the pattern from the collateral upgrade.
- **Where**: `inference-chain/app/upgrades.go`
- **Logic**:
    1. Define a `storetypes.StoreUpgrades` object with `Added: []string{"streamvesting"}`
    2. Call `app.SetStoreLoader` with the upgrade name and store upgrades
    3. Call `app.UpgradeKeeper.SetUpgradeHandler` with the handler
- **Dependencies**: 5.2 