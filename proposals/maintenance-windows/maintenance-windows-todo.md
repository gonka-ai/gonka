# Mid-Epoch Participant Maintenance Windows - Task Plan

## Prerequisite Reading

Before starting implementation, please read the following documents to understand the full context of the changes:
- The main proposal: `proposals/maintenance-windows/maintenance-windows.md`
- The app wiring for staking/slashing ordering: `inference-chain/app/app_config.go`
- The inference epoch transition and validator-set integration: `inference-chain/x/inference/module/module.go`
- The collateral staking hooks: `inference-chain/x/collateral/module/hooks.go`
- The participant model and reward flow in `x/inference`
- The maintained Cosmos SDK fork branch: `https://github.com/gonka-ai/cosmos-sdk/tree/release/v0.53.x`

## How to Use This Task List

### Workflow
- **Focus on a single task**: Please work on only one task at a time to ensure clarity and quality. Avoid implementing parts of future tasks.
- **Request a review**: Once a task's implementation is complete, change its status to `[?] - Review` and wait for confirmation.
- **Update all usages**: If a function, proto field, or type is renamed, find and update all references throughout the codebase.
- **Build after each task**: After each task is completed, build the project to ensure there are no compilation errors.
- **Test after each section**: After completing all tasks in a section, run the corresponding tests to verify the functionality.
- **Wait for completion**: After review is confirmed, mark the task as `[x] - Finished`, add a **Result** section summarizing the changes, and then move on to the next one.

### Build & Test Commands
- **Build Inference Chain**: From the project root, run `make node-local-build`
- **Build API Node**: From the project root, run `make api-local-build`
- **Run Inference Chain Unit Tests**: From the project root, run `make node-test`
- **Run API Node Unit Tests**: From the project root, run `make api-test`
- **Generate Proto Go Code**: When modifying proto files, run `ignite generate proto-go` in the `inference-chain` folder
- **Cosmos SDK Fork Tests**: Run targeted tests in the maintained Cosmos SDK fork for `x/slashing` and any touched staking/liveness code

### Status Indicators
- `[ ]` **Not Started** - Task has not been initiated
- `[~]` **In Progress** - Task is currently being worked on
- `[?]` **Review** - Task completed, requires review/testing
- `[x]` **Finished** - Task completed and verified

### Task Organization
Tasks are organized by implementation area and numbered for easy reference. Dependencies are noted where critical. Complete tasks in order unless a task explicitly says it can be done in parallel.

### Task Format
Each task includes:
- **What**: Clear description of work to be done
- **Where**: Specific files/locations to modify
- **Why**: Brief context of purpose when not obvious

## Task List

### Section 1: Maintenance Data Model and API Surface

#### 1.1 Define Maintenance Proto Messages and Queries
- **Task**: [?] Add maintenance-window message and query definitions
- **What**:
  - Define the message(s) and query surface for maintenance windows:
    - `MsgScheduleMaintenance`
    - `MsgCancelMaintenance`
    - Query for current maintenance credit
    - Query for scheduled maintenance windows
    - Query for active maintenance windows
    - Query for participant maintenance status
    - Query for scheduling availability (`could I schedule this window now?`)
  - Define request/response types with participant terminology
  - Keep scheduling-availability query explicit so operators can preflight concurrency before sending a tx
- **Where**:
  - `inference-chain/proto/inference/inference/tx.proto`
  - `inference-chain/proto/inference/inference/query.proto`
  - Generated Go files under `inference-chain/x/inference/types/`
- **Why**: The feature needs a first-class protocol API before keeper logic or testing can be implemented
- **Dependencies**: None

#### 1.2 Add Maintenance State Types
- **Task**: [?] Define maintenance reservation state and per-participant `MaintenanceState`
- **What**:
  - Add a `MaintenanceReservation` state type with:
    - reservation ID
    - participant
    - start height
    - duration
    - created by
    - reservation status
    - optional advisory activation-time warning / violation metadata
  - Add a dedicated per-participant `MaintenanceState`, keyed by participant address, containing at least:
    - maintenance credit in blocks
    - last epoch in which maintenance was activated
    - currently active reservation ID, if any
    - next scheduled reservation ID, if any
  - Add a dedicated transition schedule keyed by exact block height, storing:
    - block height
    - reservation ID
    - transition type
  - Add a dedicated scheduling-overlap index keyed by reservation start height
  - Ensure all participants begin with zero credit from feature introduction onward
  - Add validation helpers and enums/status constants as needed
