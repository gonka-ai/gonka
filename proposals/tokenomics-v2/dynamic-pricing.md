# Tokenomics V2 Proposal: Dynamic Pricing

This document proposes an enhancement to the project's tokenomics by introducing an automatic dynamic pricing mechanism for inference costs. The goal is to create a responsive pricing system that adjusts based on network utilization, similar to Ethereum's gas pricing mechanism, while maintaining economic incentives for network participants.

## 1. Summary of Changes

This proposal introduces a system where the per-token price automatically adjusts every block based on per-model network demand and utilization metrics, replacing the current Unit of Compute abstraction with direct per-token pricing. The current manual proposal-based pricing system will be replaced with an algorithmic approach that responds to real-time network load on a per-model basis, ensuring optimal resource allocation and fair pricing for each AI model.

The existing Unit of Compute system and weighted median calculation mechanism in `inference-chain/x/inference/epochgroup/unit_of_compute_price.go` will be replaced with an automatic per-model per-token price adjustment algorithm that considers block-level utilization with defined stability zones and maximum adjustment limits for each individual model.

## 2. Implementation Details

A new dynamic pricing module will be integrated into the existing `x/inference` module. The `x/inference` module will be enhanced with new pricing calculation logic that executes at the beginning of each block in the `BeginBlocker` function within `inference-chain/x/inference/module/module.go`.

### 2.1. Price Adjustment Algorithm

The core of the dynamic pricing system is a stability zone model that automatically adjusts prices to maintain optimal network utilization while providing price stability within acceptable utilization ranges. The system will implement a block-based adjustment mechanism with defined stability zones and maximum change limits.

#### 2.1.1. Stability Zone Model

The system will define a stability zone for network utilization between 40% and 60%, within which prices remain unchanged. Outside this zone, prices will adjust to encourage utilization to return to the optimal range.

The calculation process will be:

1. **Current Utilization Calculation**: At the end of each block, the system calculates the recent utilization based on inference requests processed in the current block and recent block history versus estimated network capacity.

2. **Stability Zone Check**: If utilization is between 40% and 60%, no price adjustment occurs, maintaining price stability during normal network operation.

3. **Price Adjustment**: If utilization is below 40%, prices decrease to encourage more usage. If utilization is above 60%, prices increase to moderate demand.

4. **Linear Price Adjustment**: Price changes are directly proportional to utilization deviation from the stability zone, with the elasticity parameter determining the maximum change at extreme utilization levels (0% or 100%).

#### 2.1.2. Price Adjustment Formula

The new price calculation will follow this formula, similar to Ethereum's EIP-1559, but calculated separately for each model:

```
// Calculate per-model utilization and pricing
for each_model in active_epoch_models:
    model_capacity = get_cached_capacity(model_id)  // from capacity/{model_id} KV store
    model_utilization = model_tokens_processed_in_recent_blocks[model_id] / model_capacity

    if model_utilization >= 0.40 and model_utilization <= 0.60:
        // Stability zone - no price change
        new_model_price[model_id] = previous_model_price[model_id]
    else if model_utilization < 0.40:
        // Below stability zone - decrease price
        utilization_deficit = 0.40 - model_utilization
        adjustment_factor = 1.0 - (utilization_deficit * price_elasticity)
        new_model_price[model_id] = previous_model_price[model_id] * adjustment_factor
    else:
        // Above stability zone - increase price
        utilization_excess = model_utilization - 0.60
        adjustment_factor = 1.0 + (utilization_excess * price_elasticity)
        new_model_price[model_id] = previous_model_price[model_id] * adjustment_factor

    // Ensure price never goes below 1 nicoin per token
    new_model_price[model_id] = max(new_model_price[model_id], min_per_token_price)
```

With the default elasticity of **0.05**, this means for each model independently:
- **Maximum price change**: 2% per block per model (when model utilization reaches 0% or 100%)
- **At 20% model utilization**: 1% price decrease per block for that model
- **At 80% model utilization**: 1% price increase per block for that model
- **Price floor**: Never drops below 1 nicoin to prevent zero-cost scenarios and maintain network economics
- **Independent pricing**: Each model's price adjusts based on its own demand and capacity

