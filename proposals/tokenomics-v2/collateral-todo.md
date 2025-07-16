# Tokenomics V2: Collateral System - Task Plan

## Prerequisite Reading

Before starting implementation, please read the following documents to understand the full context of the changes:
- The main proposal: `proposals/tokenomics-v2/collateral.md`
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

### Section 1: `x/collateral` Module Scaffolding and Core Logic

#### 1.1 Scaffold New Module
- **Task**: [x] Scaffold the new `x/collateral` module
- **What**: Use `ignite scaffold module collateral --dep staking,inference` to create the basic structure for the new module. This will be the foundation for all collateral management logic.
- **Where**: New directory `inference-chain/x/collateral`
- **Dependencies**: None

#### 1.2 Define Collateral Parameters
- **Task**: [x] Define collateral parameters and genesis state
- **What**: Add `UnbondingPeriodEpochs` to the module's parameters. Define the `GenesisState` to initialize it. Set the default to `1`.
- **Where**:
  - `inference-chain/proto/inference/collateral/params.proto`
  - `inference-chain/proto/inference/collateral/genesis.proto`
- **Why**: This parameter is crucial for the withdrawal unbonding process.
- **Result**: 
  - Added `UnbondingPeriodEpochs` parameter to `params.proto`.
  - Implemented parameter validation in `types/params.go` with a default of 1.
  - Genesis state already properly wired to use the default parameter.
  - Successfully built the inference chain.

#### 1.3 Implement Collateral Storage
- **Task**: [x] Implement collateral storage
- **What**: Create a keeper store to map participant addresses (string) to their collateral amounts (`sdk.Coin`). This will store the state of deposited collateral.
- **Where**: `inference-chain/x/collateral/keeper/keeper.go`
- **Dependencies**: 1.1
- **Result**:
  - Added `CollateralKey` store prefix and `GetCollateralKey()` helper in `types/keys.go`
  - Added bank keeper to the keeper struct and expected keepers interface
  - Implemented storage methods in keeper.go:
    - `SetCollateral()` - stores participant collateral
    - `GetCollateral()` - retrieves participant collateral with existence check
    - `RemoveCollateral()` - removes collateral from store
    - `GetAllCollateral()` - returns all collateral entries (for genesis export)
  - Updated module initialization to pass bank keeper to the keeper
  - Successfully built the project

#### 1.4 Implement `MsgDepositCollateral`
- **Task**: [x] Implement `MsgDepositCollateral`
- **What**: Define the `MsgDepositCollateral` message in protobuf and implement the keeper logic to handle deposits. This includes transferring tokens from the user to the `x/collateral` module account.
- **Where**:
  - `inference-chain/proto/inference/collateral/tx.proto`
  - `inference-chain/x/collateral/keeper/msg_server_deposit_collateral.go`
- **Dependencies**: 1.3
- **Result**:
  - Added `MsgDepositCollateral` message to tx.proto with participant address and amount fields
  - Created `msg_server_deposit_collateral.go` implementing the deposit logic:
    - Validates participant address
    - Transfers tokens from participant to module account
    - Handles adding to existing collateral or creating new entry
    - Prevents mixing different denominations
    - Emits deposit event with participant and amount
  - Created `events.go` with event type and attribute constants
  - Created `msg_deposit_collateral.go` with ValidateBasic() validation
  - Successfully built the project

#### 1.4a Implement Genesis Logic
- **Task**: [x] Implement Genesis Logic
- **What**: Verify that scaffolding correctly created `genesis.go` with `InitGenesis` and `ExportGenesis` functions.
- **Where**: `inference-chain/x/collateral/module/genesis.go`
- **Dependencies**: 1.2
- **Result**:
  - Created `collateral_balance.proto` defining CollateralBalance message type (following SettleAmount pattern from inference module)
  - Updated `genesis.proto` to include `repeated CollateralBalance collateral_balance_list`
  - Enhanced `InitGenesis` to restore all collateral balances from genesis state
  - Enhanced `ExportGenesis` to export all collateral balances using `GetAllCollateral()`
  - Successfully built the project

#### 1.4b Verify Module Wiring and Permissions
- **Task**: [x] Verify Module Wiring and Permissions
- **What**: Verified that the scaffolding correctly wired the module into the `ModuleManager` and Begin/End blockers. Added the one missing piece: the module account permission in `moduleAccPerms`, which is required for the module to hold funds.
- **Where**: `inference-chain/app/app_config.go`
- **Dependencies**: 1.4a
- **Result**:
  - Verified module is properly included in genesis, begin blocker, and end blocker order
  - Added module account permission with `Burner` capability for slashing functionality
  - Fixed test keeper setup in `testutil/keeper/collateral.go` to use proper mocks 
  following inference module pattern
  - Fixed genesis test nil pointer issue by properly initializing Params in test
  - All 422 tests passing, build and basic module integration verified successfully

