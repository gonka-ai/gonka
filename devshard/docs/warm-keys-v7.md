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
`GenericAuthorization`. The grant marker is `MsgClaimRewards`
(`WarmKeyGrantMarkerTypeURL`, `inference-chain/x/inference/types/authz.go`); the
`MsgStartInference` marker devshard still queries is legacy, and
`GranteesByMessageType`
(`inference-chain/x/inference/keeper/query_grantees_by_message_type.go`)
translates the legacy query to the current marker and drops expired grants.
Anything that reads raw authz events must filter on the current marker itself. Both binaries verify host signatures with
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
- Three caches keep a lookup for ever: the bridge (`bridge/grpc.go`,
  `warmCache`; `cmd/devshardd/bridge/chain.go`), the gateway session's
  `warmChecks` (`user/session.go`, `warmKeyAuthorized`) and the host's roster
  entry `peers` for payload fetches (`cmd/devshardd/session/manager_escrow_lookup.go`,
  `payloadPeerAllowed`, positive and negative). A revoked key stays accepted
  where it was cached, a new grant stays refused where its absence was cached.
- The chain checks warm signers of a settlement at submission time
  (`HasWarmKeyGrant`, `msg_server_settle_devshard_escrow.go`), against the
  grants of that moment, not against any history.

## Model

A slot has a history of bindings instead of one binding:

```
WarmKeyBinding { Addr, FromNonce, Height, RevokedAtNonce }
WarmKeyHistory  map[slot][]WarmKeyBinding
```

`FromNonce` is the diff nonce whose application introduced the binding;
`Height` is the chain height at which the grant was known to exist;
`RevokedAtNonce` is the nonce of the revoke entry, zero while the key is live.

The acceptance rule for a host signature is a function of the history and of
one nonce only (`WarmKeyHistory.Allows`): the cold key; a warm key whose
binding for that slot covers the nonce; while the slot has no binding covering
it, a key that covers it on a sibling slot of the same validator, unless the
key was revoked on this slot at or before it. No chain call, no wall clock.
Replaying the same diffs on any host yields the same answer.

