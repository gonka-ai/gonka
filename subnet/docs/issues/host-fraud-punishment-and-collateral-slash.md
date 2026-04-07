# Issue: Host fraud punishment and collateral slashing protocol

## Summary

Propose a protocol and chain message flow to punish fraudulent hosts: submit proof to mainnet, mark host as cheater, slash collateral, block participation in new subnets, and remove mining/reward eligibility.

## Target outcomes

- Fraudulent host is penalized economically (collateral slash/forfeit).
- Fraudulent host loses reward eligibility for affected period/policy window.
- Fraudulent host is blocked from joining new subnet assignments.
- Existing sessions can exclude fraudulent host immediately.

## Protocol proposal scope

1. **Mainnet cheater message**
   - Define special dispute/punishment message for host cheating.
   - Include evidence bundle, host identity, escrow/subnet context, quorum attestation.

2. **Verification rules**
   - Deterministic evidence verification.
   - Quorum/authorization checks for submitter set.
   - Replay protection and dispute window constraints.

3. **Penalty execution**
   - Collateral slash mechanics.
   - Reward suppression / mining eligibility restrictions.
   - Join-block rules for new subnets.

4. **State propagation**
   - Surface host-cheater status via query/event for schedulers and subnet runtime.
   - Ensure local devshard exclusion follows chain outcome.

## Related

- [`host-fraud-detection-and-exclusion.md`](./host-fraud-detection-and-exclusion.md)
- [`dispute-protocol-spec.md`](./dispute-protocol-spec.md)
- [`host-integrity-and-availability-umbrella.md`](./host-integrity-and-availability-umbrella.md)

## Status

Open.
