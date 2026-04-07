# Issue: Refusal and execution timeout flows correctness

## Summary

Ensure **both** timeout paths behave correctly end-to-end: **refusal** (executor unreachable / never accepts) and **execution** (accepted but never finishes). Today **subnetctl** and session logic interact in ways that can **wait a long time** on soft network failures; behavior may need **protocol and proxy changes**.

## Related

- **Fast-path / classification of errors:** [`subnetctl-network-errors-refusal-fast-path.md`](./subnetctl-network-errors-refusal-fast-path.md)
- **E2E validation of timeouts and voting:** [`../proposals/PROTOCOL_TESTING_PROPOSAL.md`](../proposals/PROTOCOL_TESTING_PROPOSAL.md)

## Status

Open — under active discussion; implement tests per protocol-testing proposal as behavior stabilizes.
