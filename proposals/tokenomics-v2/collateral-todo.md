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
    - `GetAllCollaterals()` - returns all collateral entries (for genesis export)
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
  - Enhanced `ExportGenesis` to export all collateral balances using `GetAllCollaterals()`
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
    - `GetAllUnbondings()` - for genesis export
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

### Section 1a: Post-Implementation Refactoring and Verification
- **Task**: [x] - Finished Refactor all keeper iterators and run full test suite.
- **What**: A bug was discovered where `ExportGenesis` was exporting incorrect data because a store iterator was not correctly bounded. All iterators in the `x/collateral` keeper were refactored to use the safer `prefix.NewStore` pattern.
- **Where**: `inference-chain/x/collateral/keeper/keeper.go`
- **Why**: This fixes the critical genesis export bug, prevents similar bugs in other iteration functions, and aligns the module with best practices used in `x/inference`.
- **Result**:
    - All keeper functions using iterators were updated (`GetAllCollaterals`, `GetAllUnbondings`, `GetAllJailed`, etc.).
    - All tests for `x/collateral`, `x/inference`, `make node-test`, and `make api-test` were executed and passed, confirming the refactoring did not introduce regressions.

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
- **Task**: [x] - Finished Implement Query Endpoints
- **What**: Implement gRPC and REST query endpoints for fetching participant collateral (active and unbonding) and module parameters.
- **Where**:
  - `inference-chain/proto/inference/collateral/query.proto`
  - `inference-chain/x/collateral/keeper/query_server.go`
- **Dependencies**: 1.3, 1.5.1
- **Result**:
  - Defined query services and messages in `query.proto` for parameters, single participant collateral, all collateral, and unbonding queues.
  - Implemented the corresponding logic in `query_server.go`.
  - Correctly generated the Go protobuf code to eliminate compilation errors.

#### 4.2 Implement Event Emitting
- **Task**: [x] - Finished Add event emitting to key functions
- **What**: Emit strongly-typed events for deposits, withdrawals, and slashing to facilitate off-chain tracking.
- **Where**:
  - `inference-chain/x/collateral/keeper/msg_server_*.go`
  - `inference-chain/x/collateral/keeper/keeper.go` (in the `Slash` function)
- **Dependencies**: 1.4, 1.5.2, 1.6
- **Result**: All required events (`DepositCollateral`, `WithdrawCollateral`, `SlashCollateral`, `ProcessWithdrawal`) were already implemented in previous tasks (1.4, 1.5.2, 1.5.3, and 1.6). This task was a verification step and is now complete.

#### 4.3 Implement CLI Commands
- **Task**: [x] - Finished
- **What**: Create CLI commands for all new messages and queries to allow for easy interaction and testing.
- **Where**: `inference-chain/x/collateral/client/cli/`
- **Dependencies**: 4.1
- **Result**: Added CLI commands for all new queries (`Collateral`, `AllCollaterals`, `UnbondingCollateral`, `AllUnbondingCollateral`) and messages (`DepositCollateral`, `WithdrawCollateral`) to `inference-chain/x/collateral/module/autocli.go`. The project builds successfully with these changes.

### Section 5: Testing and Integration

#### 5.1 Unit Tests for `x/collateral`
- **Task**: [x] - Finished Write unit tests for the `x/collateral` module
- **What**: Create comprehensive unit tests for the new module, covering deposits, withdrawals (with unbonding), proportional slashing, queries, and hooks.
- **Where**: `inference-chain/x/collateral/keeper/`
- **Dependencies**: Section 1, Section 3, Section 4
- **Result**:
  - Implemented a full test suite using `testify/suite` in `keeper_test.go`.
  - Created multiple test files for organizational clarity:
    - `msg_server_test.go`: Covers `DepositCollateral` and `WithdrawCollateral` message logic, including success, aggregation, and failure cases.
    - `epoch_processing_test.go`: Verifies that the `AdvanceEpoch` function correctly processes the unbonding queue and correctly ignores future-dated entries.
    - `slashing_test.go`: Tests the `Slash` function under various conditions, including proportional slashing of active/unbonding collateral and edge cases.
    - `hooks_test.go`: Ensures the staking hooks for jailing and slashing validators correctly trigger the corresponding actions in the collateral module.
    - `genesis_test.go`: Validates the `InitGenesis` and `ExportGenesis` functions for a complete state import/export cycle.
  - All tests passed, ensuring the module's core logic is robust and correct.

