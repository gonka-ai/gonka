# Exact inference accounting in v7

A nonce identifies a diff. It can carry a start, a finish, a timeout, a
heartbeat, or other service work. Counting every nonce as an inference
overstates workload and changes the missed/inference ratio.

For numeric protocol versions v7 and later, each slot's cumulative
`HostStats.Assigned` counts accepted `MsgStartInference` messages. The executor
is selected by the existing `inference_id % slot_count` routing rule. Rejected
transactions and speculative previews do not change the committed count.
Confirmations, finishes, timeouts, heartbeats and empty drain diffs do not
increment it. Sealing an inference does not subtract it.

The counter is part of the host-stats hash (protobuf field 9), snapshots,
gateway settlement JSON, and the existing settlement transaction. All hosts
sign the same state root covering it. The chain checks the routing-derived
nonce count as an upper bound, and validates `missed <= assigned` and
`invalid <= assigned - missed`. A zero v7 counter means zero work; it never
falls back to the nonce count.

Participant epoch accounting retains its existing meaning:

- `InferenceCount += assigned - missed`;
- `MissedRequests += missed`;
- `InvalidatedInferences += invalid`;
- `ValidatedInferences += assigned - missed - invalid`.

These rules do not change sampled pass policies. `Finished` and `Validated`
remain distinct existing counters. Nonce ordering, max-nonce limits, per-nonce
fees, the settlement RPC and signature envelope remain unchanged.

## Deployment and compatibility

Deploy the chain change with the v7 chain upgrade before permitting v7
settlements, and deploy matching v7 host and gateway binaries. The escrow's
frozen state-root/protocol tag selects accounting; numeric v7+ tags enable
Assigned, while named tags such as `dev` and pre-v7 tags keep legacy nonce
accounting. The image release tag is not the protocol tag. Configure and
approve the intended v7 protocol tag through the existing release process.

Start new v7 escrows with these binaries. Do not continue an earlier v7
prototype escrow whose history lacks Assigned: its old signed roots cannot
be backfilled without changing them. Restore rejects missing counters when
nonempty live or sealed inference history reveals the inconsistency. There
is no migration that guesses counts from nonce.

Pre-v7 escrows keep Assigned at zero and retain their old host-stats encoding
and accounting. Never relabel an existing escrow as v7. Existing snapshots
written by the matching version and replayed diffs retain the counter;
there is no new database column or table. Mixed old/new binaries within a
v7 escrow will disagree on state roots after the first start, so update
hosts and gateways together before creating v7 escrows.

This PR also aligns the chain's existing `Validated` (field 7) and `Finished`
(field 8) protobuf fields with the runtime. These numbers are covered by the
root but do not introduce a new sampling policy.