- **Where**:
  - `inference-chain/proto/inference/inference/*.proto`
  - `inference-chain/x/inference/types/`
  - `inference-chain/x/inference/keeper/`
- **Why**: The updated proposal now prefers a dedicated maintenance state object, separate from the hot participant object, while avoiding multiple fragmented per-participant maintenance buckets
- **Dependencies**: 1.1

#### 1.3 Add Governance Parameters
- **Task**: [?] Introduce maintenance parameter group inside global inference params
- **What**:
  - Add a maintenance parameter group to global params with at least:
    - enable/disable flag
    - minimum schedule lead blocks
    - max single window blocks
    - max concurrent participants
    - max concurrent power
    - credit cap
    - credit earned per successful epoch
  - Add validation, defaults, and governance update support
- **Where**:
  - `inference-chain/proto/inference/inference/params.proto`
  - `inference-chain/x/inference/types/params.go`
  - `inference-chain/x/inference/keeper/params*.go`
  - Any upgrade/default-genesis paths required
- **Why**: Nearly every policy decision in the proposal is governance-controlled
- **Dependencies**: 1.2

#### 1.4 Add Keeper Storage for Reservations
- **Task**: [?] Implement maintenance reservation persistence and indexes
- **What**:
  - Add keeper state for:
    - reservation primary storage by reservation ID
    - per-participant `MaintenanceState`
    - exact-height transition schedule for `BeginBlock` lifecycle execution only
    - reservation start-height overlap index for scheduling-time range scans only
  - Use direct keyed Cosmos SDK collections lookups for all primary access paths
  - Do not rely on broad iteration over reservations or unrelated participant state in `BeginBlock`
  - Be explicit about the intended storage shape. Target layout:
    - `Map[reservation_id] -> MaintenanceReservation`
    - `Map[participant_address] -> MaintenanceState`
    - `Map[(block_height, reservation_id)] -> transition_type`
    - `Map[(start_height, reservation_id)] -> reservation_id` or equivalent start-height index entry for bounded overlap checks
  - Keep pruning out of the first implementation, but structure storage so pruning can be added later
- **Where**:
  - `inference-chain/x/inference/keeper/`
  - `inference-chain/x/inference/types/keys.go` or collections schema files
- **Why**: Fast lookup is required both in slashing-path checks and in assignment suppression logic
- **Dependencies**: 1.2

#### 1.5 Add Reservation Lifecycle State Machine
- **Task**: [?] Implement reservation lifecycle transitions in `BeginBlock`
- **What**:
  - Add a maintenance lifecycle state machine that transitions:
    - `Scheduled -> Active` when `block_height == start_height`
    - `Active -> Completed` when `block_height == start_height + duration_blocks`
  - Ensure lifecycle transitions happen in a deterministic block-driven path
  - Implement the exact `BeginBlock` access pattern:
    - one lookup for transition rows at the exact current block height
    - iterate only the rows returned for that exact height
    - one direct reservation lookup per returned row
    - apply transition
    - update the participant's `MaintenanceState` active/scheduled reservation references as needed
    - delete consumed transition row
  - Do not use `<= current_height` scans or any inequality-based reservation search in the critical path
- **Where**:
  - `inference-chain/x/inference/module/`
  - `inference-chain/x/inference/keeper/`
  - App/module wiring as needed
- **Why**: The proposal now explicitly requires a block-driven lifecycle owner; status changes cannot be implicit
- **Dependencies**: 1.4

### Section 2: Scheduling and Credit Logic