- **Detailed Test Cases**:
    - **MsgServer - Deposit Collateral**:
        - `[x]` **Test Success**: A participant deposits collateral for the first time. Verify the amount is moved to the module account and the participant's collateral record is created correctly.
        - `[x]` **Test Aggregation**: A participant with existing collateral deposits more. Verify the new amount is added to their existing collateral.
        - `[x]` **Test Invalid Denom**: A participant attempts to deposit a token other than the bond denom (`nicoin`). Verify the transaction fails.
    - **MsgServer - Withdraw Collateral & Unbonding**:
        - `[x]` **Test Success**: A participant withdraws a portion of their collateral. Verify their active collateral is reduced and an `UnbondingCollateral` entry is created with the correct amount and `completionEpoch`.
        - `[x]` **Test Insufficient Funds**: A participant attempts to withdraw more collateral than they have. Verify the transaction fails.
        - `[x]` **Test Full Withdrawal**: A participant withdraws all of their collateral. Verify their active collateral becomes zero and the unbonding entry is created.
        - `[x]` **Test Unbonding Aggregation**: A participant submits a withdrawal, then submits another one that will complete in the same epoch. Verify the two amounts are aggregated into a single `UnbondingCollateral` entry.
    - **Epoch Processing (`AdvanceEpoch`)**:
        - `[x]` **Test Queue Processing**: Manually create several `UnbondingCollateral` entries for the current epoch. Call `AdvanceEpoch`. Verify the funds are returned to the participants' spendable balances and the unbonding entries are removed from the queue.
        - `[x]` **Test No-Op for Future Epochs**: Create unbonding entries for a future epoch. Call `AdvanceEpoch` for the current epoch. Verify the future-dated entries are untouched.
    - **Slashing (`Slash` function)**:
        - `[x]` **Test Proportional Slashing**: A participant has 1000 active and 1000 unbonding collateral. Trigger a 10% slash. Verify their active collateral becomes 900 and their unbonding collateral becomes 900. Also, verify that 200 tokens are burned from the module account.
        - `[x]` **Test Active-Only Slashing**: A participant has active collateral but none unbonding. Trigger a slash and verify only the active collateral is reduced.
        - `[x]` **Test Unbonding-Only Slashing**: A participant has only unbonding collateral.
        - `[x]` **Test Invalid Fraction**: A participant attempts to slash with a fraction > 1 or < 0 and verifies that the transaction fails.
    - **Staking Hooks**:
        - `[x]` **Test `BeforeValidatorSlashed`**: Mock a call from the staking keeper. Verify the associated participant's collateral is slashed proportionally.
        - `[x]` **Test `AfterValidatorBeginUnbonding` (Jailing)**: Mock the hook call for a validator being jailed. Verify the associated participant is added to the jailed list in the collateral keeper.
        - `[x]` **Test `AfterValidatorBonded` (Un-jailing)**: Add a participant to the jailed list, then mock the hook call for the validator being bonded. Verify the participant is removed from the jailed list.
    - **Genesis Import/Export**:
        - `[x]` **Test Full State Cycle**: Populate the keeper with active collateral, unbonding entries, and jailed participants. Call `ExportGenesis`. Then, use that exported state to call `InitGenesis` on a new, empty keeper. Verify that all data is restored identically.

#### 5.2 Integration Tests
- **Task**: [x] - Finished Write integration tests for all new mechanics
- **What**: Write end-to-end tests covering the full lifecycle: depositing collateral, gaining weight, and getting slashed under different conditions (cheating, downtime, consensus faults).
- **Where**: `inference-chain/x/inference/keeper/collateral_integration_test.go`
- **Dependencies**: Section 2, Section 3, Section 4
- **Result**:
  - Created a new integration test file dedicated to verifying the interaction between the `inference` and `collateral` modules.
  - Implemented tests using both mock keepers for isolated logic and a "real" keeper setup (with a shared in-memory state store) for true end-to-end validation.
  - All detailed test cases below were successfully implemented and passed, confirming that collateral-based weight adjustments and the various slashing mechanisms (for invalid status, downtime, and combined scenarios) function correctly.

