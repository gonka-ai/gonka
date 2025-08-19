# Staking, Compute, and Slashing Module Changes

This document describes the modifications made to the Cosmos SDK to support Gonka's Proof of Compute (PoC) consensus mechanism. These changes are published in our fork at: https://github.com/product-science/cosmos-sdk/tree/release/v0.53.x

This set of changes introduces a significant overhaul of the `x/staking` module to disconnect it from real tokenomics. The primary goal is to base validator consensus power on an external "Proof of Compute" (PoC) metric, rather than on bonded tokens. This involves fundamental changes to staking, slashing, and bank logic.

## Key Changes

### 1. Staking Logic Override & "Compute Power"

A new system based on "compute power" has been introduced, completely overriding the standard token-bonding staking logic.

- **`x/staking/keeper/compute.go`**: This new file introduces `SetComputeValidators`, which is the primary entry point for managing the validator set. It takes a list of `ComputeResult` objects (containing a public key and power) and reconciles it with the current validator set.
- **No Token Bonding**: The traditional mechanism of bonding tokens is entirely bypassed.
  - In `x/staking/keeper/delegation.go`, the `Delegate` function now includes a check (`validator.Description.Details != "Created after Proof of Compute"`) to avoid moving tokens for these new compute-based validators.
  - In `x/staking/keeper/val_state_change.go`, the logic to transfer tokens between the `bonded` and `not-bonded` pools has been removed.
- **Manual `TotalBondedTokens`**: The `TotalBondedTokens` function in `x/staking/keeper/pool.go` no longer checks the bank module's balance. Instead, it manually iterates through all validators and sums their `Tokens` field to calculate the total bonded power.

### 2. Power Calculation and Validator Updates

- **`DefaultPowerReduction`**: This parameter in `types/staking.go` has been changed from `1,000,000` to `1`. This makes it so that any non-zero amount of tokens (representing compute power) translates to consensus power, accommodating a different power scale.
- **Validator Power Indexing**: Logic in `x/staking/keeper/compute.go` now correctly manages the power index by explicitly calling `DeleteValidatorByPowerIndex` before setting the new power and calling `SetValidatorByPowerIndex`. This addresses issues with validator power updates in CometBFT.

### 3. Slashing Mechanism Modified for Safe Hook Triggering

- **Staking Module Made Safe**: The core `Slash` function in `x/staking/keeper/slash.go` has been modified to no longer burn tokens. Its purpose is now to reduce a validator's abstract "compute power" and to trigger hooks for other modules.
- **Safe Hook Mechanism**: This two-part change ensures that when a validator is offline, the system can safely trigger the `BeforeValidatorSlashed` hook without causing a chain halt. This allows a separate collateral module to be reliably notified and to apply real financial penalties, while the staking module only manages consensus status and abstract power scores.

### 4. Enhanced Logging

- **Insufficient Funds**: The `subUnlockedCoins` function in `x/bank/keeper/send.go` has been updated with detailed logging, including a stack trace, to make debugging `ErrInsufficientFunds` errors easier.
- **Validator Updates**: Extensive logging was added to the `ApplyAndReturnValidatorSetUpdates` function to trace the lifecycle of validators during state changes.
- **Gas Costs**: The `gasCostPerIteration` in `x/group/keeper/msg_server.go` was set to `0` to remove gas costs for certain operations. 