#### 2.1 Implement Scheduling Validation
- **Task**: [?] Implement `MsgScheduleMaintenance` validation and execution
- **What**:
  - Validate:
    - caller is participant or AuthZ delegate
    - sufficient lead time
    - duration within limits
    - enough maintenance credit
    - participant has no other future scheduled maintenance window already recorded in `MaintenanceState`
    - no overlap for same participant
    - concurrency limits satisfied at scheduling time
    - no overlap with restricted PoC commit / exchange phase
    - no overlap with restricted DKG phase
  - Deduct reserved maintenance credit when the reservation is accepted
  - Persist the reservation in scheduled state
  - Persist the participant's `next_scheduled_reservation_id`
  - Add the exact start-height and exact transition-height index entries required for later lookups
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server*.go`
  - New maintenance-specific keeper files
- **Why**: Scheduling is the main user action and the point where concurrency policy is enforced
- **Dependencies**: 1.4

#### 2.2 Implement Cancellation Logic
- **Task**: [?] Implement `MsgCancelMaintenance`
- **What**:
  - Allow cancellation only while reservation is still scheduled
  - Restore unspent maintenance credit
  - Enforce AuthZ / participant authorization
  - Emit clear events/logs
- **Where**:
  - `inference-chain/x/inference/keeper/msg_server*.go`
  - New maintenance-specific keeper files
- **Why**: Cancellation is part of the agreed feature semantics
- **Dependencies**: 2.1

#### 2.3 Implement Scheduling-Availability Query
- **Task**: [?] Add preflight schedulability query
- **What**:
  - Add a query that takes proposed participant, start height, and duration
  - Return whether the window is schedulable under current rules
  - Include enough detail to tell the caller why it would fail:
    - insufficient credit
    - lead time failure
    - participant already has a scheduled maintenance window
    - overlap
    - concurrent participant cap
    - concurrent power cap
  - Use the same scheduling-time concurrency logic as `MsgScheduleMaintenance`
  - Include epoch-phase conflict answers for PoC commit / exchange and DKG overlap
- **Where**:
  - `inference-chain/x/inference/keeper/grpc_query*.go`
  - `inference-chain/proto/inference/inference/query.proto`
- **Why**: The proposal explicitly calls out avoiding wasted gas and failed tx construction
- **Dependencies**: 2.1

#### 2.4 Implement Credit Earning in Reward Claim Flow
- **Task**: [?] Grant maintenance credit during successful reward claim
- **What**:
  - Identify the reward-claim path
  - Add maintenance-credit accrual there
  - Use the rule: if the participant would normally receive epoch rewards and successfully claims them, also grant the configured maintenance-credit amount
  - Do not grant maintenance credit if maintenance was activated for that participant in the claimed epoch
  - Enforce cap
  - Make sure no retroactive credit is minted
- **Where**:
  - Reward claim / settlement logic under `inference-chain/x/inference/keeper/`
  - Maintenance credit / maintenance epoch-usage persistence logic
- **Why**: The proposal resolves credit earning as part of reward claim rather than a separate epoch transition pass
- **Dependencies**: 1.3

#### 2.5 Mark Maintenance Usage Per Epoch
- **Task**: [?] Record maintenance activation usage for the current epoch
- **What**:
  - When a reservation transitions to active, mark that the participant used maintenance in that epoch
  - Ensure the reward-claim path can check this cheaply
  - Keep the state layout minimal and directly keyed
- **Where**:
  - `inference-chain/x/inference/keeper/`
  - Lifecycle transition path in module begin block
- **Why**: Credit suppression for maintenance-used epochs depends on a deterministic epoch-usage marker
- **Dependencies**: 1.5

#### 2.6 Implement Bounded Overlap Search
- **Task**: [?] Implement bounded scheduling-overlap lookup
- **What**:
  - Use the reservation start-height overlap index, not the `BeginBlock` transition schedule
  - Use the `max_window_blocks` parameter to bound overlap search
  - For a candidate interval `[start, end]`, range-scan only reservation start heights in the bounded interval needed to detect overlaps
  - Confirm actual overlap by reading the referenced reservations and comparing computed end heights
  - Do not perform an unbounded full-state scan for scheduling checks
- **Where**:
  - `inference-chain/x/inference/keeper/`
  - Scheduling validation and scheduling-availability query paths
- **Why**: Scheduling is not as latency-sensitive as `BeginBlock`, but overlap checks still need an explicit bounded strategy rather than hand-wavy iteration
- **Dependencies**: 1.4

### Section 3: Consensus Liveness Exemption in Cosmos SDK Fork

#### 3.1 Patch Slashing Liveness Path
- **Task**: [?] Add maintenance-aware liveness exemption to the maintained Cosmos SDK fork
- **What**:
  - Identify the exact liveness handling path in the forked `x/slashing`
  - Add a maintenance-window check so active maintenance:
    - freezes missed-signature accounting
    - prevents downtime jailing
    - prevents downtime slashing
  - Leave double-sign and evidence handling unchanged
- **Where**:
  - Maintained Cosmos SDK fork branch `release/v0.53.x`
  - Likely `x/slashing/abci.go` and/or `x/slashing/keeper/infractions.go`
- **Why**: Hook-only logic is not sufficient; protocol-level liveness enforcement must be patched directly
- **Dependencies**: 1.4

#### 3.2 Wire Maintenance State into Slashing Checks
- **Task**: [?] Define the lookup boundary from SDK liveness code into chain maintenance state
- **What**:
  - Decide and implement how the slashing path checks whether a participant is in active maintenance
  - Keep the lookup deterministic and cheap enough for begin-block usage
  - Ensure height-based semantics match the proposal exactly
  - Ensure the lookup path is direct-key based and does not require iteration
- **Where**:
  - Maintained Cosmos SDK fork
  - Inference-chain app wiring if interfaces or keeper plumbing are required
- **Why**: The maintenance exemption must be visible from the liveness path without ambiguous state access
- **Dependencies**: 3.1

#### 3.3 Add Defensive Hook Guards
- **Task**: [?] Update collateral/staking-hook logic as defense in depth
- **What**:
  - Add secondary protection so maintenance-covered participants do not get collateral-side downtime fallout if slashing-related hooks fire unexpectedly
  - Document clearly that this is secondary protection, not the primary enforcement mechanism
- **Where**:
  - `inference-chain/x/collateral/module/hooks.go`
  - Related collateral keeper logic if needed
- **Why**: The proposal explicitly calls out hooks as defense in depth
- **Dependencies**: 3.1

#### 3.4 Add Activation-Time Advisory Re-Check
- **Task**: [?] Re-check concurrency caps at activation time and persist advisory warnings
- **What**:
  - During `Scheduled -> Active` transition, re-check concurrent participant count and power against current params
  - If current caps would now reject the reservation:
    - activate anyway
    - emit warning event/log
    - persist advisory warning / violation metadata on the reservation for queries
  - Do not hard-cancel at activation time
  - Update `MaintenanceState` so the active reservation reference is set even when activation carries an advisory warning
- **Where**:
  - `inference-chain/x/inference/module/`
  - `inference-chain/x/inference/keeper/`
  - Query/event paths as needed
- **Why**: The proposal now grandfather-scheduled windows while still surfacing governance drift at activation time
- **Dependencies**: 1.5

### Section 4: Inference-Chain Duty Exemptions

#### 4.1 Suppress Random Inference Assignment
- **Task**: [?] Remove active-maintenance participants from new random assignment
- **What**:
  - Identify the assignment path(s) for new inference work
  - Exclude participants with active maintenance from random assignment immediately at window start
  - Keep participants in epoch groups; do not mutate epoch membership
- **Where**:
  - `inference-chain/x/inference/module/`
  - `inference-chain/x/inference/keeper/`
- **Why**: The proposal requires immediate assignment exclusion without epoch-group removal
- **Dependencies**: 2.1

#### 4.1a Enforce Epoch-Critical Phase Scheduling Rejections
- **Task**: [?] Reject maintenance windows that overlap PoC commit / exchange or DKG phases
- **What**:
  - Compute overlap against the current scheduling target epoch(s)
  - Reject windows overlapping:
    - PoC commit / exchange window
    - DKG phase
  - Introduce explicit scheduling errors for both cases
- **Where**:
  - `inference-chain/x/inference/keeper/`
  - Maintenance scheduling validation logic
- **Why**: These overlaps can silently break next-epoch participation or next-epoch start safety
- **Dependencies**: 2.1

#### 4.2 Waive Maintenance-Covered Inference Penalties
- **Task**: [?] Suppress downtime and expiry penalties during active maintenance
- **What**:
  - Identify downtime and expiry penalty paths
  - Add maintenance checks so active windows waive these penalties
  - Keep existing semantics once maintenance ends
  - Be explicit about the still-open question of in-flight inference treatment
- **Where**:
  - `inference-chain/x/inference/keeper/`
  - `inference-chain/x/inference/module/inference_expiry*.go`
  - Related participant status / collateral integration files
- **Why**: Penalty exemption is a core part of the feature promise
- **Dependencies**: 2.1

#### 4.3 Suppress Validation Duties
- **Task**: [?] Exempt active-maintenance participants from validation duties
- **What**:
  - Identify where validation obligations are assigned or enforced
  - Exclude maintenance-covered participants from validation duty expectations
  - Ensure no follow-on penalties are applied due solely to maintenance-covered absence
- **Where**:
  - `inference-chain/app/ante_validation.go`
  - `inference-chain/x/inference/module/`
  - `inference-chain/x/inference/keeper/`
- **Why**: The proposal now explicitly includes validation-duty exemption
- **Dependencies**: 4.2

#### 4.4 Suppress Confirmation PoC Duties
- **Task**: [?] Exempt active-maintenance participants from CPoC duties
- **What**:
  - Identify where Confirmation PoC participation is expected and where its penalties or qualification logic are applied
  - Exclude maintenance-covered participants from CPoC expectations during active windows
  - Ensure maintenance does not accidentally create false CPoC failures
- **Where**:
  - `inference-chain/x/inference/module/confirmation_poc.go`
  - Related CPoC keeper/module code
- **Why**: This was added explicitly during proposal review and must be included from the beginning
- **Dependencies**: 4.2

### Section 5: Queries, Events, and Observability

#### 5.1 Implement Maintenance Queries
- **Task**: [?] Implement all agreed query endpoints
- **What**:
  - Current credit
  - Scheduled windows
  - Active windows
  - Participant maintenance status
  - Concurrent reserved participant count/power
  - Scheduling availability
  - Reservation advisory activation-time warning / violation status
  - Consider whether participant maintenance status should surface the current active reservation ID and next scheduled reservation ID directly from `MaintenanceState`
- **Where**:
  - `inference-chain/x/inference/keeper/grpc_query*.go`
  - Proto-generated query files
- **Why**: The proposal relies on these queries for operator usability and debugging
- **Dependencies**: Section 2

#### 5.2 Add Events and Structured Logs
- **Task**: [?] Add maintenance-specific logging and events
- **What**:
  - Emit logs/events for schedule, cancel, activate, complete
  - Emit logs/events for liveness exemption application and duty suppression
  - Emit logs/events for activation-time concurrency warnings
  - Use the repo’s standard logging style
- **Where**:
  - `inference-chain/x/inference/keeper/`
  - `inference-chain/x/inference/module/`
  - Cosmos SDK fork if slashing-side logs are added
- **Why**: Maintenance behavior needs to be observable during development and production operations
- **Dependencies**: Section 4

### Section 6: Upgrade and Genesis Handling

#### 6.1 Add Upgrade Path for Maintenance Credit State and Params
- **Task**: [?] Add upgrade handling for maintenance feature introduction
- **What**:
  - Initialize maintenance credit to zero for all existing participants
  - Add new parameters with defaults
  - Initialize maintenance epoch-usage state as empty
  - Ensure feature introduction is clean for existing chains
- **Where**:
  - `inference-chain/app/upgrades*.go`
  - Any migration or default-genesis code required
- **Why**: The proposal explicitly says participants start at zero and only earn credit after feature introduction
- **Dependencies**: 1.3

#### 6.2 Verify Genesis / Export Behavior
- **Task**: [?] Verify maintenance state exports and imports cleanly
- **What**:
  - Add reservation export/import if required
  - Verify `MaintenanceState` is included correctly
  - Confirm no unexpected interaction with existing export/reset logic
- **Where**:
  - `inference-chain/app/export.go`
  - `inference-chain/x/inference/module/genesis.go`
  - Related genesis files
- **Why**: New state should survive export/import and testnet resets predictably
- **Dependencies**: 6.1

### Section 7: Testing

#### 7.1 Unit Tests for Maintenance Scheduling and Credit
- **Task**: [?] Add unit tests for maintenance keeper and message flows
- **What**:
  - Schedule success/failure cases
  - Cancel success/failure cases
  - Credit cap and deduction behavior
  - Reward-claim credit accrual behavior
  - No credit accrual in maintenance-used epochs
  - Scheduling-availability query correctness
  - Concurrency cap behavior by participant count and power
  - Reservation advisory warning persistence
  - Restricted PoC / DKG overlap rejection
- **Where**:
  - `inference-chain/x/inference/keeper/*test.go`
- **Why**: Core logic needs strong unit-level confidence before E2E work
- **Dependencies**: Sections 1-2

#### 7.2 Unit Tests for Duty Exemptions
- **Task**: [?] Add unit tests for assignment and penalty suppression
- **What**:
  - No new inference assignment during active maintenance
  - No downtime/expiry penalty during active maintenance
  - Validation-duty exemption
  - CPoC-duty exemption
  - Resume behavior immediately after window ends
- **Where**:
  - `inference-chain/x/inference/module/*test.go`
  - `inference-chain/x/inference/keeper/*test.go`
- **Why**: These behaviors are central user-facing guarantees
- **Dependencies**: Section 4

#### 7.3 Cosmos SDK Fork Tests
- **Task**: [?] Add targeted tests in forked slashing/liveness code
- **What**:
  - Maintenance active: misses do not count
  - Maintenance active: no downtime jail/slash
  - Maintenance inactive: normal liveness enforcement still works
  - Double-sign/evidence paths unaffected
  - Resume behavior after maintenance end
  - Activation-time advisory warning path does not affect liveness exemption correctness
- **Where**:
  - Maintained Cosmos SDK fork under `x/slashing/...`
- **Why**: The most safety-critical change is in the protocol liveness path
- **Dependencies**: Section 3

#### 7.4 Testermint End-to-End Coverage
- **Task**: [?] Add end-to-end maintenance-window tests in Testermint
- **What**:
  - Schedule a maintenance window
  - Pause participant execution for the covered interval
  - Verify:
    - no jail during maintenance
    - no maintenance-covered assignments/duties
    - no maintenance-covered penalties
    - normal behavior resumes after maintenance ends
  - Include at least one test touching validation/CPoC expectations
  - Include coverage that maintenance cannot be scheduled across restricted PoC / DKG phases
- **Where**:
  - `testermint/`
  - Any orchestration scripts needed to pause participant execution cleanly
- **Why**: The proposal explicitly calls out end-to-end testing as a major implementation risk and a requirement for confidence
- **Dependencies**: Sections 3-4

### Section 8: Deferred / Follow-Up Items

#### 8.1 In-Flight Inference Semantics
- **Task**: [?] Define and implement treatment of in-flight inferences
- **What**:
  - Decide whether maintenance start cancels in-flight work, allows grace handling, or uses a hybrid rule
  - Update proposal and implementation accordingly
- **Where**:
  - Proposal document
  - Inference assignment / expiry / execution code
- **Why**: This remains an explicit open issue in the proposal
- **Dependencies**: Section 4

#### 8.2 Subnet Interaction Design
- **Task**: [?] Specify how maintenance windows interact with subnets
- **What**:
  - Review subnet-specific duties and assumptions as that feature develops
  - Decide whether maintenance affects subnet scheduling, duties, or settlement logic
  - Update proposal and implementation plan when subnet semantics are clearer
- **Where**:
  - Proposal document
  - Subnet-related code under `inference-chain/x/inference/keeper/`
- **Why**: The proposal explicitly leaves subnet interaction open
- **Dependencies**: None

#### 8.3 Reservation Pruning
- **Task**: [?] Add pruning for completed/canceled maintenance reservations
- **What**:
  - Add retention and pruning strategy for historical reservations
  - Keep this out of the critical path unless state growth becomes noticeable
- **Where**:
  - Maintenance keeper state management
- **Why**: Nice-to-have follow-up item, not required for the first version
- **Dependencies**: Section 1
