# Warm keys in v7: rotation and deterministic catch-up

Design note for the v7 item "warm keys handling refactoring: make them
available to rotate and be deterministic on catch-ups". Companion to the
catch-up rework (diff pulls and snapshots), which replays the same diffs on
every host and therefore needs the key decisions to be a function of the log.

**Status.** Draft, one pull request. The pure key-history model lands first
(`signing/keyhistory.go`, with this page); the chain, state, wire, bridge,
gateway and dapi parts follow in the same branch, in the order under
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
Both binaries verify host signatures with `signing.SlotActors.Allows`
(`signing/actors.go`): the cold key, or the warm key bound to that slot, or,
while the slot has no binding, a warm key bound to a sibling slot of the same
validator, or a live chain lookup.

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
- Each host decides when to ask the chain, and the chain answers from its
  current state, so two hosts asking at different times get different answers.
  Issue #913 made replay deterministic on one host by storing the binding next
  to the diff (`DiffRecord.WarmKeyDelta`, `types/domain.go`) and injecting it
  on recovery (`user/recover.go`, `session/manager.go`). The wire `Diff`
  (`transport/v1/session.proto`) carries no such field, so every peer still
  performs its own first lookup, at its own time, against its own chain node.
- Three caches keep a lookup for ever: the bridge (`bridge/grpc.go`,
  `warmCache`; `cmd/devshardd/bridge/chain.go`), the gateway session's
  `warmChecks` (`user/session.go`, `warmKeyAuthorized`) and the host's roster
  entry `peers` for payload fetches (`cmd/devshardd/session/manager_escrow_lookup.go`,
  `payloadPeerAllowed`, positive and negative). A revoked key stays accepted
  where it was cached, a new grant stays refused where its absence was cached.
- The chain checks warm signers of a settlement at submission time
  (`HasWarmKeyGrant`, `msg_server_settle_devshard_escrow.go`), against the
  grants of that moment, not against any history.

## The missing piece: a chain log that does not change its answer

Every part of the v6 problem comes from asking a question whose answer changes
with time: "is W a warm key of G" is true today and false after a revoke, so
two hosts asking at different moments disagree, and a host replaying old
diffs after a revoke cannot reproduce what the others decided.

v7 therefore asks the chain a question whose answer never changes. The
inference module keeps an append-only **warm-key log** per participant:

```
WarmKeyLogEntry { grantee, granted_at_height, revoked_at_height (0 while live), expires_at }
```

