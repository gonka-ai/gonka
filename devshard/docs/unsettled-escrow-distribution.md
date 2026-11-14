# Unsettled escrow distribution

A group has sixteen slots. Settlement of a devshard escrow needs `2*n/3+1` signatures over the final state, which is eleven. Six slots that never sign leave ten signatures, and the chain rejects the settlement.

An escrow that is still unsettled when its epoch is pruned has its full amount split equally across the slots. `DevshardPruningThreshold` is two epochs, so the split runs once the epoch is that far behind the current one. Each slot receives `amount / 16`. A participant that holds several slots receives that many shares. The integer remainder stays in the module account. Misses and invalidations are not applied, and the creator is not refunded.

A settled escrow pays hosts for the work they signed and returns the rest to the creator. The unsettled split pays the whole escrow to the slots. A set of slots can therefore collect more by agreeing not to sign than they would under settlement.

This is a deliberate temporary rule. Devshard is still experimental, and miss and validation records are not yet trusted enough to cut a host's reward from them automatically. While that is true, settlement is submitted by hand, so a host is not left unpaid because a miss or a validation was wrong. Splitting an escrow that never settles is the same choice: the funds go to the hosts rather than being held back. It is not the target design. The target is a settlement that pays for served work and applies misses and invalidations once those records are reliable.

The rule lives in `DevshardQuorumFor` (`inference-chain/x/inference/keeper/devshard_settlement.go`) and `distributeUnsettledEscrow` (`inference-chain/x/inference/keeper/devshard_pruning.go`). The short note is in [attacks.md](./attacks.md).
