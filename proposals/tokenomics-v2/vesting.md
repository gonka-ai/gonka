# Tokenomics V2 Proposal: Reward Vesting

This document proposes an enhancement to the project's tokenomics by introducing a vesting mechanism for all newly distributed rewards. The goal is to better align the long-term incentives of network participants with the sustained growth and stability of the ecosystem.

## 1. Summary of Changes

The core change is to route all `Reward Coins` (both from subsidies and top miner rewards) through a new vesting system instead of distributing them directly to participants' wallets. `Work Coins` (fees from users) will continue to be paid out directly and will not be subject to vesting.

This will be accomplished by creating a new, dedicated `vesting` module within the `inference-chain`.

## 2. Implementation Details

### 2.1. New `x/vesting` Module

A new Cosmos SDK module will be created at `inference-chain/x/vesting/`. This module will be responsible for holding and managing all vesting funds for all participants. The module will depend on the `bank` module for token transfers and will be called by the `inference` module for both reward distribution and epoch advancement.

### 2.2. Vesting Data Structure

For each participant receiving rewards, the `vesting` module will maintain a data structure that represents their personal vesting schedule. This structure will essentially be an array or queue where each element corresponds to a single epoch's worth of vested tokens (representing one day in production).

The logic for adding new rewards is designed to be efficient and precise by aggregating funds into the existing schedule. When a new reward is earned with a vesting period of `N` epochs, the process is as follows:
1.  The total reward amount is divided by `N` to calculate the base per-epoch amount. Due to integer division, there may be a small remainder.
2.  This base per-epoch amount is added to each of the first `N` elements in that participant's vesting array. Any leftover amount (the remainder from the division) is added to the very first element (index 0). This ensures the exact total reward amount is fully accounted for without any loss due to rounding.
3.  If the participant's array currently contains fewer than `N` elements, the per-epoch amount is added to all existing elements, and the remainder is added to the first element. Then, new elements are appended to the end of the array—each containing the base per-epoch amount from the new reward—until the array's total length is `N`.

This aggregation method ensures that a participant's vesting array does not grow beyond the maximum vesting period (e.g., 180 elements for a 180-epoch period), making the process scalable and efficient.

This data structure will be defined in a new protobuf file, `inference-chain/proto/inference/vesting/vesting_schedule.proto`.

### 2.3. Vesting Period Configuration

The vesting system supports configurable vesting periods for different types of payments through parameters in the `x/inference` module:

- `WorkVestingPeriod` - Controls vesting duration for work coins (paid from escrow)
- `RewardVestingPeriod` - Controls vesting duration for reward coins (from subsidies)  
- `TopMinerVestingPeriod` - Controls vesting duration for top miner rewards

**Configuration Strategy:**
- **Default Values**: Set to `0` in the code (meaning no vesting by default)
- **Production Environment**: Expected to be configured to `180` epochs (~180 days)
- **E2E Testing**: Should be configured to `2` epochs for faster test execution
- **Development**: Can remain at `0` for immediate token distribution during development

The vesting periods are governance-configurable parameters that can be adjusted through on-chain governance proposals without requiring code changes.

### 2.4. Integration with the `x/inference` Module

The existing reward distribution logic must be modified to accommodate this change.

Currently, the `x/inference` module directly pays rewards to participants. This will be changed so that the `x/inference` module calls a new function on the `vesting` module's keeper. This new function will take the reward amount and the participant's address, and it will add the new vested amount to that participant's schedule as described above.

The `x/inference` module will need to define a `VestingKeeper` interface in its `expected_keepers.go` file, similar to how it interfaces with the `CollateralKeeper`.

The primary files to be modified for this change include:
- `inference-chain/x/inference/keeper/msg_server_claim_rewards.go` - for regular reward claims
- `inference-chain/x/inference/keeper/msg_server_claim_top_miner_rewards.go` - for top miner rewards
- Any other location where reward coins are distributed

### 2.5. Token Unlocking Mechanism

The `x/vesting` module will process vesting unlocks once per epoch (once per day in production). This will be implemented using the `AdvanceEpoch` pattern, similar to how the `x/collateral` module works.

The `x/inference` module will call the vesting module's `AdvanceEpoch(ctx, completedEpoch)` function when an epoch completes. This function will:

1.  Iterate through every participant's vesting schedule.
2.  Look at the very first element (the oldest entry) in each participant's vesting array.
3.  Transfer the token amount from that single element from the `vesting` module's account to the participant's main, spendable account balance.
4.  Remove that element from the beginning of the array, effectively making the next element in the sequence the new "first" one.
5.  After removing the element, check if the vesting array is now empty. If it is, the entire vesting schedule record for that participant is deleted from the state to prevent bloat.

This ensures a predictable, automated, and gradual release of vested tokens into the ecosystem, synchronized with the network's epoch lifecycle. The function will be implemented in `inference-chain/x/vesting/keeper/keeper.go`.

### 2.6. Chain-wide Integration

The new `x/vesting` module must be registered with the main application. This involves:
- Updating `inference-chain/app/app_config.go` to add the module to genesis order (end blocker is not needed since we use `AdvanceEpoch`)
- Adding the module account permission with `minter` capability (to hold vesting funds)
- Ensuring proper keeper initialization with bank keeper dependency
- Adding the vesting keeper to the inference module's keeper dependencies

### 2.7. Querying Vesting Status

To provide transparency, new query endpoints will be added. Participants will be able to query their current vesting status, including:
-   The total amount of tokens they have currently vesting.
-   A detailed breakdown of their vesting schedule (the array of future unlocks).
-   The total amount of tokens that have already been released to them.

These new queries will be defined in `inference-chain/proto/inference/vesting/query.proto` and implemented in `inference-chain/x/vesting/keeper/query_server.go`. 