#### 1.5 Detailed Withdrawal and Unbonding Logic

##### 1.5.1 Define Unbonding Data Structures
- **Task**: [x] Define `UnbondingCollateral` data structures
- **What**: Define a protobuf message for an unbonding entry. Implement a single-key storage approach in the keeper store using `(CompletionEpoch, ParticipantAddress)` format for efficient batch processing by epoch, with automatic aggregation for multiple withdrawals to the same epoch.
- **Where**: `inference-chain/proto/inference/collateral/unbonding.proto` and `inference-chain/x/collateral/keeper/keeper.go`
- **Dependencies**: 1.1
- **Result**:
  - Created `unbonding.proto` with `UnbondingCollateral` message containing participant, amount, and completion_epoch
  - Implemented simplified single-key storage approach with format `unbonding/{completionEpoch}/{participantAddress}`
  - Added keeper methods for unbonding management:
    - `SetUnbondingCollateral()` - automatically aggregates if entry exists
    - `GetUnbondingCollateral()` - retrieves specific entry
    - `RemoveUnbondingCollateral()` - removes single entry
    - `GetUnbondingByEpoch()` - efficient batch retrieval by epoch
    - `RemoveUnbondingByEpoch()` - efficient batch removal by epoch
    - `GetUnbondingByParticipant()` - queries all entries for a participant
    - `GetAllUnbonding()` - for genesis export
  - Updated genesis to handle unbonding entries import/export
  - Successfully built the project

##### 1.5.2 Implement `MsgWithdrawCollateral`
- **Task**: [x] Implement `MsgWithdrawCollateral` to use the unbonding queue
- **What**: Implement the keeper logic for the `MsgWithdrawCollateral` message. This logic should not release funds but instead create an `UnbondingCollateral` entry. The completion epoch should be calculated as `latest_epoch + params.UnbondingPeriodEpochs`.
- **Where**:
  - `inference-chain/proto/inference/collateral/tx.proto`
  - `inference-chain/x/collateral/keeper/msg_server_withdraw_collateral.go`
- **Dependencies**: 1.3, 1.5.1
- **Result**:
  - Added `MsgWithdrawCollateral` and response to tx.proto
  - Implemented withdrawal logic that creates unbonding entries instead of releasing funds
  - Validates participant has sufficient collateral and matching denominations
  - Enforces that all collateral deposits and withdrawals use the base denomination (`nicoin`)
  - Calculates completion epoch using the collateral module's own internal epoch state
  - Reduces active collateral and stores unbonding entry (aggregates if exists)
  - Emits withdrawal event with completion epoch
  - Created validation logic in msg_withdraw_collateral.go
  - Added error types and event constants
  - Registered messages in codec
  - Followed inference module pattern using separate BankKeeper (read) and BankEscrowKeeper (write)
  - Successfully built the project

##### 1.5.3 Implement Unbonding Queue Processing
- **Task**: [x] Create a function to process the unbonding queue
- **What**: Create a new keeper function that iterates through all `UnbondingCollateral` entries for a given epoch and releases the funds back to the participants' spendable balances.
- **Where**: `inference-chain/x/collateral/keeper/keeper.go`
- **Dependencies**: 1.5.1
- **Result**:
  - Implemented `ProcessUnbondingQueue(ctx, completionEpoch)` in the keeper.
  - The function gets all unbonding entries for the given epoch.
  - It iterates through each entry, sending the collateral from the module account back to the participant.
  - Emits a `process_withdrawal` event for each processed entry.
  - Panics if the module account is underfunded, as this indicates a critical logic error.
  - After processing all entries, it removes them from the queue using the `RemoveUnbondingByEpoch` batch-deletion function.
  - Successfully built the project.

##### 1.5.4 Integrate Queue Processing into EndBlocker
- **Task**: [x] Add an `EndBlocker` to the `x/collateral` module to process withdrawals
- **Result**:
  - Refactored the unbonding logic to be triggered by the `x/inference` module for better efficiency and correct timing.
  - Removed the `EndBlocker` from the `x/collateral` module and created an exported `AdvanceEpoch(completedEpoch)` function.
  - The `x/inference` module now calls the `collateralKeeper.AdvanceEpoch` function from within its `onSetNewValidatorsStage`, passing the completed epoch index.
  - This removes the circular dependency between the modules and makes the `collateral` module a self-contained state machine.
  - Successfully built the project with the new, more robust architecture.

