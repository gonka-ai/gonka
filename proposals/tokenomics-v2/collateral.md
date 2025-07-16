# Tokenomics V2 Proposal: Collateral for Network Weight

This document proposes an enhancement to the project's tokenomics by introducing a collateral mechanism. The goal is to strengthen network security and ensure that participants with significant weight and influence have a direct financial stake in the network's integrity.

## 1. Summary of Changes

This proposal introduces a system where network participants can deposit tokens as collateral to increase their "weight" in the network. This weight influences their role in governance processes, such as the unit of compute price calculation.

The existing Proof of Compute mechanism will remain the foundation for participation, but the ability to gain influence above a base level will be tied to this new collateral system. Collateral can be "slashed" (i.e., seized and burned) if a participant acts maliciously or fails to perform their duties.

## 2. Implementation Details

A new `x/collateral` module will be introduced to manage participant collateral. The `x/inference` module will be updated to interact with this new module, utilizing the collateral information to calculate participant weight and trigger slashing when necessary.

### 2.1. Collateral and Participant Weight

The core of this change is the modification of how a participant's weight is calculated. The system will move to a hybrid model that combines unconditional weight from Proof of Compute with additional weight that must be backed by collateral.

#### 2.1.1. Initial Grace Period

To encourage early adoption and minimize barriers to entry, there will be an initial grace period during which no collateral is required. This grace period will be controlled by a new governance-votable parameter, `GracePeriodEndEpoch`, with a proposed default of `180`. For all epochs up to and including `GracePeriodEndEpoch`, the `Base Weight Ratio` will be programmatically treated as `1.0` (100%), meaning all `Potential Weight` is granted unconditionally. After this epoch, the system will switch to using the governance-defined `Base Weight Ratio`.

#### 2.1.2. Standard Calculation Process

Here is the proposed calculation process, which becomes effective after the initial grace period:

1.  **Potential Weight Calculation**: First, based on a participant's Proof of Compute activities (work done, nonces delivered, etc.), the system calculates their total *Potential Weight*.

2.  **Base Weight**: A portion of this *Potential Weight* is granted unconditionally as a base share. This is determined by a **Base Weight Ratio**, a governance-votable parameter representing the percentage of weight that is collateral-free. It is proposed to have a default value of `0.2` (20%). The formula is:
    `Base Weight = Potential Weight * Base Weight Ratio`

3.  **Collateral-Eligible Weight**: The remaining portion of the *Potential Weight* is the *Collateral-Eligible Weight*:
    `Collateral-Eligible Weight = Potential Weight * (1 - Base Weight Ratio)`

4.  **Activating Additional Weight**: To activate this *Collateral-Eligible Weight*, the participant must have sufficient collateral deposited. The system will enforce a **Collateral Per Weight Unit** ratio, which will also be a parameter adjustable by governance. The amount of additional weight the participant receives is limited by the collateral they have provided.

5.  **Final Effective Weight**: The participant's final, effective weight used in governance and other network functions is the sum of their `Base Weight` and the `Activated Weight` backed by their collateral.

This new weight adjustment logic will be implemented in a new function within the `x/inference` module's keeper. This function will be called during the epoch transition process, specifically in the `onSetNewValidatorsStage` function, immediately after the initial `Potential Weight` has been calculated. It will iterate through the active participants, query the `x/collateral` module for each participant's active collateral, and then adjust their final `Effective Weight` based on the formulas described above. The `Participant` data structure in the `x/inference` module will not be modified; instead, the `x/collateral` module will maintain its own state mapping participant addresses to their collateral amounts.

### 2.2. Managing Collateral

The `x/collateral` module will be responsible for managing collateral deposits and withdrawals. It will expose two new messages:

*   `MsgDepositCollateral`: Allows a participant to send tokens from their spendable balance to be held as collateral within the `x/collateral` module.
*   `MsgWithdrawCollateral`: Allows a participant to initiate the return of their collateral.

#### 2.2.1. Module Parameters

The `x/collateral` module will introduce the following governance-votable parameter:
*   `UnbondingPeriodEpochs`: The number of epochs a withdrawal request must remain in the unbonding queue before being released. This provides flexibility to adjust the risk period for collateral. A default of `1` epoch is proposed.

#### 2.2.2. Withdrawal Unbonding and Release Cycle

To prevent abuse, withdrawals are not immediate. They are subject to an unbonding period that is tied to the network's epoch lifecycle. This ensures that collateral which was used to gain influence remains slashable for a period after the decision to withdraw is made.

The module will maintain a dual-indexed "unbonding queue" in its state to allow for efficient lookups by both `CompletionEpoch` (for processing releases) and by participant address (for queries and slashing).

Here is the detailed process:

