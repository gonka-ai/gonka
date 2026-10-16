# Warm keys in v7: rotation and deterministic catch-up

Design note for the v7 item "warm keys handling refactoring: make them
available to rotate and be deterministic on catch-ups". Companion to the
catch-up rework (diff pulls and snapshots), which replays the same diffs on
every host and therefore needs the key decisions to be a function of the log.

**Status.** Draft, one pull request. The pure key-history model lands first
(`signing/keyhistory.go`, with this page); the state, wire, bridge, gateway and
dapi parts follow in the same branch, in the order under
[Implementation order](#implementation-order).

## What a warm key is

A host signs with its cold key (the slot address in the escrow group) or with a
warm key: an account the cold key authorised on chain through an authz
`GenericAuthorization` for `MsgStartInference`. The chain answers "who are the
grantees of this granter" through `GranteesByMessageType`
(`inference-chain/x/inference/keeper/query_grantees_by_message_type.go`),
dropping expired grants. Both binaries verify host signatures with
`signing.SlotActors.Allows` (`signing/actors.go`): the cold key, or the warm key
bound to that slot, or, while the slot has no binding, a warm key bound to a
sibling slot of the same validator, or a live chain lookup.

## What v6 does and why it cannot rotate

- State holds one binding per slot, `WarmKeys map[slot]address`
  (`state/machine.go`). It is written on first use: a signature by an unknown
  key triggers `ResolveWarmKey`, which asks the chain and, on success, binds the
  key (`setWarmKeyLocked`). The bindings are hashed into the state root
  (`state/hash.go`, `computeWarmKeysHash`), so they are consensus data.
- A bound slot is exclusive: after the first binding, only the cold key and
  that one warm key are accepted (`SlotActors.Allows`). A host that grants a new
  warm key and signs with it is refused by every peer for as long as the escrow
  lives. There is no revoke either.
- Each host decides when to ask the chain. Issue #913 made replay deterministic
  on one host by storing the binding next to the diff (`DiffRecord.WarmKeyDelta`,
  `types/domain.go`) and injecting it on recovery (`user/recover.go`,
  `session/manager.go`). The wire `Diff` (`transport/v1/session.proto`) carries
  no such field, so every peer still performs its own first lookup, at its own
  time, against its own chain node.
- The bridge caches a lookup for ever (`bridge/grpc.go`, `warmCache`;
  `cmd/devshardd/bridge/chain.go`). A revoked key stays accepted by a host that
  cached it and stays unknown to one that did not.

## Model

A slot has a history of bindings instead of one binding:

```
WarmKeyBinding { Addr, FromNonce, Height, RevokedAtNonce }
WarmKeyHistory  map[slot][]WarmKeyBinding
```

`FromNonce` is the diff nonce whose application introduced the binding;
`Height` is the chain height at which the grant was known to exist;
`RevokedAtNonce` is the nonce of the revoke entry, zero while the key is live.

The acceptance rule for a signature produced at nonce N is a function of the
history and of N only (`WarmKeyHistory.Allows`): the cold key; a warm key
whose binding for that slot covers N; while the slot has no binding covering
N, a key that covers N on a sibling slot of the same validator. No chain call,
no wall clock. Replaying the same diffs on any host yields the same answer.

Two keys may cover the same nonce range on one slot. That is the rotation
window: the old grant stays valid until its revoke entry, so a signature made
before the revoke still verifies on replay.

The history replaces `WarmKeys` in `EscrowState`, in the state-root hash (a new
hash version, since the input changes) and in the snapshot
(`proto/devshard/v1/snapshot.proto`: a new `warm_key_history` field; the old
`warm_keys` map stays readable and converts to single live bindings with
`FromNonce = 0`).

## How a binding enters consensus

Only through a diff. `DiffContent` (`proto/devshard/v1/diff.proto`) gains two
transactions:

```
MsgWarmKeyBind   { slot, address, height }
MsgWarmKeyRevoke { slot, address, height }
```

The gateway is the sequencer and the only author of these entries. When a host
signs with a key that the history does not cover, the gateway checks the grant
through its grant source ([below](#grant-source)) and, if the grant exists at
height H, includes `MsgWarmKeyBind{slot, key, H}` in the next diff. Applying
the diff appends the binding with `FromNonce` = that nonce. A revoke entry is
produced from a revoke or expiry seen in the grant source.

A host does not trust the gateway for this. Before it accepts signatures by a
newly bound key it verifies the grant itself: the grant exists at a height at
or after `H`, from its own grant source. Today's `ResolveWarmKey` lookup moves
here, with one difference: the lookup verifies a binding that is already in
the log, it does not create one. A host that cannot verify yet (its chain node
is behind `H`, its dapi is down) applies the diff, keeps the binding marked
unverified, and refuses signatures by that key until it can verify; the diff
itself stays valid, because the gateway's signature and, once finalized, the
quorum signatures over the post-state root cover it. The height in the entry
is what makes "exists at or after H" a well-defined question for every host,
whenever it gets to ask it.

## Grant source

The grant source answers "which keys does granter G authorise, and since which
height" without a chain round trip on the apply path.

1. dapi host events. `GetHostEvents` (`common/nodemanager/nodemanager.proto`)
   gains `HOST_EVENT_KIND_KEY_GRANTED` and `HOST_EVENT_KIND_KEY_REVOKED` with
   `{granter, grantee, msg_type_url, height, expiration}`, produced by the dapi
   event listener from the chain's authz `EventGrant` / `EventRevoke`, plus a
   snapshot of the current grants with the height it was taken at when the
   long-poll starts. devshardd keeps a local grant log from this stream, the
   way it keeps the escrow cache today (`cmd/devshardd/hostevents_sink.go`).
2. Direct chain query, as today, when dapi is unavailable or does not serve the
   event kinds (an old dapi answers the request without them).

The bridge's forever-cache goes away; the grant log is the cache and it sees
revocations.

## Rotation, step by step

1. The operator grants a new warm key on chain (dapi admin flow, unchanged).
2. The host's devshardd starts signing with it. Peers refuse until the binding
   is in the log; the gateway's next diff carries `MsgWarmKeyBind`.
3. Peers apply the diff, verify the grant, accept the new key from that nonce.
4. The operator revokes the old grant. The gateway sees the revoke in its grant
   source and emits `MsgWarmKeyRevoke`; from that nonce the old key is refused.
   Signatures from before it keep verifying on replay.

## Catch-up and snapshots

A lagging host pulls diffs or a snapshot. Both carry the history, so the host
ends with the same key state as everyone else without touching the chain. The
verification mark is local and not part of consensus: a host restoring from a
snapshot re-verifies live bindings against its grant source in the background
and refuses signatures by a binding only while that check is pending.

## Compatibility

The new diff transactions are understood by v7 children only, so this is a
protocol version, not a patch to v6. The gateway emits them only on v7
sessions. Snapshots keep the old map for reading.

## Open questions

- Verification at height. A node that prunes history cannot answer "was the
  grant there at H". The rule above is "exists at or after H", which the current
  state answers; a host whose node is behind H waits. If a grant is revoked
  between H and the host's check, the host sees it as never valid; the revoke
  entry that follows resolves the window, but the host refuses that key's
  signatures in between. Is that acceptable, or should the dapi grant log be the
  authority for the past?
- Several live keys per slot. The model allows it; the operator flow may want
  "one new, one old" only.
- Who may bind. The gateway as the sole author keeps determinism simple. A host
  that signs with a new key while the gateway is down waits; is that fine?

## Implementation order

Everything ships in one pull request; the steps are the commit order, each
one buildable and tested on its own.

1. `signing/keyhistory.go`: the pure model and its acceptance rule, with
   tests. No caller yet.
2. `types`, `state`: `EscrowState.WarmKeyHistory`, hash version, apply rules for
   the two transactions, snapshot codec, `SlotActors` built from the history.
3. `bridge`: grant log fed by dapi host events, direct-query fallback.
4. `cmd/devshardctl`: bind and revoke proposals in the sequencer; admin listing.
5. `cmd/devshardd`, `user`: verification mark, refusal while unverified, replay
   and snapshot paths.
6. dapi: event kinds, snapshot of grants, event listener.
7. Tests: rotation mid-session; lagging host replaying a bind and a revoke;
   dapi down; snapshot restore; a bind the host cannot verify yet.