#### 1.6 Implement the `Slash` Function
- **Task**: [x] Implement the `Slash` function
- **What**: Create an exported `Slash` function. This function must penalize both *active* collateral and any collateral in the *unbonding queue* **proportionally** based on the slash fraction.
- **Where**: `inference-chain/x/collateral/keeper/keeper.go`
- **Why**: This centralizes the slashing logic, ensuring consistency.
- **Dependencies**: 1.3, 1.5.1
- **Result**:
  - Implemented the `Slash(ctx, participantAddress, slashFraction)` function in the keeper.
  - The function proportionally slashes both active collateral and any collateral in the unbonding queue.
  - It correctly calculates the total amount to be slashed from all of a participant's holdings.
  - After calculating the total, it burns the corresponding coins from the module account.
  - It emits a `slash_collateral` event with the participant, total slashed amount, and the slash fraction.
  - Successfully built the project.

### Section 2: Integration with `x/inference` Module

#### 2.1 Define Slashing Parameters in `x/inference`
- **Task**: [x] Define slashing and weight-related governance parameters
- **What**: Add new governance-votable parameters to the `x/inference` module's `params.proto`:
  - `base_weight_ratio`: The portion of potential weight granted unconditionally. Default `0.2`.
  - `collateral_per_weight_unit`: The collateral required per unit of weight. Default `1`.
  - `slash_fraction_invalid`: Percentage of collateral to slash when a participant is marked `INVALID`. Default `0.20` (20%).
  - `slash_fraction_downtime`: Percentage of collateral to slash for downtime. Default `0.10` (10%).
  - `downtime_missed_percentage_threshold`: The missed request percentage that triggers a downtime slash. Default `0.05` (5%).
  Update `params.go` with default values and validation.
- **Where**:
  - `inference-chain/proto/inference/inference/params.proto`
  - `inference-chain/x/inference/types/params.go`
- **Dependencies**: None
- **Result**:
  - Grouped the new parameters under a `CollateralParams` message in `params.proto` for better organization.
  - Added `slash_fraction_invalid`, `slash_fraction_downtime`, and `downtime_missed_percentage_threshold` to the new message.
  - Implemented default values and validation logic for the new parameters in `params.go`.
  - Successfully built the project.

#### 2.1a Add Grace Period Parameter to `x/inference`
- **Task**: [x] Add `GracePeriodEndEpoch` parameter
- **What**: Add a new governance-votable parameter, `GracePeriodEndEpoch`, to the `CollateralParams` of `x/inference` module's `params.proto`. This parameter defines the epoch number at which the collateral requirement grace period ends. Set its default value to `180`.
- **Where**:
  - `inference-chain/proto/inference/inference/params.proto`
  - `inference-chain/x/inference/types/params.go`
- **Why**: To make the initial collateral-free period configurable via governance.
- **Dependencies**: None

#### 2.2 Implement Collateral-Based Weight Adjustment
- **Task**: [x] Implement collateral-based weight adjustment
- **What**: Create a new keeper function, `AdjustWeightsByCollateral`. This function will iterate through all active participants after their `PotentialWeight` has been calculated by `ComputeNewWeights`. It will adjust their weights based on the new collateral logic:
  - If the current epoch is before or at `GracePeriodEndEpoch`, no adjustment is made.
  - After the grace period, it queries the `x/collateral` module for active collateral. It calculates `BaseWeight` (e.g., 20% of `PotentialWeight`) and then activates additional weight based on the participant's collateral, up to the remaining `Collateral-Eligible Weight`.
- **Where**: Create the new function in a new file, `inference-chain/x/inference/keeper/collateral_weight.go`. Call this function from `onSetNewValidatorsStage` in `inference-chain/x/inference/module/module.go` immediately after the call to `am.keeper.ComputeNewWeights`.
- **Why**: This implements the core logic of Tokenomics V2, where network weight is backed by financial collateral after an initial grace period.
- **Dependencies**: 1.3, 2.1a
- **Result**:
  - Refactored the architecture to move `BaseWeightRatio` and `CollateralPerWeightUnit` from the `x/collateral` module to `x/inference` for better cohesion.
  - Created a new `AdjustWeightsByCollateral` function in `inference-chain/x/inference/keeper/collateral_weight.go` (renamed from `weight.go`).
  - The function now correctly and efficiently adjusts the `Weight` of `ActiveParticipant` objects in-memory.
  - Integrated the new function into the epoch lifecycle by calling it from `onSetNewValidatorsStage` in `module.go`.
  - Ensured all logic sources parameters from the correct module and the project builds successfully.

