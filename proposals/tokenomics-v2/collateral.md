# Tokenomics V2 Proposal: Collateral for Network Weight

This document proposes an enhancement to the project's tokenomics by introducing a collateral mechanism. The goal is to strengthen network security and ensure that participants with significant weight and influence have a direct financial stake in the network's integrity.

## 1. Summary of Changes

This proposal introduces a system where network participants can deposit tokens as collateral to increase their "weight" in the network. This weight influences their role in governance processes, such as the unit of compute price calculation.

The existing Proof of Contribution (PoC) mechanism will remain the foundation for participation, but the ability to gain influence above a base level will be tied to this new collateral system. Collateral can be "slashed" (i.e., seized and burned) if a participant acts maliciously or fails to perform their duties.

## 2. Implementation Details

These changes will be integrated directly into the existing `x/inference` module.

### 2.1. Collateral and Participant Weight

The core of this change is the modification of how a participant's weight is calculated. The system will move to a hybrid model that combines unconditional weight from Proof of Contribution (PoC) with additional weight that must be backed by collateral.

Here is the proposed calculation process:

1.  **Potential Weight Calculation**: First, based on a participant's PoC activities (work done, nonces delivered, etc.), the system calculates their total *Potential Weight*.

2.  **Base Weight**: A portion of this *Potential Weight* is granted unconditionally. This is determined by a **Base Weight Ratio**, a parameter that can be adjusted by on-chain governance (e.g., initially set to 20%). The formula is:
    `Base Weight = Potential Weight * Base Weight Ratio`

3.  **Collateral-Eligible Weight**: The remaining portion of the *Potential Weight* is the *Collateral-Eligible Weight*:
    `Collateral-Eligible Weight = Potential Weight * (1 - Base Weight Ratio)`

4.  **Activating Additional Weight**: To activate this *Collateral-Eligible Weight*, the participant must have sufficient collateral deposited. The system will enforce a **Collateral Per Weight Unit** ratio, which will also be a parameter adjustable by governance. The amount of additional weight the participant receives is limited by the collateral they have provided.

5.  **Final Effective Weight**: The participant's final, effective weight used in governance and other network functions is the sum of their `Base Weight` and the `Activated Weight` backed by their collateral.

This new calculation logic will be implemented in the `getWeight` function, which is currently referenced in `inference-chain/x/inference/epochgroup/unit_of_compute_price.go`. The `Participant` data structure, defined in `inference-chain/x/inference/types/participant.proto`, will be updated to include a field for the participant's `CollateralAmount`.

### 2.2. Managing Collateral

Two new messages will be introduced to manage collateral deposits and withdrawals:

*   `MsgDepositCollateral`: Allows a participant to send tokens from their spendable balance to be held as collateral by the `x/inference` module.
*   `MsgWithdrawCollateral`: Allows a participant to request the return of their collateral. To prevent abuse, withdrawals will be subject to an "unbonding period" (e.g., 28 days), during which the funds are still slashable but cannot be used as collateral to gain weight.

These messages will be defined in `inference-chain/x/inference/types/tx.proto` and their logic implemented in new keeper files within `inference-chain/x/inference/keeper/`.

### 2.3. Slashing Conditions

Collateral is the economic guarantee of good behavior. It will be slashed under two conditions:

1.  **Malicious Behavior**: If a participant is caught cheating (e.g., submitting a fraudulent inference result), a significant portion of their collateral will be slashed immediately. This logic will be integrated into the parts of the code that already handle inference validation, such as the logic within `inference-chain/x/inference/keeper/msg_server_finish_inference.go`.

2.  **Failure to Participate (Downtime)**: The network relies on active participation. If a participant fails to meet their duties for a certain period (e.g., has significant downtime and fails to participate in epochs), a small, predefined portion of their collateral will be slashed. This check will be implemented in the `EndBlocker` of the `x/inference` module, located in `inference-chain/x/inference/module/module.go`.

Slashing ensures that only active, honest, and reliable participants can maintain significant weight and influence within the network. 

## 3. Integration with the Staking Module via Hooks

While the `x/inference` module will manage its own collateral and application-specific slashing logic, it must also react to consensus-level faults detected by the `x/slashing` module (e.g., a validator double-signing). To achieve this, the module will implement the `StakingHooks` interface and register itself with the staking keeper.

This allows the collateral module to be notified of critical validator state changes and apply its own financial penalties in sync with the core consensus penalties.

The following hooks will be implemented:

### 3.1. `BeforeValidatorSlashed`
*   **Trigger**: Called by the `x/staking` keeper after a validator has been confirmed to have committed a liveness (downtime) or Byzantine (double-signing) fault, but *before* the state change is finalized.
*   **Action for Collateral Module**:
    1.  The hook receives the address of the punished validator and the slash fraction.
    2.  The module will look up the participant associated with this validator address.
    3.  If a participant is found, the module will immediately slash their deposited collateral by the same fraction.
    4.  This ensures that consensus-level faults result in the burning of real collateral from the `x/inference` module, maintaining network security.

### 3.2. `AfterValidatorBeginUnbonding`
*   **Trigger**: Called by the `x/staking` keeper the moment a validator's status changes from `BONDED` to `UNBONDING`. This happens when a validator is jailed for any reason or is kicked from the active set for having low power.
*   **Action for Collateral Module**:
    1.  The module will look up the participant associated with the validator.
    2.  If found, the module can mirror this state change. For instance, it could prevent the participant from depositing more collateral or prevent them from using their collateral to gain weight until they become active again.
    3.  This hook serves as a signal that the participant is inactive at the consensus level.

### 3.3. `AfterValidatorBonded`
*   **Trigger**: Called by the `x/staking` keeper whenever a validator enters the `BONDED` state. This occurs when a previously jailed validator is un-jailed and has enough power to rejoin the active set.
*   **Action for Collateral Module**:
    1.  The module will look up the participant associated with the validator.
    2.  If found, the module can mark their collateral as fully active again.
    3.  This hook signals that the participant is once again an active and trusted part of the consensus set, and their collateral can be used to its full effect. 