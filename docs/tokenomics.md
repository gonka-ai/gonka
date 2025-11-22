# Tokenomics

This document outlines the tokenomics of the project, explaining how fees are charged, and how rewards are distributed to participants.

## Fees

Fees are an essential part of the ecosystem, ensuring that the network is compensated for the computational resources used.

### Fee Calculation based on Units of Compute

When a user sends an inference request, the fee for request is determined by the potential computational cost of the request, which is based on the number of tokens in the prompt and the maximum number of tokens that can be in the completion. Based on the actual number of tokens processed: the tokens in the user's prompt and the tokens generated in the completion. This ensures users only pay for what they use.

The calculation is based on a more nuanced concept of "Units of Compute":

1.  **Unit of Compute**: This is an abstract representation of the computational resources required for an inference task.

2.  **Units of Compute per Token**: Each AI model has a specific `UnitsOfComputePerToken` value. This value, set when the model is registered on the chain, represents how many Units of Compute are consumed for each token processed by that model.

3.  **Unit of Compute Price**: The price for a single Unit of Compute is dynamic and is determined by the network participants. In each epoch, participants can submit proposals for the price. The final `UnitOfComputePrice` is a weighted median of all valid proposals for that epoch. This logic can be seen in `inference-chain/x/inference/epochgroup/unit_of_compute_price.go`.

The final fee for an inference request is calculated by multiplying these three factors together:

`Final Fee = (Prompt Tokens + Actual Completion Tokens) * UnitsOfComputePerToken * UnitOfComputePrice`

To ensure network providers are guaranteed payment, an amount covering the maximum possible cost is held in escrow when a request is initiated. This process is explained in the next section. The final cost calculation happens in the `CalculateCost` function within `inference-chain/x/inference/calculations/inference_state.go`.

*Note: The current implementation in `CalculateCost` and `CalculateEscrow` appears to use a constant `PerTokenCost` for simplicity, but the underlying architecture is designed for the dynamic, model-specific pricing described above.*

### The Escrow and Refund Mechanism

It is important to understand that an initial amount, calculated using the user-defined `Max Completion Tokens`, is held in **escrow**. This is a crucial mechanism to ensure fairness for both users and network participants.

1.  **Initial Escrow**: Before any on-chain transaction, the `decentralized-api` first verifies that the user has sufficient funds to cover the maximum possible cost of the request. This check can be seen in `decentralized-api/internal/server/public/post_chat_handler.go`. If the check passes, an amount is placed in escrow on the blockchain to guarantee payment to the provider. The on-chain escrow calculation can be found in `CalculateEscrow` in `inference-chain/x/inference/calculations/inference_state.go`.

2.  **Final Settlement and Refund**: After the inference request is completed, the system calculates the **Actual Cost** based on the *actual* number of tokens generated (as shown in the formula above). The provider is paid this `ActualCost` from the escrowed amount. Any remaining funds are immediately **refunded** to the user.

This two-step process ensures that users are only charged for the computational resources they actually consume, while providers are protected against unfunded requests.

### Blockchain Transaction Fees

Unlike many other blockchains, this network does not charge separate fees (i.e., "gas") for submitting transactions. The configuration, as seen in `decentralized-api/cosmosclient/cosmosclient.go`, sets transaction fees to zero.

The economic model is focused on the fees for the core service—AI inference—rather than on generic transaction processing. This simplifies the experience for users, who only need to be concerned with the cost of their inference requests.

## Rewards

Participants who contribute to the network by running inference nodes are rewarded with newly minted coins.

### Reward Components

After each epoch, participants can claim their rewards. The rewards consist of two parts:

1.  **Work Coins:** These are the fees that were put into escrow by the users who made the inference requests.
2.  **Reward Coins:** These are newly minted coins created through a subsidy mechanism to incentivize network participation.

The process of claiming rewards and the distribution of work and reward coins are handled in `inference-chain/x/inference/keeper/msg_server_claim_rewards.go`. The minting of new reward coins is done by the `MintRewardCoins` function in `inference-chain/x/inference/keeper/payment_handler.go`.

### Subsidy Mechanism

The number of **Reward Coins** minted in each epoch is not fixed; it is determined by a **Subsidy Mechanism**. This mechanism calculates the total amount of new coins to create based on the total work performed by all participants in that epoch.

This system is designed to incentivize participation, especially in the early stages of the network, and it gradually reduces the number of new coins over time. The specific logic for this calculation can be found in `inference-chain/x/inference/keeper/accountsettle.go`.

### Distribution of Rewards

Both **Work Coins** and **Reward Coins** are distributed to participants at the end of each epoch. However, they are distributed differently:

*   **Work Coins Distribution**: The distribution of Work Coins is straightforward. Each participant receives the exact amount of fees that they have accumulated for the inference tasks they processed during the epoch. These are the funds that were held in escrow.