1.  **Initiating a Withdrawal**: A participant sends a `MsgWithdrawCollateral`. The system reads the latest completed epoch number (`latestEpoch`) and calculates the `CompletionEpoch` for the withdrawal, which is `latestEpoch + params.UnbondingPeriodEpochs`. The request is then placed in the unbonding queue.

2.  **State During Unbonding**: Once a withdrawal is initiated:
    *   The requested amount is immediately removed from the participant's *active* collateral and no longer contributes to their `Effective Weight` calculation for future epochs.
    *   The funds are held in the `x/collateral` module's unbonding queue and remain fully slashable. If a slashing event occurs, the unbonding amount is penalized first.

3.  **Handling Multiple Withdrawals**: The system will support multiple pending withdrawals for a single participant. If a participant submits a new withdrawal request while a previous one is already in the unbonding queue for the same target `CompletionEpoch`, the amounts will be aggregated.

4.  **Processing and Releasing Funds**: The actual release of funds is tied to the epoch lifecycle to ensure they are held for a full period of risk.
    *   A withdrawal initiated during `latestEpoch` is eligible for release only after the `onSetNewValidators` stage of its calculated `CompletionEpoch`.
    *   The `x/collateral` module will process its unbonding queue at the end of each epoch. It will have a mechanism to efficiently query all withdrawals scheduled for release in the current epoch and move the funds back to the participants' spendable balances.

These messages will be defined in `inference-chain/proto/inference/collateral/tx.proto`, and their logic will be implemented in the keeper of the `x/collateral` module. The `x/collateral` module will hold the collateralized funds in a module account.

### 2.3. Slashing Conditions

Slashing will be initiated by the `x/inference` module, but executed by the `x/collateral` module. The `x/collateral` module will expose a `Slash` function that other modules can call. When a slash is triggered, the penalty will be applied **proportionally** to both the participant's active collateral and any funds they have in the unbonding queue.

The `x/inference` module will introduce new governance parameters to control the severity of slashing and the collateral-to-weight ratio:
*   `BaseWeightRatio`: The portion of a participant's `PotentialWeight` that is granted unconditionally, without collateral backing. Proposed default: `0.2` (20%).
*   `CollateralPerWeightUnit`: The amount of collateral (in the native token) required to activate one unit of `Collateral-Eligible Weight`.
*   `SlashFractionInvalid`: The percentage of a participant's total collateral to be slashed when they are marked as `INVALID`. Proposed default: `0.20` (20%).
*   `SlashFractionDowntime`: The percentage of a participant's total collateral to be slashed for failing to meet participation requirements in an epoch. Proposed default: `0.10` (10%).
*   `DowntimeMissedPercentageThreshold`: The epoch performance threshold that triggers a downtime slash. If a participant's missed request percentage for an epoch exceeds this value, their collateral will be slashed. Proposed default: `0.05` (5%).

#### 2.3.1. Malicious Behavior (Marked as `INVALID`)

The most severe penalty is reserved for participants whose work is consistently proven to be incorrect. A participant is marked as `INVALID` only when their failure rate becomes statistically significant.

*   **Trigger**: The slash is triggered at the exact moment a participant's status is changed to `INVALID`. This check occurs after the participant's failure statistics are updated, ensuring the penalty is applied only when the threshold is crossed. This logic will be implemented in:
    1.  `msg_server_invalidate_inference.go`: When an authority directly invalidates a result.
    2.  `msg_server_validation.go`: When a peer validation vote confirms a failure.
*   **Action**: Upon the status change, the `x/inference` module will call the `x/collateral` module's `Slash` function, using the `SlashFractionInvalid` parameter to determine the penalty amount.

#### 2.3.2. Failure to Participate (Downtime)

The network relies on active participation. Participants who fail to meet performance standards for an epoch will have a portion of their collateral slashed.

*   **Trigger**: This check is performed **once per epoch**. The logic resides within the `onSetNewValidatorsStage` function in `inference-chain/x/inference/module/module.go`, which is called from the module's `EndBlocker` when an epoch concludes. A slash is triggered if a participant's performance (e.g., missed request percentage) for the epoch crosses a governance-defined threshold.
*   **Action**: The `x/inference` module will call the `x/collateral` module's `Slash` function, using the `SlashFractionDowntime` parameter to determine the penalty.

This separation of concerns ensures that `x/inference` is responsible for defining *what* constitutes a slashable offense, while `x/collateral` is responsible for the financial operation of *executing* the slash.

### 2.4. Collateral Requirements for Proof of Compute

The weight a participant gains from Proof of Compute is directly tied to the number of nonces they successfully deliver. To ensure this weight is backed by a financial stake, the system requires collateral for any weight beyond a base level.

The `CollateralPerWeightUnit` parameter, set by governance, defines how much collateral (in the native token) is required for each unit of `Collateral-Eligible Weight`. Since a participant's `Potential Weight` is a function of their nonces, this parameter effectively sets the collateral requirement per nonce.

