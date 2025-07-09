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