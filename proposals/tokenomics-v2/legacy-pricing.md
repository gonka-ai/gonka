# Legacy Unit of Compute Pricing System

This document explains the current Unit of Compute pricing system implementation and identifies exactly where changes need to be made to implement the dynamic pricing system described in [dynamic-pricing.md](./dynamic-pricing.md).

## Current Unit of Compute Pricing Flow

To understand where dynamic pricing changes need to be implemented, it's important to document the current Unit of Compute pricing flow:

### Current Pricing Flow During Epoch Transitions

**Current Implementation in `inference-chain/x/inference/module/module.go`:**

1. **`onSetNewValidatorsStage` Function** (called during epoch transitions): This function calls `computePrice` to calculate the price for the upcoming epoch, then calls `moveUpcomingToEffectiveGroup` to apply the calculated price to the new epoch.

2. **`computePrice` Function**: This function gets the default price from either the current epoch group data's `UnitOfComputePrice` field or from params for epoch 1. It then retrieves all price proposals from participants using `AllUnitOfComputePriceProposals` and calculates the weighted median price using the epoch group's `ComputeUnitOfComputePrice` method.

3. **`moveUpcomingToEffectiveGroup` Function**: This function sets the calculated price in the new epoch group data by assigning it to `newGroupData.UnitOfComputePrice`.

### Current Cost Calculation System

**In `inference-chain/x/inference/calculations/inference_state.go`:** The current simplified implementation uses a constant `PerTokenCost` value of 1000 for both cost calculation and escrow determination.

**`CalculateCost` Function Usage:**
- Called in `ProcessFinishInference` to calculate the actual cost after inference completion based on real token usage
- Called in `setEscrowForFinished` to determine payment amounts when an inference that was already processed gets finished
- Multiplies the total token count (prompt + completion tokens) by the constant `PerTokenCost`

**`CalculateEscrow` Function Usage:** 
- Called in `ProcessStartInference` to determine how much money needs to be held in escrow before inference begins
- Calculates escrow by multiplying the sum of max tokens and prompt tokens by the constant `PerTokenCost`
- Used to ensure sufficient funds are available to cover the maximum possible cost of an inference request
- This is the "up-front" cost calculation that reserves funds before processing

**Inference Lifecycle Cost Flow:**

**Normal Order (Start → Finish):**
1. **Start**: `CalculateEscrow` determines how much to hold in escrow (based on estimated max usage)
2. **Finish**: `CalculateCost` determines actual cost (based on real token usage)  
3. **Settlement**: Difference between escrow and actual cost is refunded to user

**Out-of-Order (Finish → Start):**
1. **Finish arrives first**: `ProcessFinishInference` creates inference entity with completion data and sets `ExecutedBy` field
2. **Start arrives later**: `ProcessStartInference` detects the inference was already finished (via `finishedProcessed` function checking `ExecutedBy`) and calls `setEscrowForFinished`
3. **Special settlement**: `setEscrowForFinished` immediately calculates actual cost using `CalculateCost`, compares it to escrow amount, and sets payment to the minimum of actual cost and escrow amount

**Key Difference**: When Finish arrives first, the system immediately settles the payment when Start arrives, rather than waiting for a separate settlement phase.

**Pricing Implications for Dynamic Pricing:**
- Both `CalculateCost` and `CalculateEscrow` are used regardless of message order
- In the out-of-order case, `CalculateCost` is called twice: once in `setEscrowForFinished` and once in `ProcessFinishInference` (for ActualCost field)
- The dynamic pricing system must handle both scenarios and ensure consistent pricing regardless of message arrival order
- Price recording must happen on whichever message arrives first, as described in the dynamic pricing proposal

### Current API Fund Validation

**In `decentralized-api/internal/server/public/post_chat_handler.go`:** The fund validation logic calculates the maximum token cost by multiplying the request's max tokens by the constant `PerTokenCost`, calculates the prompt token cost similarly, and then sums both values to determine the total escrow needed before processing the inference request.

## Implementation Integration Points for Dynamic Pricing

The dynamic pricing mechanism will integrate seamlessly with existing tokenomics components by **replacing** the current Unit of Compute pricing flow.

### REPLACE: Unit of Compute Price Calculation in Epoch Transitions

**Current Location**: `inference-chain/x/inference/module/module.go`, `computePrice` function

**Current Logic**: The manual price proposal system where the function calls `ComputeUnitOfComputePrice` on the upcoming epoch group to calculate a weighted median from participant proposals.

**New Logic**: Replace this with dynamic pricing system initialization by calling `CacheAllModelCapacities` from the keeper to cache model capacities from epoch group data. Dynamic pricing will then update per-model prices during block processing via the EndBlocker function.

### REPLACE: Price Storage in Epoch Group Data

**Current Location**: `inference-chain/x/inference/module/module.go`, `moveUpcomingToEffectiveGroup` function