The nonce is the **authorization nonce**: the nonce of the diff that carries
the signed message. Receipts, finishes, votes and HeightAcks do not sign a
production time, and they do not need one. A message signed by a key that is
live at the diff that includes it is accepted; a message the gateway includes
after the key's revoke is refused, whenever it was produced. The gateway is
the sequencer, so it decides what it includes before a revoke, and a host that
rotates keeps the old key live until its pending messages are through
([Rotation](#rotation-step-by-step)).

Two keys may cover the same nonce range on one slot. That is the rotation
window: the old grant stays valid until its revoke entry, so a diff that was
valid when applied stays valid on replay.

Every verifier that takes `signing.SlotActors` today (HeightAck, repair,
gossip, settlement) moves to a rule built from the history for the diff at
hand. A projection that keeps one key per slot is not an option: it would
refuse the old key inside the rotation window, including from another HA
replica of the same host.

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

The gateway is the sequencer and the only author of these entries, but it is
not the authority for them: its signature authenticates the diff, not the
grant, and a gateway that could bind a key of its own to a host's slot could
forge that host's finishes, votes and HeightAcks. The entry therefore carries
a **proof that every host checks from the log alone**, so that applying a diff
never depends on local chain state, a local cache or the wall clock:

```
MsgWarmKeyBind   { slot, address, height, attestation }
MsgWarmKeyRevoke { slot, address, height, attestation }
```

The attestation is a signature by the slot's cold key over
`(escrow, slot, address, height, bind|revoke)`. The cold key is the account
that grants on chain, so it is the right authority, and every host knows it
from the group. The host obtains the attestation through its dapi, which
holds the account key for chain transactions, when the operator grants or
revokes (see [Open questions](#open-questions) for a cold key that is not
reachable). `height` records the chain height at which the grant existed when
the bind was requested; it is informational for operators and for the grant
source, not an input to acceptance.

Application is staged: the bind and revoke entries of a diff apply before its
other transactions, so a key bound at nonce N is live for the messages of
nonce N. Compose, the log-plane checks (`checkLogPlaneLocked`), application
and replay all evaluate host signatures against the history as of the diff
being applied, with that diff's own entries included. The gateway keeps one
rule on top for simplicity of reasoning: it includes a host's first message by
a new key no earlier than the diff after the bind.

The grant source ([below](#grant-source)) is the gateway's precondition for
proposing an entry and every host's monitoring of its own grants; it is not
on the apply path. A host that sees a bind it does not recognise in its grant
source logs it and raises a metric, and otherwise applies the diff like any
other, because the attestation is the proof.

## Grant source

The grant source answers "which keys does granter G authorise, and since which
height" without a chain round trip on the apply path.

1. dapi host events. `GetHostEvents` (`common/nodemanager/nodemanager.proto`)
   gains `HOST_EVENT_KIND_KEY_GRANTED` and `HOST_EVENT_KIND_KEY_REVOKED` with
   `{granter, grantee, height, expiration}`, produced by the dapi event
   listener from the chain's authz `EventGrant` / `EventRevoke` filtered on
   the current marker, with the legacy marker translated as the query does.
   The stream keeps its cursor and `needs_reset` contract: on a reset, and at
   the first poll, dapi answers with a snapshot of the current grants stamped
   with the height and cursor it was taken at, and devshardd replaces its
   grant log with that snapshot before it applies later events. A grant log is
   therefore never older than the last reset. devshardd keeps the log the way
   it keeps the escrow cache today (`cmd/devshardd/hostevents_sink.go`).
2. Direct chain query, as today, when dapi is unavailable or does not serve the
   event kinds (an old dapi answers the request without them), with a TTL
   instead of a forever-cache.

The three forever-caches ([above](#what-v6-does-and-why-it-cannot-rotate)) go
away: the bridge's `warmCache`, the session's `warmChecks` and the roster
`peers` decisions all read the grant log, which sees revocations, or the
history, which is consensus.

## Rotation, step by step

1. The operator grants a new warm key on chain (dapi admin flow, unchanged)
   and dapi produces the bind attestation with the account key.
2. The host's devshardd sends the attested bind to the gateways of its
   escrows and starts signing with the new key. Peers refuse the key until the
   binding is in the log; the gateway's next diff carries `MsgWarmKeyBind`.
3. Peers apply the diff; the new key is live from that nonce.
4. The old key stays live, and the host may keep using it, until every escrow
   that has signatures by it in flight is past them. The chain checks a
   settlement's warm signers against the grants at submission
   (`HasWarmKeyGrant`), so the old grant must stay on chain until the
   settlements signed by the old key are submitted, or the gateway re-collects
   those signatures with the new key. The operator revokes on chain only then;
   dapi produces the revoke attestation, the gateway emits
   `MsgWarmKeyRevoke`, and from that nonce the old key is refused. Diffs from
   before it replay unchanged.

## Catch-up and snapshots

A lagging host pulls diffs or a snapshot. Both carry the history, and every
bind and revoke carries its attestation, so the host ends with the same key
state as everyone else without touching the chain, however long it was away
and whatever has been revoked or pruned on chain since. Nothing is "pending
verification" on the apply path.

## Compatibility

The new diff transactions are understood by v7 children only, so this is a
protocol version, not a patch to v6. The gateway emits them only on v7
sessions. Snapshots keep the old map for reading.

## Open questions

- The attestation key. The design signs binds with the cold key through dapi,
  where the account key lives for chain transactions. If a deployment keeps
  that key elsewhere, the alternative is a chain proof of the grant (a Merkle
  proof against a block header the hosts can verify), which is heavier and
  depends on the light-client story; or an attestation by the old warm key for
  a rotation, with the cold key needed only for the first bind.
- Settlement and rotation. `HasWarmKeyGrant` checks grants at submission. The
  rule above makes the operator keep the old grant until pending settlements
  are through. The alternative is a chain change that accepts signers granted
  at the escrow's epoch; that belongs to the 0.2.18 settlement branch.
- Several live keys per slot. The model allows it; the operator flow may want
  "one new, one old" only.
- Who may bind. The gateway as the sole author keeps sequencing simple. A host
  that rotates while the gateway is down waits with the old key; is that fine?
- A slot reassigned to another validator in a later epoch, and HA replicas of
  one host that share a warm key, follow the same rule (the history is per
  slot and the attestation is per cold key); both need a test.

## Implementation order

Everything ships in one pull request; the steps are the commit order, each
one buildable and tested on its own.

1. `signing/keyhistory.go`: the pure model and its acceptance rule, with
   tests. No caller yet.
2. `types`, `state`: `EscrowState.WarmKeyHistory`, hash version, the two
   transactions with their attestation, staged application, journal undo/redo,
   snapshot codec, every verifier (HeightAck, repair, gossip, settlement)
   reading the history for the diff at hand.
3. `bridge`: grant log fed by dapi host events with the reset rule, direct-query
   fallback with a TTL; the three forever-caches removed.
4. `cmd/devshardctl`: bind and revoke proposals in the sequencer with the
   "first use after the bind" rule; admin listing.
5. `cmd/devshardd`, `user`: the host's attestation request to dapi, sending the
   bind, replay and snapshot paths.
6. dapi: attestation endpoint, event kinds, grant snapshot on reset, event
   listener on the current marker.
7. Tests: rotation mid-session; lagging host replaying a bind and a revoke
   after the grant was revoked and pruned on chain; dapi down; snapshot
   restore; a settlement with old-key signatures across a rotation; a slot
   reassigned to another validator; two HA replicas sharing a key.