#### 2.3 Trigger Slashing When Participant is Marked `INVALID`
- **Task**: [x] Trigger slash when participant status becomes `INVALID`
- **What**: Add logic to trigger a call to the `x/collateral` module's `Slash` function at the moment a participant's status changes to `INVALID`. The slash amount will be determined by the new `SlashFractionInvalid` governance parameter. This requires checking the participant's status before and after it is recalculated.
- **Where**: This logic must be added in two places:
  1. `inference-chain/x/inference/keeper/msg_server_invalidate_inference.go`: Inside `InvalidateInference`, after `calculateStatus` is called.
  2. `inference-chain/x/inference/keeper/msg_server_validation.go`: Inside `Validation`, after `calculateStatus` is called.
- **Dependencies**: 1.6, 2.1
- **Result**:
  - Added the `Slash` method to the `CollateralKeeper` interface in `x/inference/types/expected_keepers.go`.
  - Implemented logic in `msg_server_invalidate_inference.go` to check for a status transition to `INVALID` and trigger a collateral slash using the `SlashFractionInvalid` parameter.
  - Implemented the same slashing logic in `msg_server_validation.go` to ensure consistent punishment.
  - Refactored the duplicated logic into a shared `CheckAndSlashForInvalidStatus` function in `inference-chain/x/inference/keeper/collateral.go`.
  - Renamed `collateral_weight.go` to `collateral.go` to better reflect its purpose.

#### 2.4 Trigger Slashing for Downtime at End of Epoch
- **Task**: [x] Add downtime slashing trigger to epoch settlement
- **What**: Enhance the `x/inference` module by adding logic to check each participant's performance for the completed epoch. If their missed request percentage exceeds the `DowntimeMissedPercentageThreshold` parameter, it should trigger a call to the `x/collateral` module's `Slash` function.
- **Where**: The new logic has been placed inside the `SettleAccount` function in `inference-chain/x/inference/keeper/accountsettle.go`, which is a more efficient location than originally planned.
- **Dependencies**: 1.6, 2.1
- **Result**:
  - Created a new `CheckAndSlashForDowntime` function in `inference-chain/x/inference/keeper/collateral.go`.
  - This function calculates a participant's missed request percentage for the epoch and compares it to the `DowntimeMissedPercentageThreshold` parameter.
  - If the threshold is exceeded, it slashes the participant's collateral using the `SlashFractionDowntime` parameter.
  - The logic is called from `SettleAccount` in `accountsettle.go`, which ensures it runs exactly once per participant at the end of each epoch, right after their final performance stats are available.

### Section 3: Integration with `x/staking` via Hooks

#### 3.1 Implement `StakingHooks` Interface
- **Task**: [x] Implement and register `StakingHooks`
- **What**: Implement the `StakingHooks` interface in the `x/collateral` module. Register these hooks with the `staking` keeper so the module can react to validator state changes.
- **Where**:
  - A new file `inference-chain/x/collateral/module/hooks.go`
  - `inference-chain/x/collateral/module/module.go` (for registration)
- **Why**: This allows consensus-level penalties to be mirrored in the application-specific collateral system.
- **Dependencies**: 1.6
- **Result**:
  - Created a new `hooks.go` file in `x/collateral/module` with the `StakingHooks` implementation.
  - Updated `x/collateral/types/expected_keepers.go` to include the `SetHooks` method in the `StakingKeeper` interface.
  - Registered the new hooks with the `stakingKeeper` in `x/collateral/module/module.go`.

#### 3.2 Implement `BeforeValidatorSlashed` Hook
- **Task**: [x] Implement `BeforeValidatorSlashed` logic
- **What**: When a validator is slashed at the consensus level, this hook should trigger a proportional slash of the corresponding participant's collateral in the `x/collateral` module.
- **Where**: `inference-chain/x/collateral/hooks.go`
- **Dependencies**: 3.1
- **Result**:
  - Implemented the `BeforeValidatorSlashed` hook.
  - The logic now directly converts the validator's address (`ValAddress`) to its corresponding account address (`AccAddress`) and attempts to slash collateral. This simplifies the implementation by removing the dependency on the `x/inference` module for this hook.