**Current Logic**: The function sets a single unit of compute price for all models by assigning the calculated price to `newGroupData.UnitOfComputePrice`.

**New Logic**: Remove this line entirely since prices will be stored per-model in dedicated KV storage rather than as a single value in the epoch group data.

### REPLACE: Cost Calculation Functions

**Current Location**: `inference-chain/x/inference/calculations/inference_state.go`

**Current Logic**: 
- `CalculateCost` uses a constant `PerTokenCost` value multiplied by the sum of completion and prompt tokens
- `CalculateEscrow` uses the same constant multiplied by the sum of max tokens and prompt tokens

**New Logic**: 
- Update `CalculateCost` to use per-model dynamic pricing by calling `GetCachedModelPrice` with the inference model to retrieve the current price from the KV store, then multiply the total token count by this model-specific price
- Update `CalculateEscrow` to use the same per-model pricing mechanism, retrieving the model-specific price and multiplying by the estimated token count (max tokens + prompt tokens)

### ADD: Dynamic Price Updates During Block Processing

**New Location**: `inference-chain/x/inference/module/module.go`, `EndBlocker` function

**New Logic**: Add a call to `UpdateDynamicPricing` from the keeper to update per-model prices every block based on current utilization metrics.

### REPLACE: API Fund Validation

**Current Location**: `decentralized-api/internal/server/public/post_chat_handler.go`

**Current Logic**: The function calculates max token cost by multiplying max tokens by the constant `PerTokenCost` from the calculations package.

**New Logic**: Replace this with model-specific dynamic pricing by calling `GetModelPerTokenPrice` on the cosmos client with the request model, then multiply max tokens by this model-specific price.

### Price Recording During Inference Processing

**Integration Points**: Both `inference-chain/x/inference/keeper/msg_server_start_inference.go` and `inference-chain/x/inference/keeper/msg_server_finish_inference.go`

**New Logic**: Whichever handler processes the first message will read the current price from `pricing/{model_id}` KV storage and lock it for that specific inference.

**Out-of-Order Considerations**: 
- If `MsgFinishInference` arrives first, it must record the price immediately
- If `MsgStartInference` arrives later, it should skip price recording since it's already locked
- This ensures consistent pricing regardless of message arrival order, which is critical given the system's support for unordered transactions

### Utilization Tracking During Inference Completion

**Integration Point**: `inference-chain/x/inference/keeper/msg_server_finish_inference.go`

**New Logic**: Add call to `UpdateModelUtilization(modelId, blockHeight, totalTokens)` to track actual usage for dynamic pricing calculations.

### Capacity Caching During Epoch Activation

**Integration Point**: `inference-chain/x/inference/module/module.go`, `onSetNewValidatorsStage` function, immediately after the `moveUpcomingToEffectiveGroup` call

**New Logic**: Call `CacheAllModelCapacities()` to copy `total_throughput` values from each model's epoch group data to the `capacity/{model_id}` KV store for fast access during the epoch.

## Current System Architecture

### Three-Factor Pricing Formula

The current system uses:
```
Final Fee = (Prompt Tokens + Actual Completion Tokens) × UnitsOfComputePerToken × UnitOfComputePrice
```

Where:
- **UnitsOfComputePerToken**: Set per model during registration
- **UnitOfComputePrice**: Calculated each epoch via weighted median of participant proposals
- **Final cost**: Applied after inference completion

### Key Components

1. **Price Proposal System**: Participants submit proposals each epoch via `MsgSubmitUnitOfComputePriceProposal`
2. **Weighted Median Calculation**: In `inference-chain/x/inference/epochgroup/unit_of_compute_price.go`
3. **Model Registration**: Each model has `UnitsOfComputePerToken` value
4. **Epoch Group Storage**: Single `UnitOfComputePrice` stored in epoch group data
5. **Constant Fallback**: `PerTokenCost = 1000` used for simplified calculations

### Files Requiring Changes

**Chain Node (`inference-chain`)**:
- `x/inference/module/module.go` - Epoch transition and price calculation
- `x/inference/calculations/inference_state.go` - Cost calculation functions  
- `x/inference/keeper/msg_server_start_inference.go` - Price recording
- `x/inference/keeper/msg_server_finish_inference.go` - Price recording and utilization tracking
- `x/inference/epochgroup/unit_of_compute_price.go` - Price calculation (to be deprecated)
- `x/inference/keeper/unit_of_compute.go` - Price storage and retrieval (to be modified)

**API Node (`decentralized-api`)**:
- `internal/server/public/post_chat_handler.go` - Fund validation
- `internal/server/public/get_pricing_handler.go` - Pricing endpoint
- `cosmosclient/cosmosclient.go` - Price query methods
- `internal/server/admin/unit_of_compute_proposal_handlers.go` - Admin endpoints (to be deprecated)

This documentation serves as a complete reference for implementing the dynamic pricing system by showing exactly what current logic needs to be replaced and where new functionality should be added. 