The minimum price of **1 nicoin** serves as a technical and economic safeguard:
- Prevents computational issues with zero pricing
- Ensures participants always receive minimal compensation
- Maintains network incentive structure even during extremely low demand
- Uses the smallest denomination unit, making it effectively negligible while preventing edge cases

This logic will be implemented in new functions:

**In `inference-chain/x/inference/keeper/dynamic_pricing.go`:**
- `CalculateModelDynamicPrice(modelId string)` - implements the price adjustment algorithm
- `CacheModelCapacity(modelId string, capacity int64)` - stores capacity in KV storage
- `GetCachedModelCapacity(modelId string)` - retrieves cached capacity from KV storage
- `CacheAllModelCapacities()` - caches capacity for all active models during epoch activation
- `UpdateDynamicPricing()` - main function called from BeginBlocker to update all model prices

**Utilization data will be retrieved from existing stats infrastructure:**
- `GetSummaryByModelAndTime(from, to int64)` - leverages existing stats aggregation for per-model token usage over time windows

#### 2.1.3. KV Storage Structure for Dynamic Pricing Data

The system will use simplified KV storage for calculated prices and cached capacity:

```
// Current per-token pricing per model  
Key Pattern: pricing/current/{model_id}
Value: current_per_token_price (uint64)

// Cached model capacity (copied from epoch group at epoch start)
Key Pattern: pricing/capacity/{model_id}
Value: total_throughput (int64)

Examples:
pricing/current/Qwen2.5-7B-Instruct → 75
pricing/capacity/Qwen2.5-7B-Instruct → 1000000
```

This structure allows:
- **Fast price lookup**: Direct access during inference processing
- **Cached capacity**: Avoid reading large epoch group objects repeatedly
- **Independent pricing**: Each model maintains its own current price
- **Leverages existing infrastructure**: Utilization data comes from proven stats system

### 2.2. Network Utilization Measurement

The system will calculate network utilization by leveraging the existing stats infrastructure. The utilization calculation will be based on aggregated inference activity from the proven `GetSummaryByModelAndTime()` function and cached capacity metrics.

#### 2.2.1. Per-Model Utilization Metrics

The following metrics will be used to calculate per-model network utilization:

1. **Per-Model Token Processing**: Total tokens processed (prompt + completion) from completed inferences for each specific model within a time window, retrieved using the existing `GetSummaryByModelAndTime()` function from the stats infrastructure.

2. **Model-Specific Capacity Access**: Retrieved from cached `pricing/capacity/{model_id}` KV storage, representing the total token processing capacity available for that specific model (copied from epoch group data at epoch start).

3. **Time-Based Utilization Windows**: Utilization calculated over configurable time windows (e.g., 60 seconds) using real block timestamps from the existing stats system, providing accurate feedback on true resource consumption patterns.

The utilization calculation will leverage the proven `GetSummaryByModelAndTime()` function from `inference-chain/x/inference/keeper/developer_stats_aggregation.go`, ensuring consistency with existing analytics and reporting systems.

#### 2.2.2. Price Recording vs. Utilization Measurement

The system implements two distinct timing mechanisms for pricing and utilization:

**Price Recording**: When the first inference message arrives (either `MsgStartInference` or `MsgFinishInference`), the current unit of compute price is recorded and locked for that specific inference. This ensures users have predictable pricing based on network conditions when their inference begins processing, regardless of message ordering.

**Utilization Measurement**: When an inference completes (at `MsgFinishInference`), the actual token count (prompt + completion tokens) is recorded and used to update network utilization metrics. This reflects the true computational work performed.

This separation ensures:
- Users get price certainty based on when their inference enters the system (earliest message)
- Utilization calculations reflect actual resource consumption
- Price adjustments are based on real computational load, not just request frequency
- System works correctly regardless of whether `MsgStartInference` or `MsgFinishInference` arrives first

