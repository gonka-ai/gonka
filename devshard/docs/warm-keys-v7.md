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
WarmKeyLogEntry { id, grantee, granted_at_height, revoked_at_height (0 while live), expires_at }
```

A grant appends an entry with the next `id` for that granter; a revoke, an
expiry (the module ends the entry at the height the expiry passes) or a
replacement sets `revoked_at_height`; nothing is deleted. A replacement in
the same block as its predecessor gets a larger id, so ids order grants
where heights cannot. The query `WarmKeyEntry(granter, id)` returns the
entry, and that answer is the same for ever, on every node, however much
state has been pruned since: the log is small (one entry per grant) and
lives in the module store like any other record. `GranteesByMessageType`
keeps working for current grants.

At the upgrade that introduces the log, the module seeds it from the authz
grants that exist at that moment, one entry per current grant with
`granted_at_height` = the upgrade height and ids in a fixed order, so every
warm key in use has an entry to point at without a new grant transaction.
History from before the upgrade is not reconstructed; it is not needed,
because no v7 session exists before it.

This is the chain part of the item. It belongs to the 0.2.18 chain branch that
collects the settlement changes for v7.

## Model

A slot has a history of bindings instead of one binding:

```
WarmKeyBinding { Addr, FromNonce, GrantID, GrantedAtHeight, RevokedAtNonce, RevokedAtHeight }
WarmKeyHistory  map[slot][]WarmKeyBinding
```

`FromNonce` is the diff nonce whose application introduced the binding and
`RevokedAtNonce` the nonce of the revoke entry, zero while the key is live.
`GrantID` names the chain log entry the binding points at, and the two heights
are copied from it. A bind must name a grant id above every earlier grant of
the same key on the slot, and a revoke must name the live binding's grant id
(`WarmKeyHistory.Bind`, `Revoke`), so an old grant cannot bring a revoked key
back and a stale revoke cannot end a later binding.

The acceptance rule for a host signature is a function of the history, of one
nonce and of that diff's stamped height (`WarmKeyHistory.Allows`, with the
height rule applied by the state machine): the cold key; a warm key whose
binding for that slot covers the nonce **and whose grant is live at the
diff's stamped height** (the diff's height is below `revoked_at_height`, or
the entry has none); while the slot has no binding covering it, a key that
covers it on a sibling slot of the same validator, unless the key was revoked
on this slot at or before it. No chain call on the apply path, no wall clock.
The stamped height is the user-signed height the diff chain already carries
(`MsgHeartbeat.observed_height`, acknowledged by the hosts through
height-sync), so it is part of the log and every host reads the same value.
Replaying the same diffs on any host yields the same answer.

The height rule is what makes a revoke effective even when the gateway never
emits `MsgWarmKeyRevoke`: a host that has seen the entry's end in the chain
log refuses the key in every diff stamped at or past it. The revoke entry is
bookkeeping that ends the binding's nonce range and keeps the history
readable; the chain log is the authority for the end.

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
MsgWarmKeyBind   { slot, address, grant_id }
MsgWarmKeyRevoke { slot, address, grant_id }
```

The gateway is the sequencer and the only author of these entries, but it is
not the authority for them: its signature authenticates the diff, not the
grant. The authority is the chain log, and the entry names the log record by
its id. Every host checks the entry against the chain log before it counts
the key. For a bind in a diff stamped at height h: the entry `grant_id` of
the slot's cold key exists, its grantee is `address`, `granted_at_height` is
at or below h, and the entry is not ended at or before h. For a revoke: the
entry is ended, and the diff is stamped at or past its `revoked_at_height`.
The gateway cannot bind a key through an old grant, because the diff's own
height is what the grant must be live at, and the gateway cannot stamp a
height the hosts have not acknowledged.

The check needs a complete, committed view of the log through h: a host
answers only once its chain node, or its log copy, has reached h with no
gap; before that it waits for the answer rather than deciding without it.
Availability delays a host, it never forks it, and the answer, once given, is
the same on every host for ever.

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
module sets `revoked_at_height` at the height whose block time passes the
expiry. The gateway sees it in the grant source and emits the revoke like any
other, and the height rule above ends the key even if it does not.

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
   (`cmd/devshardd/hostevents_sink.go`), with one addition: the copy knows
   the height it is complete through, and an event the runner gives up on
   (`hostevents/runner.go`, `MaxDispatchAttempts`) marks the copy incomplete
   from that event's height until a full reload, so an answer is never
   given from a copy with a hole.
