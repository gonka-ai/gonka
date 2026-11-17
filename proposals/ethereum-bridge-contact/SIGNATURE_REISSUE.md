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

## Chain interface and lifecycle

The `inference` module exposes `MsgReissueBridgeSignature`, with the original
bridge `request_id` and the transaction `creator`. The corresponding CLI is:

```sh
inferenced tx inference reissue-bridge-signature ORIGINAL_REQUEST_ID --from OWNER
```

`ORIGINAL_REQUEST_ID` is the `req_...` string returned by the original bridge
request, not its hexadecimal BLS identifier. The handler derives the BLS key
with Keccak-256. The new message belongs to the ordinary bridge fee group.
The mint creator can reissue a mint; the original user or registered wrapped
token contract recorded at creation can reissue a withdrawal. The latter must
use the contract execution path to act as that contract. The message accepts
no replacement recipient, token, amount, destination or payload.

Eligibility is controlled by `Params.bridge_reissue_params.allowed_targets`.
For example, this is a fragment of the full inference parameter object, using
a placeholder address that must be replaced with a verified new deployment:

```json
{
  "bridge_reissue_params": {
    "allowed_targets": [
      {"chain_id": "ethereum", "contract_address": "0x1111111111111111111111111111111111111111"}
    ]
  }
}
```

Use the existing authority-gated `MsgUpdateParams` governance flow, preserving
the other inference parameters. The target must also be present in the bridge
registry. Addresses in this parameter are lowercase, nonzero 20-byte hex;
there can be at most 32 distinct chain/address pairs. No target is enabled by
default, and the upgrade does not add one. Removing an entry or its bridge
registration prevents subsequent reissue requests. On-chain configuration
cannot itself prove the EVM bytecode's global replay semantics: that verification
is a prerequisite to approving a target.

Only requests created while their destination is enabled receive immutable
`reissue_signers` metadata. Enabling a target later does not change historical
requests. The handler also verifies the saved source-chain hash, operation and
destination against the current chain and configuration on every reissue.

A completed request can be reissued once the current signing epoch is at least
two epochs newer than its last completed signature. This permits renewal at
the inclusive expiry boundary, while the old proof can still be accepted by
the destination. Each request can attempt reissue at most once in any epoch;
the current epoch must have a usable BLS key and signing phase. This does not
require, or assume, synchronization of the destination's latest epoch.

The existing BLS `SigningStatus` query exposes `last_completed_epoch_id`,
`last_completed_signature`, `last_reissue_epoch_id` and `reissue_count`.
While collecting the replacement, the old proof remains in the two
`last_completed_*` fields. Success replaces it. Timeout, late submission or
aggregation failure restores the old `COMPLETED` state and its payload hash.
The attempted epoch remains recorded; completion cleanup is retried if needed.
None of those paths unlocks escrow or mints a refund. Old partial signatures
are removed before collection, and the replacement's hash binds the new epoch.

The normal `EventThresholdSigningRequested` event triggers existing DAPI BLS
signers for the new epoch; `bridge_signature_reissued` records admission and
`threshold_signing_reissue_restored` records restoration after failure.
The DAPI processes every signing request in a transaction, checking that the
parallel event attributes align before attempting any signature. A failure in
one request does not suppress subsequent requests. It also forwards requested
events from `FinalizeBlockEvents` through the same signing handler, so ordinary
initial-signature retries emitted in EndBlock are not lost. Reissue failures
still restore the completed proof instead of entering that ordinary retry path.

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

The new message, state fields and lifecycle rules require a coordinated chain
upgrade, not a rolling binary patch. The branch targets `upgrade-v0.2.18` and
includes the BLS-to-inference hook wiring and single-emission fixes from #1836.
The completion retry cap in #2155 should be validated with that wiring.

## Local verification

Run `npm test -- --network hardhat`. The security suite builds real BLS keys
and signatures with `@noble/curves`, independently of the contract, and exercises
the local EVM's EIP-2537 map and pairing precompiles. It checks successful WGNK,
ETH and ERC-20 redemption; same- and cross-epoch replay; replacement after
expiry; the inclusive two-epoch boundary; payload, operation, chain and
contract binding; and rollback when an asset transfer fails. The arithmetic
harness only exposes `_negateG1` to test canonical field boundaries, including
`y = 0`, `p - 1`, `p` and `p + 1`.

Chain keeper and application tests separately cover authorization, immutable
payload checks, real BLS aggregation, completion cleanup, balances, expiry
restoration and message routing. Run the affected chain suites with
`go test ./app/... ./x/bls/... ./x/inference/...` from `inference-chain`.
These local checks do not constitute a deployment or a legacy migration plan.