#### 2.2.3. Per-Model Capacity Caching

Model-specific capacity will be cached separately to avoid repeatedly reading large epoch group objects:

**Capacity Caching at Epoch Activation**: When the upcoming epoch group becomes the current/active epoch group, the system will copy the `total_throughput` field from each model's subgroup `EpochGroupData` into the KV store using the `capacity/{model_id}` key pattern.

**Fast Capacity Access**: During block processing and price calculations, the system will read the cached capacity values from KV storage rather than querying the epoch group objects.

**Epoch Immutability**: The cached capacity values remain constant throughout the epoch, reflecting the stable model allocation determined during epoch formation, while utilization and pricing data updates independently in real-time.

#### 2.2.4. Inference Price Storage and Cost Calculation

To handle the complex inference lifecycle where messages can arrive out-of-order, the dynamic pricing system will store the price directly in each inference entity and modify the existing cost calculation functions:

**Price Storage in Inference Entity**: Each inference will store its locked-in per-token price, ensuring consistent pricing regardless of message arrival order.

**Price Recording Function**: A new function `RecordInferencePrice()` will be called at the beginning of both `ProcessStartInference` and `ProcessFinishInference` to:
- In `ProcessStartInference`: if `!finishedProcessed(inference)` then this is the first message, so read current price from `pricing/current/{model_id}` KV storage and store it
- In `ProcessFinishInference`: if `!startProcessed(inference)` then this is the first message, so read current price from `pricing/current/{model_id}` KV storage and store it
- This reuses the existing state-checking logic to determine if the inference record already existed

**Modified Cost Calculation Functions**: 
- `CalculateCost()` in `inference-chain/x/inference/calculations/inference_state.go` will read the stored per-token price from the inference entity and multiply by actual token count
- `CalculateEscrow()` will read the stored per-token price from the inference entity (price is guaranteed to be stored by this point)
- Both functions work with the simplified formula: `tokens × stored_per_token_price`

**Out-of-Order Message Handling**:
- **Normal order (Start→Finish)**: Start message calls `RecordInferencePrice()` first, locks price; Finish message calls `RecordInferencePrice()` but price already exists
- **Out-of-order (Finish→Start)**: Finish message calls `RecordInferencePrice()` first, locks price; Start message calls `RecordInferencePrice()` but price already exists
- **All cost calculations**: `CalculateCost()` and `CalculateEscrow()` simply read the already-stored price
- This ensures price recording happens immediately when first message arrives, not during cost calculations

### 2.3. Grace Period Mechanism

To encourage early adoption and provide stability during network bootstrap, the system will include a grace period during which inference prices are set to zero. This mechanism ensures that early users and developers can test and build on the network without cost barriers.

#### 2.3.1. Zero-Price Grace Period

During the initial network phase, controlled by a governance parameter `GracePeriodEndEpoch` with a proposed default of `90` epochs, the dynamic pricing system will be bypassed and all inference costs will be set to zero.

The grace period logic will be implemented as:

1. **Grace Period Check**: Before applying dynamic pricing calculations, the system checks if the current epoch is within the grace period.

2. **Zero Price Override**: If within the grace period, the per-token price is set to `0` for all models regardless of utilization metrics.

3. **Smooth Transition**: After the grace period ends, the system transitions to a base price level before beginning dynamic adjustments.

This functionality will be integrated into the existing epoch transition logic in `inference-chain/x/inference/module/module.go` within the `moveUpcomingToEffectiveGroup` function.

#### 2.3.2. Post-Grace Period Initialization

When the grace period ends, the system will initialize pricing at a governance-defined `BasePerTokenPrice` value for each model, which will serve as the starting point for dynamic adjustments. This ensures a predictable transition from free usage to market-based pricing.

### 2.4. Parameter Configuration