#### 3.3 Implement `AfterValidatorBeginUnbonding` Hook
- **Task**: [x] Implement `AfterValidatorBeginUnbonding` logic
- **What**: When a validator starts unbonding (e.g., is jailed), this hook should trigger a state change in the `x/collateral` module, potentially restricting the participant's collateral usage.
- **Where**: `inference-chain/x/collateral/hooks.go`
- **Dependencies**: 3.1
- **Result**:
  - Implemented the `AfterValidatorBeginUnbonding` hook to create a persistent record of a participant's jailed status.
  - This is achieved by calling a new `k.SetJailed()` method in the collateral keeper, which stores the participant's address.
  - This state can now be queried by other modules or functions in the future to restrict actions for jailed participants.

#### 3.4 Implement `AfterValidatorBonded` Hook
- **Task**: [x] Implement `AfterValidatorBonded` logic
- **What**: When a validator becomes bonded again, this hook should signal that the participant's collateral can be considered fully active again.
- **Where**: `inference-chain/x/collateral/hooks.go`
- **Dependencies**: 3.1
- **Result**:
  - Implemented the `AfterValidatorBonded` hook to remove a participant's jailed status from the persistent store.
  - This is done by calling the new `k.RemoveJailed()` method, ensuring the on-chain state accurately reflects the validator's return to the active set.

### Section 4: Queries, Events, and CLI

#### 4.1 Implement Query Endpoints
- **Task**: [ ] Implement Query Endpoints
- **What**: Implement gRPC and REST query endpoints for fetching participant collateral (active and unbonding) and module parameters.
- **Where**:
  - `inference-chain/proto/inference/collateral/query.proto`
  - `inference-chain/x/collateral/keeper/query_server.go`
- **Dependencies**: 1.3, 1.5.1

#### 4.2 Implement Event Emitting
- **Task**: [ ] Add event emitting to key functions
- **What**: Emit strongly-typed events for deposits, withdrawals, and slashing to facilitate off-chain tracking.
- **Where**:
  - `inference-chain/x/collateral/keeper/msg_server_*.go`
  - `inference-chain/x/collateral/keeper/keeper.go` (in the `Slash` function)
- **Dependencies**: 1.4, 1.5.2, 1.6

#### 4.3 Implement CLI Commands
- **Task**: [ ] Implement CLI commands
- **What**: Create CLI commands for all new messages and queries to allow for easy interaction and testing.
- **Where**: `inference-chain/x/collateral/client/cli/`
- **Dependencies**: 4.1

### Section 5: Testing and Integration

#### 5.1 Unit Tests for `x/collateral`
- **Task**: [ ] Write unit tests for the `x/collateral` module
- **What**: Create comprehensive unit tests for the new module, covering deposits, withdrawals (with unbonding), proportional slashing, queries, and hooks.
- **Where**: `inference-chain/x/collateral/keeper/`
- **Dependencies**: Section 1, Section 3, Section 4

#### 5.2 Integration Tests
- **Task**: [ ] Write integration tests for all new mechanics
- **What**: Write end-to-end tests covering the full lifecycle: depositing collateral, gaining weight, and getting slashed under different conditions (cheating, downtime, consensus faults).
- **Where**: `inference-chain/x/inference/` integration tests
- **Dependencies**: Section 2, Section 3, Section 4

### Section 6: Testermint E2E Tests

**Objective**: To verify the end-to-end functionality of the collateral and slashing system in a live test network environment. All tests will be implemented in a new `CollateralTests.kt` file, following the structure of `GovernanceTests.kt`. Each test will be a separate `@Test` function within the `CollateralTests` class.

**Where**: A new file `testermint/src/test/kotlin/CollateralTests.kt`

#### **6.1 Test Successful Collateral Deposit**
- **Task**: [ ] Create test for `MsgDepositCollateral`
- **What**: Implement a new `@Test` function that creates a scenario where a participant successfully deposits collateral.
- **Scenario**:
    1. Initialize the network using `initCluster()`.
    2. Select a funded participant.
    3. Query their initial collateral (should be zero).
    4. Execute a `deposit-collateral` transaction.
    5. Query their final collateral and verify it has increased by the deposited amount.
    6. Verify their spendable balance has decreased accordingly.