- **Detailed Test Cases**:
    - **Collateral-Based Weight Adjustment**:
        - `[x]` **Test Grace Period**: Set the current epoch to be before `GracePeriodEndEpoch`. Have a participant perform work to get `PotentialWeight`. Verify their final `Weight` is equal to their `PotentialWeight`, regardless of collateral.
        - `[x]` **Test Post-Grace Period (No Collateral)**: Set the current epoch to be after the grace period. A participant with zero collateral gets `PotentialWeight`. Verify their final `Weight` is only the `BaseWeight` (e.g., 20% of `PotentialWeight`).
        - `[x]` **Test Post-Grace Period (Full Collateral)**: A participant has enough collateral to back 100% of their `Collateral-Eligible Weight`. Verify their final `Weight` equals their `PotentialWeight`.
        - `[x]` **Test Post-Grace Period (Partial Collateral)**: A participant has enough collateral to back 50% of their `Collateral-Eligible Weight`. Verify their final `Weight` is `BaseWeight + (0.5 * Collateral-Eligible Weight)`.
    - **Slashing for `INVALID` Status**:
        - `[x]` **Test Full Flow**: A participant deposits collateral. Simulate them providing incorrect inference results until their status changes to `INVALID`. Verify that at the moment of the status flip, the `x/collateral` keeper slashes their funds by the `SlashFractionInvalid` percentage.
    - **Slashing for Downtime**:
        - `[x]` **Test Full Flow**: A participant deposits collateral. Simulate them missing enough requests to exceed the `DowntimeMissedPercentageThreshold`. Advance the epoch. Verify that during epoch settlement, the `x/collateral` keeper slashes their funds by the `SlashFractionDowntime` percentage.
    - **Combined Slashing Scenario**:
        - `[x]` **Test Double Jeopardy**: A participant is slashed for downtime at the end of an epoch. In the next epoch, they are marked `INVALID`. Verify both slashes are applied correctly and the remaining collateral is calculated as expected.

### Section 6: Testermint E2E Tests

**Objective**: To verify the end-to-end functionality of the collateral and slashing system in a live test network environment. All tests are implemented in `CollateralTests.kt`, following the structure of `GovernanceTests.kt`. Tests have been merged into comprehensive scenarios for better coverage and efficiency.

**Where**: `testermint/src/test/kotlin/CollateralTests.kt`

#### **6.1 Comprehensive Deposit and Withdrawal Test**
- **Task**: [x] - Finished Create test for complete `MsgDepositCollateral` and `MsgWithdrawCollateral` lifecycle
- **Test Name**: `a participant can deposit collateral and withdraw it`
- **What**: Implement a comprehensive test that covers the full collateral deposit and withdrawal lifecycle.
- **Scenario**:
    1. Initialize the network using `initCluster()`.
    2. Query initial collateral (should be zero).
    3. Execute a `deposit-collateral` transaction and verify balances.
    4. Submit a `withdraw-collateral` request.
    5. Verify active collateral is zero, but spendable balance has *not* yet increased.
    6. Query the `unbonding-collateral` queue and confirm the withdrawal is present.
    7. Wait for `UnbondingPeriodEpochs` + 1 epochs to pass.
    8. Verify spendable balance has increased by the withdrawn amount and the queue is empty.
- **Result**: Successfully implemented and verified the complete deposit/withdrawal lifecycle including unbonding mechanics.

#### **6.2 Comprehensive Downtime Slashing with Proportional Distribution**
- **Task**: [x] - Finished Create merged test for downtime slashing and proportional slashing
- **Test Name**: `a participant is slashed for downtime with unbonding slashed`
- **What**: Implement a comprehensive test that combines downtime detection, collateral withdrawal, and proportional slashing of both active and unbonding collateral.
- **Scenario**:
    1. A participant deposits collateral (1000 tokens).
    2. Create inference timeouts by configuring invalid mock responses.
    3. Wait for inference expiration to trigger downtime conditions.
    4. **Withdraw portion of collateral** (400 tokens) to create unbonding entry (600 active, 400 unbonding).
    5. Wait for epoch to end and trigger downtime slashing.
    6. Verify **proportional slashing** of both:
       - Active collateral: `600 - (600 × slashFractionDowntime)`
       - Unbonding collateral: `400 - (400 × slashFractionDowntime)`
- **Result**: Successfully implemented a comprehensive test that verifies downtime detection, timeout mechanisms, and proportional slashing across both active and unbonding collateral pools. This merged test covers the functionality of the original separate tests for downtime slashing, proportional slashing, and mixed collateral scenarios.

**Summary**: The testermint E2E test suite has been optimized from 5 separate tests into 2 comprehensive tests that provide better coverage while being more efficient to run. The merged tests validate the complete collateral system including deposits, withdrawals, unbonding, timeouts, downtime detection, and proportional slashing mechanics.

### Section 7: Network Upgrade

**Objective**: To create and register the necessary network upgrade handler to activate both the collateral and streamvesting systems on the live network in a single coordinated upgrade.

