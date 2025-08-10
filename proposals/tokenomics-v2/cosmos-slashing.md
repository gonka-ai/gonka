# Cosmos SDK: Original Slashing Conditions

This document outlines the conditions under which a validator's staked tokens (collateral) can be slashed or burned in the original, unmodified `x/staking` and `x/slashing` modules of the Cosmos SDK. These mechanisms are the core of the network's security model, enforcing validator honesty and availability.

There are two primary categories of offenses that trigger slashing:

1.  **Liveness Faults (Downtime)**: Punished for being offline and failing to participate in consensus.
2.  **Byzantine Faults (Misbehavior)**: Punished for actively trying to compromise the network, with double-signing being the primary example.

---

## 1. Liveness Faults (Downtime)

This penalty is applied automatically to validators who are unresponsive or unavailable.

### Triggering Condition

-   The logic is handled in `x/slashing/keeper/infractions.go` within the `HandleValidatorSignature` function, which is executed for every validator on every block.
-   A validator is considered to have committed a liveness fault if, within a configurable window (`SignedBlocksWindow`), they fail to sign a minimum number of blocks (`MinSignedPerWindow`).
-   Specifically, if `MissedBlocksCounter` for a validator exceeds `SignedBlocksWindow - MinSignedPerWindow`, the penalty is triggered.

### Penalties

1.  **Slashing**: A small, predefined fraction of the validator's stake is burned.
    -   **Amount**: Defined by the `SlashFractionDowntime` parameter in the `x/slashing` module (e.g., default is 0.5%). This penalty applies to the validator's self-bonded stake as well as the stake of all its delegators.
2.  **Jailing**: The validator is "jailed," meaning they are immediately removed from the active validator set and cannot participate in consensus.
    -   **Duration**: The jailing lasts for a period defined by the `DowntimeJailDuration` parameter (e.g., 10 minutes).
    -   After the jail period expires, the validator operator must manually send a `MsgUnjail` transaction to rejoin the active set.

---

## 2. Byzantine Faults (Double-Signing)

This is the most severe penalty, reserved for validators who provably act maliciously. The primary example is "double-signing" or "equivocation," where a validator signs two different blocks at the same height.

### Triggering Condition

-   The process begins when another node in the network observes the misbehavior and submits it to the chain as an `Evidence` transaction.
-   The `x/evidence` module's `BeginBlocker` processes this transaction. The core logic is in `x/evidence/keeper/infraction.go` within the `handleEquivocationEvidence` function.
-   After verifying the evidence is valid (e.g., not too old), the `handleEquivocationEvidence` function directly calls the `x/slashing` keeper to initiate the punishment.
-   The **exact trigger** is the call to `k.slashingKeeper.SlashWithInfractionReason(...)` within this function. This call starts the chain of events that leads to the validator being slashed and jailed.

### Penalties

1.  **Severe Slashing**: A significant fraction of the validator's stake is burned.
    -   **Amount**: Defined by the `SlashFractionDoubleSign` parameter in the `x/slashing` module (e.g., default is 5%). This is a much harsher penalty than for downtime.
2.  **Permanent Jailing (Tombstoning)**: The validator is permanently removed from the active set.
    -   The `JailUntil` time for the validator is set to the maximum possible time (`292277024625-12-02 23:47:16.854775807 +0000 UTC`), effectively a permanent ban.
    -   The validator is "tombstoned," meaning their consensus public key is blacklisted. They can **never** rejoin the validator set with that key. The only way for the operator to validate again would be to create a completely new validator with a new key. 