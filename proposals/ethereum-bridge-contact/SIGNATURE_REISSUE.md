# Signature reissue and activation

This contract is a new-deployment version. Its global replay protection makes
reissuing the same request under another epoch safe **at the same contract**.
It does not modify the deployed immutable bridge, migrate its WGNK supply, or
make signatures bound to an old contract redeemable at a new one.

## Why expiry needs reissue

`withdraw` and `mintWithSignature` reject a signature when its epoch is more
than two epochs behind `latestEpochId`. This limits exposure to old epoch keys,
but a completed Gonka signing request cannot simply be cancelled: its signature
may already have been redeemed. A delayed user therefore needs another
signature for the same operation, not an automatic refund or a new request id.

The chain's reissue path must preserve the request id, target chain, bridge
address, recipient, operation, token address and amount. Only the signing epoch
changes. Both signatures can be valid at the same time; `_processedRequests`
is keyed only by request id, so either one can execute first and every later
attempt is rejected. The legacy `processedRequests(epochId, requestId)` and
`isRequestProcessed(epochId, requestId)` views both expose this global state.

A failed reissue must retain the last successful signature and its epoch. It
must not make a previously completed operation eligible for cancellation or
refund. This restriction also applies while the replacement is being signed.

## Activation requirements

Prepare these steps together, but execute them only through the project's
deployment and governance process:

1. Deploy the new contract in `ADMIN_CONTROL`. Verify the target chain,
   immutable chain identifiers, deployed code and global request-id semantics.
2. Activate chain support for reissue and explicitly approve the new target
   chain and bridge address. Reissue eligibility must be recorded when new
   requests are created, before completion hooks remove pending-refund data.
   An empty approved-target list keeps reissue disabled.
3. Initialize the new contract's keys and enable normal operation only after
   that chain support is available. New requests must target this address;
   existing requests must keep their original destination.

The existing bridge's deduplication is epoch-scoped. Re-signing its requests
under a new epoch can therefore permit a second redemption. Do not approve it
for this reissue flow, import historical requests into the new contract, or
assume a signature became invalid merely because many epochs passed. A
separate legacy-recovery/migration design would need proof that the old claim
can no longer be used; this change implements neither migration nor refunds.

## Local verification

Run `npm test -- --network hardhat`. The security suite builds real BLS keys
and signatures with `@noble/curves`, independently of the contract, and exercises
the local EVM's EIP-2537 map and pairing precompiles. It checks successful WGNK,
ETH and ERC-20 redemption; same- and cross-epoch replay; replacement after
expiry; the inclusive two-epoch boundary; payload, operation, chain and
contract binding; and rollback when an asset transfer fails. The arithmetic
harness only exposes `_negateG1` to test canonical field boundaries, including
`y = 0`, `p - 1`, `p` and `p + 1`.

These tests verify contract behavior. The chain's authorization, lifecycle and
failure handling require their own keeper and application integration tests
before deployment.
