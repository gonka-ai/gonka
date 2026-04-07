# Issue: Implement protocol E2E testing (testenv harness)

## Summary

Implement the **subnet protocol E2E testing** approach described in **`subnet/docs/proposals/PROTOCOL_TESTING_PROPOSAL.md`**: Python (or equivalent) driver, **dev-only control-plane** on `mock-server` and `subnethost`, **observers** on subnetctl and participant GET endpoints, and **scenarios** (refusal-timeout, execution-timeout, recover-on-retry, adversarial hosts).

## Deliverables (from proposal)

- Harness under **`subnet/testenv`** (or agreed path).
- **`PUT /test/rules`** (or similar) on mock-server HTTP sidecar.
- **`POST /test/faults`** (or similar) on `subnethost` for per-handler / per-nonce behavior.
- CI job with compose stack optional but desired.

## Related

- **Proposal:** [`../proposals/PROTOCOL_TESTING_PROPOSAL.md`](../proposals/PROTOCOL_TESTING_PROPOSAL.md)
- **Testenv context:** [`../proposals/TESTENV_PROPOSAL.md`](../proposals/TESTENV_PROPOSAL.md)

## Status

Open — meta-issue for execution of the proposal.