#### **6.2 Test Unbonding and Withdrawal**
- **Task**: [ ] Create test for the full `MsgWithdrawCollateral` lifecycle
- **What**: Implement a new `@Test` function that verifies the unbonding period and the eventual release of funds.
- **Scenario**:
    1. A participant deposits collateral (building on 6.1).
    2. They submit a `withdraw-collateral` request.
    3. **Immediately after**, verify their active collateral is now zero, but their spendable balance has *not* yet increased.
    4. Query the `unbonding-collateral` queue and confirm their withdrawal is present.
    5. Wait for `UnbondingPeriodEpochs` + 1 epochs to pass.
    6. Verify their spendable balance has now increased by the withdrawn amount.
    7. Verify the unbonding queue for that completion epoch is now empty.

#### **6.3 Test Slashing for `INVALID` Status**
- **Task**: [ ] Create test for slashing due to malicious behavior
- **What**: Implement a new `@Test` function where a participant gets marked `INVALID` and their collateral is slashed.
- **Scenario**:
    1. A participant deposits a known amount of collateral (e.g., 1000 tokens).
    2. Configure the mock server for that participant to consistently return invalid inference results.
    3. Run enough invalid inferences to cross the statistical threshold and trigger the `INVALID` status change.
    4. Verify the participant's status is now `INVALID`.
    5. Query their collateral and confirm it has been reduced by the `SlashFractionInvalid` percentage (e.g., reduced to 800 tokens if the slash is 20%).

#### **6.4 Test Slashing for Downtime**
- **Task**: [ ] Create test for downtime-based slashing
- **What**: Implement a new `@Test` function where a participant is slashed for being unresponsive.
- **Scenario**:
    1. A participant deposits a known amount of collateral.
    2. Configure their mock server to be unresponsive or to have a long delay.
    3. Send enough inference requests to them to ensure their "missed request" rate for the epoch will exceed the `DowntimeMissedPercentageThreshold`.
    4. Wait for the epoch to end.
    5. Verify their collateral has been reduced by the `SlashFractionDowntime` percentage.

#### **6.5 Test Proportional Slashing (Active vs. Unbonding)**
- **Task**: [ ] Create test for proportional slashing of unbonding collateral
- **What**: Implement a new `@Test` function for a complex scenario to ensure slashing is applied proportionally to both active and unbonding funds.
- **Scenario**:
    1. A participant deposits 2000 tokens.
    2. They initiate a withdrawal for 1000 tokens, placing it in the unbonding queue. They now have 1000 active and 1000 unbonding collateral.
    3. Trigger a slashing event (e.g., for downtime with a 10% slash).
    4. Verify their active collateral is now 900 tokens (1000 - 10%).
    5. Verify the amount in their unbonding queue is now 900 tokens (1000 - 10%).
    6. After the unbonding period, verify they receive only 900 tokens back.

### Section 7: Network Upgrade

**Objective**: To create and register the necessary network upgrade handler to activate the collateral system on the live network.

#### **7.1 Create Upgrade Package**
- **Task**: [ ] Create the upgrade package directory
- **What**: Create a new directory for the upgrade. It should be named `v2_collateral` to represent the major feature addition.
- **Where**: `inference-chain/app/upgrades/v2_collateral/`
- **Dependencies**: All previous sections.

#### **7.2 Implement Upgrade Handler**
- **Task**: [ ] Implement the upgrade handler logic
- **What**: Create an `upgrades.go` file with a `CreateUpgradeHandler` function. This handler will perform the one-time state migration for the `x/inference` module's parameters.
- **Logic**:
    1. Inside the handler, get the `x/inference` keeper.
    2. Read the existing module parameters.
    3. Add the three new slashing and downtime parameters with their agreed-upon default values.
    4. Set the updated parameters back into the store.
- **Where**: `inference-chain/app/upgrades/v2_collateral/upgrades.go`
- **Dependencies**: 7.1

#### **7.3 Register Upgrade Handler in `app.go`**
- **Task**: [ ] Register the upgrade handler and new module store
- **What**: Modify the main application setup to be aware of the new upgrade. This involves two steps: defining the new store and registering the handler.
- **Where**: `inference-chain/app/upgrades.go` (in the `setupUpgradeHandlers` function)
- **Logic**:
    1. Define a `storetypes.StoreUpgrades` object that includes an `Added: []string{"collateral"}` entry.
    2. Call `app.SetStoreLoader` with the upgrade name and the store upgrades object.
    3. Call `app.UpgradeKeeper.SetUpgradeHandler`, passing it the `v2_collateral` upgrade name and the `CreateUpgradeHandler` function from the new package.
- **Dependencies**: 7.2 