*   **Reward Coins Distribution**: Reward Coins are distributed proportionally among all contributing participants. The system first calculates the total amount of work done by all participants in an epoch. Then, each participant receives a share of the total Reward Coins that is directly proportional to their contribution to the total work.

This dual reward system ensures that participants are compensated directly for the tasks they perform (Work Coins) and also receive a share of the network's growth and success (Reward Coins). The logic for calculating these amounts for each participant is in the `getSettleAmount` function within `inference-chain/x/inference/keeper/accountsettle.go`.

### Top Miner Rewards

In addition to the regular rewards and subsidies, there are special rewards for the top miners in the network. These rewards are designed to incentivize high-performing and reliable participants. The criteria for being a top miner and the distribution of these rewards are defined in `inference-chain/x/inference/keeper/top_miner_calculations.go`.

## Collateral System

The network implements a collateral system that strengthens security by ensuring participants with significant network influence have a direct financial stake in the network's integrity. This system was introduced as part of Tokenomics V2 to create accountability and prevent malicious behavior.

### Participant Weight and Collateral

Network participants earn "weight" through Proof of Compute activities (work done, nonces delivered, etc.). This weight influences their role in governance processes, such as unit of compute price calculation. The collateral system introduces a hybrid model that combines:

1. **Base Weight**: A portion of potential weight granted unconditionally (default 20%)
2. **Collateral-Eligible Weight**: Additional weight that must be backed by deposited collateral (remaining 80%)

### Grace Period

To encourage early adoption, there is an initial grace period (default 180 epochs) during which no collateral is required. During this period, all potential weight is granted unconditionally. After the grace period ends, the collateral requirements become active.

### Managing Collateral

Participants can interact with the collateral system through two main operations:

- **Deposit Collateral**: Transfer tokens from spendable balance to be held as collateral
- **Withdraw Collateral**: Initiate return of collateral (subject to unbonding period)

Withdrawals are not immediate - they enter an "unbonding queue" for a configurable period (default 1 epoch) before being released. This ensures collateral remains slashable even after withdrawal is initiated.

### Slashing Conditions

Collateral can be "slashed" (seized and burned) under specific conditions:

1. **Malicious Behavior**: When a participant is marked as `INVALID` due to consistently incorrect work (default 20% slash)
2. **Downtime**: When a participant fails to meet participation requirements in an epoch (default 10% slash)
3. **Consensus Faults**: When the associated validator commits consensus-level violations

Slashing is applied proportionally to both active collateral and any collateral in the unbonding queue.

### Integration with Consensus

The collateral system integrates with the underlying Cosmos SDK staking module through hooks, ensuring that consensus-level penalties (validator slashing, jailing) are reflected in the application-specific collateral system.

*For detailed technical specifications, see the [Collateral Proposal](../proposals/tokenomics-v2/collateral.md).*

## Reward Vesting

To better align long-term incentives of network participants with sustained growth and stability, the network implements a reward vesting system. This ensures that newly distributed rewards are released gradually over time rather than immediately.

### Vesting Mechanism

All newly distributed rewards are routed through a dedicated vesting system:

- **Work Coins**: Fees from user requests, subject to configurable vesting periods
- **Reward Coins**: Newly minted subsidies, subject to configurable vesting periods  
- **Top Miner Rewards**: Special high-performer rewards, subject to configurable vesting periods

### Vesting Schedule

Each participant maintains a personal vesting schedule - essentially an array where each element represents tokens unlocking in a specific epoch. When new rewards are earned:

1. The reward amount is divided evenly across the vesting period
2. Any remainder from division is added to the first epoch for precision
3. Amounts are aggregated into existing schedule elements to maintain efficiency

### Vesting Periods

The system supports different vesting periods for different reward types:

- `WorkVestingPeriod`: Controls vesting for work coins (default 0, configurable via governance)
- `RewardVestingPeriod`: Controls vesting for reward subsidies (default 0, configurable via governance)  
- `TopMinerVestingPeriod`: Controls vesting for top miner rewards (default 0, configurable via governance)

In production environments, these are typically configured to 180 epochs (~180 days) to encourage long-term participation.

### Token Unlocking

Vested tokens are automatically unlocked once per epoch:

1. The system processes each participant's vesting schedule
2. Tokens from the oldest vesting entry are transferred to the participant's spendable balance
3. The processed entry is removed from the schedule
4. Empty schedules are cleaned up to prevent state bloat

This creates a predictable, automated release of vested tokens synchronized with the network's epoch lifecycle.

### Querying Vesting Status

Participants can query their vesting status to see:
- Total amount currently vesting
- Detailed breakdown of future unlock schedule  
- Historical information about released tokens

*For detailed technical specifications, see the [Vesting Proposal](../proposals/tokenomics-v2/vesting.md).*

## Token Supply

The initial total supply of the native coin and its distribution are defined in the `DefaultGenesisOnlyParams` function in `inference-chain/x/inference/types/params.go`. This includes the allocation for the originator, top reward amount, pre-programmed sale amount, and standard reward amount. 
