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
- **Task**: `[ ]` Scaffold the new `x/vesting` module
- **What**: Use `ignite scaffold module vesting --dep inference` to create the basic structure.
- **Where**: New directory `inference-chain/x/vesting`
- **Dependencies**: None

#### **1.2 Define Vesting Data Structures**
- **Task**: `[ ]` Define `VestingSchedule` data structures
- **What**: Define a protobuf message for a participant's vesting schedule, which will contain a list of daily vesting amounts. Implement a keeper store to map a participant's address to their `VestingSchedule`.
- **Where**:
  - `inference-chain/x/vesting/types/vesting_schedule.proto`
  - `inference-chain/x/vesting/keeper/keeper.go`
- **Dependencies**: 1.1

#### **1.3 Implement Reward Addition Logic**
- **Task**: `[ ]` Implement the core reward vesting logic
- **What**: Create an exported keeper function `AddVestedRewards(address, amount, vesting_days)`. This function will retrieve a participant's schedule and add the new reward according to the aggregation logic described in the proposal (divide by N, add remainder to the first element, and extend the array if necessary).
- **Where**: `inference-chain/x/vesting/keeper/keeper.go`
- **Dependencies**: 1.2

#### **1.4 Implement Token Unlocking Logic**
- **Task**: `[ ]` Create the token unlocking function
- **What**: Create a keeper function that processes all vesting schedules. For each schedule, it should transfer the amount in the first element to the participant, remove the first element, and delete the schedule if it becomes empty.
- **Where**: `inference-chain/x/vesting/keeper/keeper.go`
- **Dependencies**: 1.2

#### **1.5 Integrate Unlocking into an EndBlocker**
- **Task**: `[ ]` Add an `EndBlocker` to the `x/vesting` module
- **What**: The `x/vesting` module needs its own `EndBlocker`. Once per day (approximated by a set number of blocks), this `EndBlocker` will call the token unlocking function from task 1.4.
- **Where**: `inference-chain/x/vesting/module/module.go`
- **Dependencies**: 1.4

### **Section 2: Integration with `x/inference` Module**

#### **2.1 Modify Reward Distribution**
- **Task**: `[ ]` Reroute reward payments to the vesting module
- **What**: Modify the reward distribution logic in the `x/inference` module. Instead of paying `Reward Coins` directly to the participant, it should now call the `AddVestedRewards` function on the `x/vesting` keeper. `Work Coins` should still be paid directly.
- **Where**: The primary location is `inference-chain/x/inference/keeper/msg_server_claim_rewards.go`, but other reward paths (like top miner rewards) must also be updated.
- **Dependencies**: 1.3

### **Section 3: Queries, Events, and CLI**

#### **3.1 Implement Query Endpoints**
- **Task**: `[ ]` Implement query endpoints
- **What**: Implement gRPC and REST endpoints to query a participant's vesting status (total vesting, detailed schedule, and total released).
- **Where**:
  - `inference-chain/x/vesting/types/query.proto`
  - `inference-chain/x/vesting/keeper/query_server.go`
- **Dependencies**: 1.2

#### **3.2 Implement Event Emitting**
- **Task**: `[ ]` Add event emitting
- **What**: Emit events when rewards are vested and when they are released (unlocked).
- **Where**:
  - `inference-chain/x/vesting/keeper/keeper.go` (in `AddVestedRewards` and the unlocking function)
- **Dependencies**: 1.3, 1.4

### **Section 4: Testing**

#### **4.1 Unit Tests**
- **Task**: `[ ]` Write unit tests for the `x/vesting` module
- **What**: Create unit tests covering the core logic: adding new rewards (including aggregation), unlocking tokens daily, and handling empty schedules.
- **Where**: `inference-chain/x/vesting/keeper/`

#### **4.2 Testermint E2E Tests**
- **Task**: `[ ]` Create a `VestingTests.kt` E2E test file
- **What**: Create end-to-end tests in a new `VestingTests.kt` file to verify the full lifecycle.
- **Where**: `testermint/src/test/kotlin/VestingTests.kt`
- **Scenarios**:
    1.  **Test Reward Vesting**: Verify that after a reward is claimed, a participant's spendable balance does *not* increase, but their vesting schedule is created correctly.
    2.  **Test Daily Unlocking**: Wait for a day's worth of blocks and verify that a portion of the vested amount is released to the participant's spendable balance.
    3.  **Test Reward Aggregation**: Give a participant one reward with a 180-day vest. Then, give them a second reward and verify it's correctly aggregated into the existing 180-day schedule without extending it.

### **Section 5: Network Upgrade**

#### **5.1 Create and Register Upgrade**
- **Task**: `[ ]` Implement the network upgrade
- **What**: Create and register a network upgrade handler named `v2_vesting` to activate the new module.
- **Logic**: The upgrade needs to add the store for the new `x/vesting` module. No parameter migrations are needed for this feature.
- **Where**:
    - `inference-chain/app/upgrades/v2_vesting/upgrades.go`
    - `inference-chain/app/upgrades.go` 