#### **7.1 Create Combined Upgrade Package**
- **Task**: [x] Create the upgrade package directory for both modules
- **What**: Create a new directory for the upgrade. It should be named `v1_15` to represent the major tokenomics v2 feature addition that includes both collateral and streamvesting systems.
- **Where**: `inference-chain/app/upgrades/v1_15/`
- **Dependencies**: All previous sections from both collateral and streamvesting.
- **Result**: ✅ **COMPLETED** - Successfully created `inference-chain/app/upgrades/v1_15/` directory for the combined upgrade.

#### **7.2 Create Constants File**
- **Task**: [x] Create the constants file
- **What**: Create a `constants.go` file defining the upgrade name.
- **Content**:
  ```go
  package v1_15
  
  const UpgradeName = "v0.1.15"
  ```
- **Where**: `inference-chain/app/upgrades/v1_15/constants.go`
- **Dependencies**: 7.1
- **Result**: ✅ **COMPLETED** - Successfully created constants file with upgrade name `v0.1.15`.

#### **7.3 Implement Combined Upgrade Handler**
- **Task**: [x] Implement the upgrade handler logic for both modules
- **What**: Create an `upgrades.go` file with a `CreateUpgradeHandler` function. This handler will perform the one-time state migration and module initialization.
- **Logic**:
    1. **Log upgrade start**: Log the beginning of the v1_15 upgrade process
    2. **Initialize collateral module parameters**: Set default values for the new collateral-related parameters in the `x/inference` module:
       - `CollateralParams.BaseWeightRatio`: Default `0.2` (20%)
       - `CollateralParams.CollateralPerWeightUnit`: Default `1`
       - `CollateralParams.SlashFractionInvalid`: Default `0.20` (20%)
       - `CollateralParams.SlashFractionDowntime`: Default `0.10` (10%)
       - `CollateralParams.DowntimeMissedPercentageThreshold`: Default `0.05` (5%)
       - `CollateralParams.GracePeriodEndEpoch`: Default `180`
    3. **Initialize vesting module parameters**: Set default values for the new vesting-related parameters in the `x/inference` module:
       - `TokenomicsParams.WorkVestingPeriod`: Default `0`
       - `TokenomicsParams.RewardVestingPeriod`: Default `0`
       - `TokenomicsParams.TopMinerVestingPeriod`: Default `0`
    4. **Initialize streamvesting module parameters**: The streamvesting module will use its default parameter (`RewardVestingPeriod`: `180`)
    5. **Module store initialization**: Both modules' stores will be automatically initialized during the migration process
    6. **Validation**: Verify that parameters were set correctly
    7. **Log completion**: Log successful completion of the upgrade
- **Where**: `inference-chain/app/upgrades/v1_15/upgrades.go`
- **Dependencies**: 7.2
- **Result**: ✅ **COMPLETED** - Successfully implemented comprehensive upgrade handler that initializes all collateral and vesting parameters, handles module store initialization via migrations, includes parameter validation and comprehensive logging.

#### **7.4 Register Combined Upgrade Handler in `app.go`**
- **Task**: [x] Register the upgrade handler and new module stores
- **What**: Modify the main application setup to be aware of the new upgrade. This involves defining the new stores and registering the handler.
- **Where**: `inference-chain/app/upgrades.go` (in the `setupUpgradeHandlers` function)
- **Logic**:
    1. **Import the v1_15 package**: Add import for the new upgrade package
    2. **Define store upgrades**: Create a `storetypes.StoreUpgrades` object that includes both new modules
    3. **Set store loader**: Call `app.SetStoreLoader` with the upgrade name and the store upgrades object (only when this specific upgrade is being applied)
    4. **Register handler**: Call `app.UpgradeKeeper.SetUpgradeHandler`, passing it the `v1_15.UpgradeName` and the `CreateUpgradeHandler` function from the new package
- **Dependencies**: 7.3
- **Result**: ✅ **COMPLETED** - Successfully registered v1_15 upgrade handler in `app/upgrades.go`. Added proper store loader for both collateral and streamvesting modules, registered upgrade handler, and verified successful build of both inference chain and API components.

#### **7.5 Integration Testing**
- **Task**: [ ] Test the upgrade process
- **What**: Verify that the upgrade works correctly in a test environment.
- **Testing Steps**:
    1. **Pre-upgrade state**: Start a network with the previous version
    2. **Prepare upgrade**: Submit an upgrade proposal for `v0.1.15`
    3. **Execute upgrade**: Allow the upgrade to execute at the specified height
    4. **Post-upgrade validation**: 
       - Verify both modules are active and functional
       - Check that collateral deposits/withdrawals work
       - Verify that rewards are being vested through streamvesting
       - Confirm all new parameters are set to their default values
       - Test weight adjustments based on collateral
       - Test slashing mechanisms
    5. **End-to-end testing**: Run a complete tokenomics v2 workflow