The dynamic pricing system will introduce several new governance-configurable parameters to the `EpochParams` structure in `inference-chain/x/inference/types/params.go`:

- `StabilityZoneLowerBound`: Lower bound of stability zone where price doesn't change (default: 0.40)
- `StabilityZoneUpperBound`: Upper bound of stability zone where price doesn't change (default: 0.60)
- `PriceElasticity`: Controls price adjustment magnitude - determines maximum change at maximum utilization deviation (default: 0.05)
- `UtilizationWindowDuration`: Time window in seconds for utilization calculation (default: 60)
- `MinPerTokenPrice`: Minimum per-token price floor to prevent zero pricing (default: 1 nicoin)
- `BasePerTokenPrice`: Initial per-token price after grace period (default: 100)
- `GracePeriodEndEpoch`: Epoch when free inference period ends (default: 90)

These parameters will be added to the `DefaultEpochParams` function and will be adjustable through governance proposals.

### 2.5. Migration from Manual Pricing

The transition from the current manual proposal system to dynamic pricing will be handled through a coordinated upgrade process. The existing price proposal functionality in `inference-chain/x/inference/keeper/msg_server_submit_unit_of_compute_price_proposal.go` will be deprecated but remain available for emergency price interventions through governance.

#### 2.5.1. Backward Compatibility

During the transition period, both systems will coexist:

1. **Manual Override**: Governance can still submit manual price proposals that override dynamic pricing for specific epochs.

2. **Gradual Migration**: The dynamic pricing system can be enabled with a flag, allowing for testing and gradual rollout.

3. **Emergency Controls**: Administrative controls will allow temporary suspension of dynamic pricing if needed.

### 2.6. Integration with Existing Systems

The dynamic pricing mechanism will integrate seamlessly with existing tokenomics components without disrupting current reward distribution or validation systems.

#### 2.6.1. Implementation Integration

The dynamic pricing system will integrate at multiple points in the inference lifecycle:

**Price Recording**: In both `inference-chain/x/inference/keeper/msg_server_start_inference.go` and `inference-chain/x/inference/keeper/msg_server_finish_inference.go`, the system will check if this is the first message for a given inference (by checking if the inference entity exists and whether a price has already been recorded). Whichever handler processes the first message will read the current price from `pricing/current/{model_id}` KV storage and lock it for that specific inference.

**Price Calculation Using Existing Stats**: The system leverages the existing stats infrastructure to calculate utilization. Completed inferences automatically contribute to the stats system via the existing `SetDeveloperStats()` mechanism, which stores per-model token data using real block timestamps.

**Model Price Updates**: During each block's beginning in `inference-chain/x/inference/module/module.go`, specifically in the `BeginBlocker` function, the system will call `UpdateDynamicPricing()` from `inference-chain/x/inference/keeper/dynamic_pricing.go` which will:
- Calculate time window boundaries using `UtilizationWindowDuration` parameter
- Call existing `GetSummaryByModelAndTime()` to get per-model token usage from proven stats infrastructure
- Read cached model capacity from `pricing/capacity/{model_id}` KV storage  
- Recalculate per-token price for each active model based on utilization vs capacity
- Update the current price in `pricing/current/{model_id}` KV storage

**Capacity Caching Integration**: During epoch activation in `inference-chain/x/inference/module/module.go`, specifically in the `onSetNewValidatorsStage` function immediately after the `moveUpcomingToEffectiveGroup` call, the system will call `CacheAllModelCapacities()` from `inference-chain/x/inference/keeper/dynamic_pricing.go` to copy `total_throughput` values from each model's epoch group data to the `pricing/capacity/{model_id}` KV store for fast access during the epoch.

**Inference Message Handler Updates**: In both `inference-chain/x/inference/keeper/msg_server_start_inference.go` and `inference-chain/x/inference/keeper/msg_server_finish_inference.go`:
- Call `RecordInferencePrice()` function at the beginning of each handler
- This function checks if price is already stored and records current price if not
- All subsequent cost calculations in the handlers use the stored price
- This ensures whichever message arrives first locks in the pricing for the entire inference lifecycle

