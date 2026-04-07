# Issue: Host fraud detection and exclusion

## Summary

Define and implement detection of fraudulent host behavior, then exclude fraudulent hosts from active work and voting in the current devshard.

## Fraud classes (host-side)

- Signed message attribution abuse.
- Signed protocol-invalid messages (not strictly following protocol shape/order/rules).
- Malicious timestamp behavior (for example far-future values used to block protocol progress).
- Any other cryptographically attributable protocol violation by host.

## Desired behavior

1. Detect host fraud with canonical evidence.
2. Broadcast and persist evidence to peers.
3. Mark host as **fraud-disputed** in session state.
4. Immediately exclude disputed host from:
   - scheduling/executor selection,
   - protocol voting acceptance paths.
5. Forward evidence to dispute/punishment flow for chain-level consequences.

## Design notes

- Exclusion should be deterministic and consistent across hosts.
- Evidence must be replay-safe and bounded in storage.
- Fraud-disputed status should be strictly stronger than temporary unreachable status.

## Dependencies

- [`host-integrity-and-availability-umbrella.md`](./host-integrity-and-availability-umbrella.md)
- [`host-fraud-punishment-and-collateral-slash.md`](./host-fraud-punishment-and-collateral-slash.md)
- [`dispute-protocol-spec.md`](./dispute-protocol-spec.md)
- [`user-fraud-detection-scope.md`](./user-fraud-detection-scope.md)

## Status

Open.