- **Where**: Local testnet and testermint integration tests
- **Dependencies**: 7.4

**Summary**: The v1_15 upgrade will be a major release that activates the complete Tokenomics V2 system, including both collateral requirements for network weight and reward vesting mechanics. This upgrade represents the transition from the grace period system to the full collateral-backed participation model. The upgrade will activate collateral parameters in the inference module and three key vesting parameters (`WorkVestingPeriod`, `RewardVestingPeriod`, `TopMinerVestingPeriod`) that enable reward vesting through the streamvesting system.

### Section 8: Governance Integration

**Objective**: To ensure all new tokenomics parameters can be modified through on-chain governance voting, providing decentralized control over the economic parameters.

#### **8.1 Add Parameter Keys for Vesting Parameters**
- **Task**: [x] Add parameter keys for the three vesting parameters in the inference module
- **What**: Add parameter key constants for `WorkVestingPeriod`, `RewardVestingPeriod`, and `TopMinerVestingPeriod` to make them governable.
- **Where**: `inference-chain/x/inference/types/params.go` (in the parameter key constants section)
- **Why**: These parameters need to be governable so the community can adjust vesting periods through proposals.
- **Dependencies**: Section 7 (Network Upgrade)
- **Result**: ✅ **COMPLETED** - Successfully added parameter keys `KeyWorkVestingPeriod`, `KeyRewardVestingPeriod`, and `KeyTopMinerVestingPeriod` to the inference module's parameter system.

#### **8.2 Update ParamSetPairs for Vesting Parameters**
- **Task**: [x] Include vesting parameters in governance parameter set
- **What**: Update the `ParamSetPairs()` function to include the three new vesting parameters with proper validation functions.
- **Where**: `inference-chain/x/inference/types/params.go` (in the `ParamSetPairs()` method)
- **Dependencies**: 8.1
- **Result**: ✅ **COMPLETED** - Successfully implemented `ParamSetPairs()` method for `TokenomicsParams` with proper validation. Created `validateVestingPeriod()` function that handles both pointer and direct value types. All three vesting parameters now properly integrated into governance system.

#### **8.3 Test Governance Parameter Changes**
- **Task**: [x] Create tests for governance parameter updates
- **What**: Create unit tests that verify the three vesting parameters can be updated through governance parameter change proposals.
- **Where**: `inference-chain/x/inference/keeper/params_test.go` or similar test file
- **Dependencies**: 8.2
- **Result**: ✅ **COMPLETED** - Successfully implemented comprehensive test suite with 3 test functions:
  - `TestTokenomicsParamsGovernance()`: Tests parameter updates with different vesting periods (0, 180, mixed values, test values)
  - `TestVestingParameterValidation()`: Tests validation function with valid/invalid parameter types
  - `TestTokenomicsParamsParamSetPairs()`: Tests parameter registration and key mapping
All tests passing and verifying governance parameter functionality works correctly.

#### **8.4 Testermint Governance E2E Test**
- **Task**: [x] Create E2E test for vesting parameter governance
- **What**: Create a testermint E2E test that submits a parameter change proposal to modify one of the vesting parameters and verifies it takes effect.
- **Where**: `testermint/src/test/kotlin/VestingGovernanceTests.kt`
- **Dependencies**: 8.3
- **Result**: ✅ **COMPLETED** - Successfully implemented comprehensive E2E governance test that:
  1. Starts with initial vesting periods (2 epochs each)
  2. Submits governance proposal to change parameters (5, 10, 15 epochs respectively)
  3. Votes on proposal and waits for execution
  4. Verifies parameters updated correctly
  5. Tests that new rewards use updated vesting periods
  6. Confirms existing vesting schedules remain unaffected
Test validates complete governance flow from proposal submission to parameter change verification.

**Summary**: After completing governance integration, all tokenomics v2 parameters will be fully governable: collateral parameters (BaseWeightRatio, SlashFractions, etc.), streamvesting module parameter (RewardVestingPeriod), and the three inference module vesting parameters (WorkVestingPeriod, RewardVestingPeriod, TopMinerVestingPeriod). This ensures the economic system remains adaptable through decentralized governance. 