**Separate Storage Benefits**: This architecture keeps epoch group data immutable during the epoch while maintaining fast access to dynamic pricing data through dedicated KV storage, while storing locked-in prices directly in inference entities.

**Example Scenarios**:
- **Scenario A**: `MsgStartInference` arrives first → `RecordInferencePrice()` reads current price from KV storage, stores in inference entity
- **Scenario B**: `MsgFinishInference` arrives first → `RecordInferencePrice()` reads current price from KV storage, stores in inference entity
- **Both cases**: Later arriving message calls `RecordInferencePrice()` but price already exists, so no action taken

#### 2.6.2. Pricing API Updates

The existing pricing API in `decentralized-api/internal/server/public/get_pricing_handler.go` will be enhanced to provide per-model dynamic pricing information by querying the `pricing/current/{model_id}` KV storage for current prices, along with utilization metrics from existing stats functions, and capacity data for each active model.

#### 2.6.3. API Query Changes for Fund Verification

The transition to per-token pricing requires significant updates to how the decentralized API calculates and verifies user funds for inference requests.

**Current Implementation**: The system currently uses a complex three-factor calculation:
```
Cost = Tokens × Model.UnitsOfComputePerToken × Network.UnitOfComputePrice
```

**New Simplified Implementation**: The system will use direct per-token pricing:
```
Cost = Tokens × Model.PerTokenPrice
```

**Key API Changes**:

**Fund Verification in Chat Handler**: In `decentralized-api/internal/server/public/post_chat_handler.go`, the cost calculation logic will be updated to:
- Query the current per-token price from `pricing/current/{model_id}` KV storage via the chain client
- Calculate escrow amount as: `(PromptTokens + MaxCompletionTokens) × PerTokenPrice`
- Remove references to `UnitsOfComputePerToken` and `UnitOfComputePrice`

**Pricing Queries**: The API will need new query methods in `decentralized-api/cosmosclient/cosmosclient.go`:
- `GetModelPerTokenPrice(modelId string)` - retrieves current per-token price for a specific model
- `GetAllModelPrices()` - retrieves current prices for all active models
- Remove obsolete `GetUnitOfComputePrice()` method

**Cost Calculation Updates**: In `inference-chain/x/inference/calculations/inference_state.go`:
- Add new `RecordInferencePrice()` function called at the start of both `ProcessStartInference` and `ProcessFinishInference`
- Update `CalculateCost()` function to read the per-token price from the inference record (guaranteed to be stored)
- Update `CalculateEscrow()` function to read the per-token price from the inference record (guaranteed to be stored)
- Both functions will use the simplified formula: `tokens × stored_per_token_price`
- Remove `UnitsOfComputePerToken` parameter dependencies

**Price Recording in Inference Processing**: In `inference-chain/x/inference/keeper/msg_server_start_inference.go` and `msg_server_finish_inference.go`:
- Both handlers call `RecordInferencePrice()` function at the beginning
- First message to arrive records current price from `pricing/{model_id}` KV storage in inference entity
- Later arriving message calls same function but price already exists, so no action taken
- This handles both normal order (Start→Finish) and out-of-order (Finish→Start) scenarios
- Ensures consistent pricing regardless of message arrival order, critical for unordered transaction support
- Remove Unit of Compute abstraction from inference records

**Benefits of Simplified API Queries**:
- **Faster calculations**: Single multiplication instead of three-factor formula
- **Clearer pricing**: Users see direct cost per token for each model
- **Reduced complexity**: Eliminates abstract Unit of Compute concept
- **Better UX**: More intuitive pricing for developers and users

## 3. Benefits and Economic Impacts

The dynamic pricing system will provide several economic and operational benefits:

### 3.1. Per-Model Market Efficiency

Automatic price discovery for each AI model ensures that inference costs reflect true demand and supply conditions for specific models, leading to more efficient resource allocation and fair pricing that accounts for different computational requirements and popularity levels.

