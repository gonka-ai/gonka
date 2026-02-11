# Transaction Batching

## Problem

Each inference requires 3 on-chain transactions: `MsgStartInference`, `MsgFinishInference`, `MsgValidation` (from 0 to many).

Current constraints (6s blocks, ~10 MB for inference txs):
- Per-inference: ~2 KB across 3 transactions
- Transaction count bottleneck: ~3,000 txs/block
- Current throughput: ~1,000 inferences/block = ~600K/hour

Block size is underutilized. Transaction count is the limiting factor.

## Throughput with Batching

Batching 100 inferences per transaction:
- Removes tx count bottleneck (3,000 txs = 100K inferences)
- Block size becomes new ceiling

| Scenario | Bottleneck | Inferences/hour |
|----------|------------|-----------------|
| Current (3 txs/inference) | Tx count | 600K |
| Batched, no optimization | Block size | 11M |
| Batched + data optimization | Block size | 27M |

## Data Optimization (Custom Proto)

For maximum throughput, custom batched message types can reduce per-inference size by ~60%:

**Address deduplication:** Store unique addresses once, reference by index.
- Current: 4 addresses × 45 bytes = 180 bytes/inference
- Optimized: 4 indices × 1 byte = 4 bytes/inference

**Binary encoding:** Raw bytes instead of hex/base64 strings.
- Hashes: 32 bytes vs 64 bytes (hex)
- Signatures: 64 bytes vs 88 bytes (base64)

**Shared fields:** Extract common fields to batch level.
- `creator`, `node_version`, `model` (if batching by model)

## Recommended Approach

**Phase 1: Native Cosmos SDK Multi-Message Transactions**

No chain changes required. API node packages multiple `MsgStartInference` into single transaction:

```go
txBuilder.SetMsgs(
    &MsgStartInference{...},
    &MsgStartInference{...},
    // ... up to 100
)
```

Benefits:
- Immediate implementation
- Removes tx count bottleneck
- ~18x throughput improvement

**Phase 2: Custom Batched Proto Types (if needed)**

If block size becomes limiting, implement optimized message types:

```proto
message MsgStartInferenceBatched {
    string creator = 1;
    repeated string executors = 2;
    repeated StartEntry entries = 3;
}

message StartEntry {
    string inference_id = 1;
    bytes prompt_hash = 2;
    uint32 executor_idx = 3;
    // ... minimal per-inference fields
}
```

Additional ~2.5x improvement over Phase 1.
