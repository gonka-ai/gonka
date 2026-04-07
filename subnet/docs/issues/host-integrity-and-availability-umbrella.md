# Issue: Host integrity and availability — umbrella

## Summary

Track host-side faults that must lead to exclusion from active work, with separate handling for:

1. **Fraudulent host behavior** (protocol-invalid signed messages, attribution abuse, malicious timestamp use, or any message not strictly following protocol).
2. **Unreachable host behavior** (temporary liveness failure where host can later recover and rejoin current devshard work).

Short-term devshard policy should prevent scheduling work to excluded hosts and reject their votes where required. Long-term behavior should fold into dynamic membership design.

## Child issues

- Fraud host detection and exclusion:
  [`host-fraud-detection-and-exclusion.md`](./host-fraud-detection-and-exclusion.md)
- Unreachable host exclusion and return-to-work:
  [`host-unreachable-exclusion-and-return.md`](./host-unreachable-exclusion-and-return.md)
- Punishment and collateral slash proposal:
  [`host-fraud-punishment-and-collateral-slash.md`](./host-fraud-punishment-and-collateral-slash.md)

## Relation to dynamic devshards

- Long-term membership and validator/slot lifecycle should follow:
  [`devshard-dynamic-participants.md`](./devshard-dynamic-participants.md)
- This umbrella defines short-term rules for current devshard behavior until dynamic membership is implemented.

## Short-term invariants

- Do not schedule inferences to hosts marked unreachable.
- Do not accept protocol votes from hosts marked as fraudulent/disputed.
- Keep unreachable-host accounting for missed inferences while excluded.
- Allow explicit return-to-work flow for unreachable hosts before permanent penalties.

## Status

Open.
