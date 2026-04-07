# Issue: Unreachable host exclusion, missed-inference accounting, and return to work

## Summary

When a host is unreachable (detected via start/timeout reachability voting), exclude it from new work scheduling while continuing missed-inference accounting. Allow a controlled return-to-work signal for current devshard participation before hard penalties.

## Current context

- Unreachability is detected in timeout/refusal verification flow where hosts vote on executor reachability.
- Today, short-term behavior should avoid assigning jobs to unreachable hosts.

## Required short-term behavior

1. **Detect and mark unreachable**
   - Transition host to `unreachable` status based on quorum-reachable checks.

2. **Exclude from scheduling**
   - Do not schedule new inferences to unreachable host.

3. **Keep accounting**
   - Continue counting missed rounds/inferences while host stays unreachable.

4. **Return-to-work path**
   - Host may gossip/sign a readiness message proving it is back online.
   - After quorum acceptance, host can rejoin scheduling in the same devshard if not permanently punished.

5. **Boundary with fraud**
   - Unreachable is recoverable.
   - Fraud-disputed is non-recoverable in-session unless governance/protocol override exists.

## Longer-term alignment

This temporary exclusion/rejoin flow should later map to dynamic participant lifecycle:
[`devshard-dynamic-participants.md`](./devshard-dynamic-participants.md).

## Related

- [`timeout-flows-refusal-and-execution.md`](./timeout-flows-refusal-and-execution.md)
- [`subnetctl-network-errors-refusal-fast-path.md`](./subnetctl-network-errors-refusal-fast-path.md)
- [`host-integrity-and-availability-umbrella.md`](./host-integrity-and-availability-umbrella.md)

## Status

Open.