A grant appends an entry; a revoke, an expiry or a replacement sets
`revoked_at_height` on it; nothing is deleted. The query
`WarmKeyAt(granter, grantee, height)` answers "was grantee a warm key of
granter at height" from the log, and that answer is the same for ever, on
every node, however much state has been pruned since: the log is small
(one entry per grant) and lives in the module store like any other record.
`GranteesByMessageType` keeps working for current grants; `HasWarmKeyGrant`
gains a height so a settlement can name the height its signatures were made
under ([Rotation](#rotation-step-by-step)).

This is the chain part of the item. It belongs to the 0.2.18 chain branch that
collects the settlement changes for v7.

## Model

A slot has a history of bindings instead of one binding:

```
WarmKeyBinding { Addr, FromNonce, GrantedAtHeight, RevokedAtNonce, RevokedAtHeight }
WarmKeyHistory  map[slot][]WarmKeyBinding
```

`FromNonce` is the diff nonce whose application introduced the binding and
`RevokedAtNonce` the nonce of the revoke entry, zero while the key is live.
`GrantedAtHeight` and `RevokedAtHeight` point at the chain log entry: the
heights at which the chain recorded the grant and its end. A bind must name a
grant height above the height at which the same key was last revoked on the
slot, and a revoke must name a height above the grant it ends
(`WarmKeyHistory.Bind`, `Revoke`), so an old grant cannot bring a revoked key
back and a stale revoke cannot end a later binding.

The acceptance rule for a host signature is a function of the history and of
one nonce only (`WarmKeyHistory.Allows`): the cold key; a warm key whose
binding for that slot covers the nonce; while the slot has no binding covering
it, a key that covers it on a sibling slot of the same validator, unless the
key was revoked on this slot at or before it. No chain call, no wall clock.
Replaying the same diffs on any host yields the same answer.

The nonce is the **authorization nonce**, and each path has its own:

- Messages carried in diffs (receipts, finishes, votes, HeightAck, heartbeat):
  the nonce of the diff that carries them. They do not sign a production
  time and do not need one. A message signed by a key that is live at the
  diff that includes it is accepted; one the gateway includes after the key's
  revoke is refused, whenever it was produced. The gateway sequences, so it
  decides what goes in before a revoke, and a host that rotates keeps the old
  key live until its pending messages are through
  ([Rotation](#rotation-step-by-step)).
- State-signature gossip and settlement signatures sign a state nonce: that
  nonce.
- Repair requests and answers, and other admission on a session the receiver
  holds, are local decisions, not consensus: the receiver uses its history
  head. A key rotated in may repair an old turn; a revoked key may not,
  whatever `RefNonce` it names.
- Admission without a session (`ServeUnboundGetPayload`, `payloadRoster`,
  `scanRosterWarmKey`) has no history to read and uses the grant source
  ([below](#grant-source)) at the current height, as it does today, minus the
  forever-cache.

Two keys may cover the same nonce range on one slot. That is the rotation
window: the old grant stays valid until its revoke entry, so a diff that was
valid when applied stays valid on replay.

Every verifier that takes `signing.SlotActors` today (HeightAck, repair,
gossip, settlement) moves to a rule built from the history with the nonce of
its path. A projection that keeps one key per slot is not an option: it would
refuse the old key inside the rotation window, including from another HA
replica of the same host.

## How a binding enters consensus

Only through a diff. `DiffContent` (`proto/devshard/v1/diff.proto`) gains two
transactions:

```
MsgWarmKeyBind   { slot, address, granted_at_height }
MsgWarmKeyRevoke { slot, address, revoked_at_height }
```

The gateway is the sequencer and the only author of these entries, but it is
not the authority for them: its signature authenticates the diff, not the
grant. The authority is the chain log, and the entry names the log record by
its height. Every host checks the entry against the chain log before it
counts the key: `WarmKeyAt(cold key of slot, address, granted_at_height)`
must be true for a bind, and the log must show `revoked_at_height` for a
revoke. The answer is time-invariant, so every honest host that ever checks
gets the same one, and a host whose chain node or dapi is unavailable waits
for the answer rather than deciding without it: availability delays a host,
it never forks it.

A bind the chain log does not confirm is a gateway fault. The host refuses
the diff like any other invalid diff, does not sign the post-state root, and
the escrow stalls on that gateway: the same outcome as any other misbehaving
sequencer today, and nothing the gateway can do forges a host signature.

Application is staged: the bind and revoke entries of a diff apply before its
other transactions, so a key bound at nonce N is live for the messages of
nonce N. Compose, the log-plane checks (`checkLogPlaneLocked`), application
and replay all evaluate host signatures against the history as of the diff
being applied, with that diff's own entries included. The gateway keeps one
rule on top for simplicity of reasoning: it includes a host's first message by
a new key no earlier than the diff after the bind.

**Expiry.** A grant with an expiry is one more end in the chain log: the
module sets `revoked_at_height` when the expiry passes. The gateway sees it
in the grant source and emits the revoke like any other.

## Grant source

The grant source is the hosts' and the gateway's view of the chain log
without a chain round trip per question.

1. dapi host events. `GetHostEvents` (`common/nodemanager/nodemanager.proto`)
   gains `HOST_EVENT_KIND_KEY_GRANTED` and `HOST_EVENT_KIND_KEY_REVOKED` with
   `{granter, grantee, height, expires_at}`, produced by the dapi event
   listener from the module's warm-key log events on the current marker
   only. Events on the legacy marker are ignored: a legacy grant is stored
   separately on chain and its revoke says nothing about a `MsgClaimRewards`
   grant. The stream keeps its cursor and `needs_reset` contract: on a reset,
   and at the first poll, dapi answers with the full log for the participants
   the host asks about, stamped with the height and cursor it was taken at,
   and devshardd replaces its copy before it applies later events. devshardd
   keeps the copy the way it keeps the escrow cache today
   (`cmd/devshardd/hostevents_sink.go`).
2. Direct chain query, `WarmKeyAt` and the log, when dapi is unavailable or
   does not serve the event kinds (an old dapi answers the request without
   them).

A copy of an append-only log needs no TTL and no invalidation; the three
forever-caches above go away: the bridge's `warmCache`, the session's
`warmChecks` and the roster `peers` decisions read the log copy, or the
history, which is consensus.

## Rotation, step by step

1. The operator grants a new warm key on chain (dapi admin flow, unchanged).
   The module appends a log entry at height H1.
2. The host's devshardd starts signing with the new key. Peers refuse the key
   until the binding is in the log; the gateway sees the grant in its grant
   source and its next diff carries `MsgWarmKeyBind{slot, key, H1}`.
3. Peers apply the diff, confirm the entry against the chain log, and the new
   key is live from that nonce.
4. The old key stays live, and the host may keep using it, until every escrow
   that has signatures by it in flight is past them. A settlement submitted
   with old-key signatures names the height its signatures were made under,
   and `HasWarmKeyGrant` at that height answers from the log, so the operator
   does not have to keep the old grant on chain for pending settlements.
5. The operator revokes on chain at height H2. The gateway emits
   `MsgWarmKeyRevoke{slot, key, H2}`; from that nonce the old key is refused.
   Diffs from before it replay unchanged.

## Catch-up and snapshots

A lagging host pulls diffs or a snapshot. Both carry the history, and every
bind and revoke names a chain log entry whose answer does not change, so the
host ends with the same key state as everyone else, however long it was away
and whatever has been revoked since. A host that replays verifies the entries
against the log the same way a live host does; for finalized nonces it may
instead trust the quorum signatures over the post-state root that the
catch-up carries, since every honest signer verified the entry before
signing. Nothing is "pending verification" on the apply path, and nothing
depends on pruned state.

The snapshot (`proto/devshard/v1/snapshot.proto`) gains a
`warm_key_history` field; the old `warm_keys` map stays readable and
converts to single live bindings with `FromNonce = 0` and no grant height,
which later binds and revokes treat as height 0.

## Compatibility

The new diff transactions are understood by v7 children only, so this is a
protocol version, not a patch to v6. The gateway emits them only on v7
sessions. The chain log and query ship with the 0.2.18 chain upgrade; until
then the gateway cannot propose binds and v7 sessions run on cold keys and
the v6 single binding.

## Open questions

- Scope of the chain change: the warm-key log and `WarmKeyAt` in the
  inference module, `HasWarmKeyGrant` with a height for settlement, and a
  settlement message that names that height. This note assumes the 0.2.18
  branch takes them; if it cannot, the fallback is `GranteesByMessageType` at
  the current height with the determinism gap of v6 kept for replay.
- Several live keys per slot. The model allows it; the operator flow may want
  "one new, one old" only.
- Who may bind. The gateway as the sole author keeps sequencing simple. A host
  that rotates while the gateway is down waits with the old key; is that fine?
- Converted v6 bindings have no grant height; a later revoke of such a key
  must still be ordered. Treating their height as 0 works for ordering but
  loses the audit trail.
- A slot reassigned to another validator in a later epoch, and HA replicas of
  one host that share a warm key, follow the same rule (the history is per
  slot and the log is per cold key); both need a test.

## Implementation order

Everything ships in one pull request; the steps are the commit order, each
one buildable and tested on its own.

1. `signing/keyhistory.go`: the pure model and its acceptance rule, with
   tests. No caller yet.
2. `inference-chain`: the warm-key log, the `WarmKeyAt` query, log events,
   `HasWarmKeyGrant` with a height, the settlement message naming it.
3. `types`, `state`: `EscrowState.WarmKeyHistory`, hash version, the two
   transactions, staged application, journal undo/redo, snapshot codec, every
   verifier (HeightAck, repair, gossip, settlement) reading the history by its
   own nonce rule.
4. `bridge`: the log copy fed by dapi host events with the reset rule,
   `WarmKeyAt` and the log as the direct fallback; the three forever-caches
   removed.
5. `cmd/devshardctl`: bind and revoke proposals in the sequencer from the
   grant source, with the "first use after the bind" rule; admin listing.
6. `cmd/devshardd`, `user`: entry confirmation against the log before a key is
   counted, waiting on availability, replay and snapshot paths, the
   unbound-payload path on the log copy.
7. dapi: event kinds, log snapshot on reset, event listener on the module
   events.
8. Tests: rotation mid-session; lagging host replaying a bind and a revoke
   after the revoke, with the chain pruned; dapi down; snapshot restore; a
   settlement with old-key signatures across a rotation; a slot reassigned to
   another validator; two HA replicas sharing a key; a gateway proposing a
   bind the log does not confirm.
