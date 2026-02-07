# GIP-XXX: Transfer Agent Reward Sharing

## Summary

This proposal enables automatic reward sharing for Transfer Agents (TAs), allowing aggregators and gateway operators to earn a portion of inference fees by running their own TA node.

## Abstract

Currently, 100% of inference fees go to executors. This proposal adds a mechanism where Transfer Agents — which already serve as entry points for inference requests — receive a portion (10%) of executor payments. This incentivizes the creation of user-friendly API gateways, SDKs, and aggregators that expand Gonka's reach.

## Motivation

### Problem

1. **High barrier to entry for developers**: Using Gonka directly requires understanding ECDSA signing, endpoint selection, and key management
2. **No incentive for aggregators**: Building API gateways is valuable but currently unrewarded
3. **Underutilized infrastructure**: Transfer Agents already proxy requests but have no economic incentive beyond altruism

### Solution

Leverage the existing Transfer Agent system to automatically split fees:

```
Current flow:
User → Transfer Agent → Executor → Executor gets 100%

Proposed flow:
User → Transfer Agent → Executor → Executor gets 90%, TA gets 10%
```

### Benefits

1. **For Developers**: Easier onboarding through user-friendly TAs
2. **For TA Operators**: Sustainable revenue model aligned with network usage
3. **For Executors**: More traffic through better distribution
4. **For Network**: Expanded ecosystem without adding module complexity

## Specification

### Changes to x/inference

This proposal modifies the existing inference payment system rather than introducing a new module.

#### New Constant

```go
// TransferAgentRewardBasisPoints defines the percentage of executor payment
// that goes to the Transfer Agent (1000 = 10%)
const TransferAgentRewardBasisPoints = 1000
```

#### Updated Payments Struct

```go
type Payments struct {
    ExecutorPayment      int64
    TransferAgentPayment int64 // NEW: TA's share of the payment
    // ... other existing fields remain unchanged
}
```

#### Payment Splitting Logic

```go
func splitPayments(payments *Payments, totalPayment int64) {
    if totalPayment <= 0 {
        payments.ExecutorPayment = 0
        payments.TransferAgentPayment = 0
        return
    }

    // TA share: totalPayment * basisPoints / 10000, with explicit overflow protection.
    if totalPayment > math.MaxInt64/TransferAgentRewardBasisPoints {
        // Division-first fallback to avoid int64 overflow.
        taPayment := (totalPayment / 10000) * TransferAgentRewardBasisPoints
        payments.TransferAgentPayment = taPayment
        payments.ExecutorPayment = totalPayment - taPayment
        return
    }

    taPayment := (totalPayment * TransferAgentRewardBasisPoints) / 10000
    payments.TransferAgentPayment = taPayment
    payments.ExecutorPayment = totalPayment - taPayment
}
```

#### Payment Processing

When an inference completes:
1. Calculate total executor payment as before
2. Split payment: 90% to executor, 10% to Transfer Agent
3. Pay executor through existing mechanism
4. Pay TA through their participant balance (if registered)
5. If TA is not a registered participant, log warning and return the TA share back to the executor (graceful degradation)

### Parameters

| Parameter | Type | Default | Governance |
|-----------|------|---------|------------|
| `transfer_agent_reward_basis_points` | int64 | 1000 (10%) | Future PR |

Note: Currently implemented as a constant. A follow-up PR will make this a governable parameter in `TokenomicsParams`.

### No API Changes Required

Transfer Agents are already tracked in inference requests. No changes to the API or message formats are needed.

## Rationale

### Why Use Transfer Agents Instead of a New Module?

- **TAs already exist**: They serve as the entry point for inference requests
- **No registration needed**: TAs are already registered in the system
- **Simpler implementation**: Reuse existing infrastructure
- **Feedback-driven**: Based on review from @gmorgachev

### Why 10% Default?

- Higher than original 5% proposal to make TA operation more attractive
- Still leaves 90% for executors who do the computational work
- Uses basis points (1000/10000) for precision and future adjustability

### Why Basis Points?

- Industry standard for fee calculations
- Allows fine-grained control (0.01% precision)
- Avoids floating-point issues

### Backward Compatibility

- Inferences without a Transfer Agent work exactly as before (100% to executor)
- Existing TA implementations continue to work
- No breaking changes to APIs or message formats

## Implementation

Reference implementation in this PR:

Key files:
- `x/inference/calculations/inference_state.go` - `splitPayments()` function and constants
- `x/inference/calculations/inference_state_test.go` - Unit tests for payment splitting
- `x/inference/keeper/msg_server_start_inference.go` - Integration with payment flow

### Test Coverage

- Unit tests for `splitPayments()` with various edge cases
- Unit tests for `setEscrowForFinished()` with TA payment
- All existing inference calculation tests pass

## Security Considerations

1. **Integer overflow**: Explicit overflow protection in `splitPayments()` to avoid `int64` overflow
2. **Invalid TA addresses**: Graceful degradation if TA not registered as participant
3. **Zero/negative amounts**: Explicit checks in `splitPayments()`
4. **No custody risk**: Fees go directly to participant balances

## Future Improvements

1. Make `TransferAgentRewardBasisPoints` a governable parameter in `TokenomicsParams`
2. Allow different reward tiers based on TA reputation/stake
3. Add TA statistics tracking (requests proxied, fees earned)

## Timeline

1. **Review period**: 7 days for community feedback
2. **Testnet deployment**: 14 days testing on testnet
3. **Mainnet activation**: After successful testnet validation

## Voting

- **Yes**: Approve Transfer Agent reward sharing
- **No**: Reject the proposal
- **Abstain**: Decline to vote
- **NoWithVeto**: Reject and burn deposit

## References

- [Tokenomics Document](https://gonka.ai/tokenomics.pdf)
- [Transfer Agent Documentation](https://docs.gonka.ai/transfer-agents)
- [Original x/relay proposal discussion](https://github.com/gonka-ai/gonka/pull/633)

---

## Appendix: How It Works

### For TA Operators

Simply run a Transfer Agent node. When users send inference requests through your TA, you automatically receive 10% of the executor payment.

```
User Request → Your TA → Executor
                 ↓
         10% of payment goes to your participant balance
```

### For Developers

No changes needed. Continue using the Gonka API as before. If you route through a Transfer Agent, that TA earns rewards automatically.

### Example Payment Flow

```
Inference completes with 1000 GONKA executor payment:
├── Executor receives: 900 GONKA (90%)
└── Transfer Agent receives: 100 GONKA (10%)
```