### 3.2. Model-Specific Network Stability

By targeting optimal utilization levels per model, the system prevents both network congestion for popular models and underutilization for specialized models, maintaining consistent service quality across the entire model portfolio.

### 3.3. Enhanced Participant Incentives

Dynamic pricing creates stronger economic incentives for participants to:
- Support diverse model portfolios to capture different pricing opportunities
- Maintain high-performance nodes for resource-intensive models
- Optimize their resource allocation across models based on demand patterns
- Remain online during peak demand periods for their supported models

### 3.4. Model-Aware Developer Experience

Predictable per-model pricing algorithms combined with the grace period provide developers with:
- Better cost forecasting capabilities for specific models
- Clear economic signals about model demand and resource requirements
- Flexibility to choose optimal models for their use cases
- Early-stage development opportunities without cost barriers across all models

## 4. Monitoring and Governance

The dynamic pricing system will include comprehensive monitoring and governance capabilities to ensure optimal network operation.

### 4.1. Per-Model Metrics and Analytics

New query endpoints will be added to `inference-chain/proto/inference/inference/query.proto` to provide real-time visibility into per-model pricing metrics:

**Per-Token Pricing Queries**:
- `GetModelPerTokenPrice(model_id)` - retrieves current per-token price for a specific model
- `GetAllModelPerTokenPrices()` - retrieves current per-token prices for all active models
- `GetModelPriceHistory(model_id, block_range)` - historical per-token price data for analysis

**Utilization and Capacity Queries**:
- `GetModelUtilization(model_id, block_range)` - current and historical utilization rates for each model from KV storage
- `GetModelCapacity(model_id)` - model-specific capacity data from cached `capacity/{model_id}` storage
- `GetAllModelMetrics()` - comprehensive metrics for all models including prices, utilization, and capacity

**System-Wide Queries**:
- `GetGracePeriodStatus()` - grace period status and countdown
- `GetDynamicPricingParams()` - current parameter values and configuration
- `GetCrossModelAnalytics()` - cross-model utilization comparisons and portfolio analytics

### 4.2. Governance Controls

All dynamic pricing parameters will remain adjustable through standard governance proposals, allowing the network to adapt the pricing mechanism as conditions change. Additionally, emergency governance procedures will allow temporary overrides of dynamic pricing during network stress or unexpected conditions.

### 4.3. Data Collection

The system will maintain historical records of utilization and pricing data in the chain state, enabling analysis of pricing effectiveness and parameter optimization over time.

## 5. Testing and Validation

The dynamic pricing mechanism will be thoroughly tested across different network conditions and load scenarios.

### 5.1. Simulation Testing

Unit tests will be added to validate price calculation algorithms under various utilization scenarios in `inference-chain/x/inference/keeper/dynamic_pricing_test.go`, including stability zone behavior and linear elasticity responses.

### 5.2. Integration Testing

End-to-end tests will be implemented in the `testermint` test suite to verify proper integration with block processing and existing tokenomics components, ensuring price adjustments follow the linear elasticity model correctly.

### 5.3. Gradual Rollout

The system will support feature flags and gradual rollout capabilities, allowing for careful deployment and monitoring in production environments.

## 6. Network Upgrade Plan

Activating the dynamic pricing system requires a coordinated network upgrade similar to other tokenomics enhancements. The upgrade process will:

1. **Parameter Migration**: Add new dynamic pricing parameters to the existing `EpochParams` structure with appropriate default values.

2. **Logic Integration**: Integrate dynamic pricing calculations into the existing epoch transition workflow.

3. **API Extensions**: Enhance existing pricing endpoints to support dynamic pricing information.

4. **Feature Activation**: The dynamic pricing system will be activated through a governance proposal after the upgrade is complete, allowing for proper testing and validation.

This ensures that the dynamic pricing system can be deployed safely without disrupting existing network operations, while providing clear migration paths for all network participants. 