2. Direct chain query, `WarmKeyEntry` and the log, when dapi is unavailable
   or does not serve the event kinds (an old dapi answers the request without
   them), and whenever the copy is incomplete through the height asked.

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
   that has signatures by it in flight is past them, settlements included:
   the chain checks a settlement's warm signers against the grants at
   submission (`HasWarmKeyGrant`), so the operator revokes only after the
   settlements signed by the old key are submitted. Letting the settlement
   name a past height instead was considered and rejected: the submitter
   chooses that height, so it would let a compromised, revoked key sign
   settlements "in the past".
5. The operator revokes on chain; the entry gets `revoked_at_height` H2. The
   gateway emits `MsgWarmKeyRevoke{slot, key, grant_id}` in a diff stamped at
   or past H2; from that nonce the old key is refused, and the height rule
   refuses it in any diff stamped at or past H2 whether or not the entry is
   emitted. Diffs from before it replay unchanged.

## Catch-up and snapshots

A lagging host pulls diffs or a snapshot. Both carry the history, and every
bind and revoke names a chain log entry whose answer does not change, so the
host ends with the same key state as everyone else, however long it was away
and whatever has been revoked since. A host that replays verifies every entry
against the log the same way a live host does, with the same completeness
rule. There is no shortcut through the quorum signatures over a post-state
root: those signatures are checked with the same history, so trusting them
for the history would let a fabricated snapshot vouch for its own signers.
Nothing is "pending verification" on the apply path, and nothing depends on
pruned state.

The snapshot (`proto/devshard/v1/snapshot.proto`) gains a
`warm_key_history` field. The old `warm_keys` map is not converted: a v6
binding has no grant id to verify, so a session that moves to v7 starts with
an empty history and the gateway emits explicit binds, against the seeded
log entries, for the keys in use; until that diff the hosts accept cold keys
only.

## Compatibility

The new diff transactions are understood by v7 children only, so this is a
protocol version, not a patch to v6. The gateway emits them only on v7
sessions. The chain log and query ship with the 0.2.18 chain upgrade, which
seeds the log from the grants of that moment; until then the gateway cannot
propose binds and a v7 session accepts cold keys only.

## Open questions

- Scope of the chain change: the warm-key log with ids, `WarmKeyEntry`, the
  module ending entries on expiry, and the seeding at the upgrade. This note
  assumes the 0.2.18 branch takes them; if it cannot, the fallback is
  `GranteesByMessageType` at the current height with the determinism gap of
  v6 kept for replay.
- Residual risk at settlement. A warm key compromised and then revoked can
  still have signed settlement material before the revoke, and the chain
  cannot tell when a signature was made. Revoking before pending settlements
  are submitted costs the operator those settlements; that is the price of
  the rule "the chain checks grants at submission". A height-aware check was
  rejected because the submitter would choose the height.
- Several live keys per slot. The model allows it; the operator flow may want
  "one new, one old" only.
- Who may bind. The gateway as the sole author keeps sequencing simple. A host
  that rotates while the gateway is down waits with the old key; is that fine?
- A slot reassigned to another validator in a later epoch, and HA replicas of
  one host that share a warm key, follow the same rule (the history is per
  slot and the log is per cold key); both need a test.

## Implementation order

Everything ships in one pull request; the steps are the commit order, each
one buildable and tested on its own.

1. `signing/keyhistory.go`: the pure model and its acceptance rule, with
   tests. No caller yet.
2. `inference-chain`: the warm-key log with ids, the `WarmKeyEntry` query,
   log events, expiry ending entries, the seeding at the upgrade.
3. `types`, `state`: `EscrowState.WarmKeyHistory`, hash version, the two
   transactions, staged application, the height rule against the diff's
   stamped height, journal undo/redo, snapshot codec, every verifier
   (HeightAck, repair, gossip, settlement) reading the history by its own
   nonce rule; the explicit binds at the move to v7.
4. `bridge`: the log copy fed by dapi host events with the reset and
   completeness rules, `WarmKeyEntry` and the log as the direct fallback; the
   three forever-caches removed.
5. `cmd/devshardctl`: bind and revoke proposals in the sequencer from the
   grant source, with the "first use after the bind" rule; admin listing.
6. `cmd/devshardd`, `user`: entry confirmation against the log before a key is
   counted, waiting on availability, replay and snapshot paths, the
   unbound-payload path on the log copy.
7. dapi: event kinds, log snapshot on reset, event listener on the module
   events.
8. Tests: rotation mid-session; lagging host replaying a bind and a revoke
   after the revoke, with the chain pruned; dapi down; a log copy with a
   skipped event; snapshot restore; a settlement with old-key signatures
   across a rotation; a slot reassigned to another validator; two HA replicas
   sharing a key; a gateway proposing a bind through an old grant, through a
   grant of another grantee, and before its log entry exists; a revoke the
   gateway never emits.