For example, a target could be set for a high-performance GPU like an H100, which is expected to produce approximately 1600 nonces per epoch. To fully back the collateral-eligible portion of its weight (assuming an 80% collateral requirement), a participant might be required to post 100 gonka coins. This would establish a `CollateralPerWeightUnit` equivalent to `0.0625` gonka per nonce (`100 gonka / 1600 nonces`). This ensures that as a participant's influence via Proof of Compute grows, so does their financial commitment to the network's integrity.

## 3. Integration with the Staking Module via Hooks

To ensure that consensus-level penalties are reflected in the application-specific collateral system, the new `x/collateral` module will integrate with the `x/staking` and `x/slashing` modules. It will implement the `StakingHooks` interface and register itself with the staking keeper to listen for validator state changes.

**Validator-to-Participant Mapping**: The `x/staking` module operates on validator operator addresses (`gonkavaloper...`). The `x/inference` module maintains a mapping between these operator addresses and the main participant account addresses (`gonka...`) which hold the collateral. When a hook is triggered with a validator operator address, the `x/collateral` module will use this existing mapping to find the correct participant account to slash.

This allows the `x/collateral` module to react to consensus-level faults and apply its own penalties in sync with the core protocol.

The following hooks will be implemented in the `x/collateral` module:

### 3.1. `BeforeValidatorSlashed`
*   **Trigger**: Called by the `x/staking` keeper after a validator has been confirmed to have committed a liveness (downtime) or Byzantine (double-signing) fault, but *before* the state change is finalized.
*   **Action for Collateral Module**:
    1.  The hook receives the `ConsensusAddress` of the punished validator and the `slashFraction`.
    2.  The `x/collateral` module will use the `ConsensusAddress` to look up the participant.
    3.  If a participant is found with collateral, the module will immediately slash both their active and unbonding collateral by the same `slashFraction`.
    4.  This ensures that consensus-level faults result in the burning of real collateral from the `x/collateral` module, maintaining network security.

### 3.2. `AfterValidatorBeginUnbonding`
*   **Trigger**: Called by the `x/staking` keeper the moment a validator's status changes from `BONDED` to `UNBONDING`. This happens when a validator is jailed for any reason or is kicked from the active set for having low power.
*   **Action for Collateral Module**:
    1.  The `x/collateral` module will look up the participant associated with the validator.
    2.  If found, the module can mirror this state change. For instance, it could prevent the participant from depositing more collateral or prevent them from using their collateral to gain weight until they become active again.
    3.  This hook serves as a signal that the participant is inactive at the consensus level.

### 3.3. `AfterValidatorBonded`
*   **Trigger**: Called by the `x/staking` keeper whenever a validator enters the `BONDED` state. This occurs when a previously jailed validator is un-jailed and has enough power to rejoin the active set.
*   **Action for Collateral Module**:
    1.  The `x/collateral` module will look up the participant associated with the validator.
    2.  If found, the module can mark their collateral as fully active again.
    3.  This hook signals that the participant is once again an active and trusted part of the consensus set, and their collateral can be used to its full effect.

## 4. Queries, Events, and CLI

To ensure transparency and facilitate interaction, the `x/collateral` module will include a comprehensive set of queries, events, and CLI commands.

### 4.1. Queries

The module will expose gRPC and REST query endpoints to retrieve information about:
*   A specific participant's active and unbonding collateral.
*   All unbonding collateral for a given epoch.
*   The current `x/collateral` module parameters.

### 4.2. Events

The module will emit events for all significant state changes, including:
*   `EventDepositCollateral(participant, amount)`
*   `EventInitiateWithdrawal(participant, amount, completion_epoch)`
*   `EventProcessWithdrawal(participant, amount)`
*   `EventSlashCollateral(participant, amount_slashed)`

These events are essential for block explorers, indexers, and other off-chain services to track collateral movements.

### 4.3. CLI Commands

The module will provide CLI commands for:
*   Depositing and withdrawing collateral.
*   Querying participant collateral balances and module parameters.

## 5. Network Upgrade Plan

Activating the collateral system requires a coordinated network upgrade. The upgrade process will be managed by the `x/upgrade` module and will perform two critical functions:

1.  **Create New `x/collateral` Module Store**: The upgrade will be configured to add a new store to the blockchain's state for the `x/collateral` module. This is where all collateral balances and unbonding queues will be stored.
2.  **Migrate `x/inference` Parameters**: The upgrade handler will execute a one-time migration of the `x/inference` module's parameters. It will read the existing parameters from the store, add the new `BaseWeightRatio`, `CollateralPerWeightUnit`, `SlashFractionInvalid`, `SlashFractionDowntime`, and `DowntimeMissedPercentageThreshold` parameters with their defined default values, and save the updated parameter structure back to the store.

This ensures that upon upgrade, the new module is ready and all existing modules have the necessary parameters to support the collateral and slashing features. 