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
WarmKeyBinding { Addr, FromNonce, Sequence, Height, ExpiresAt, Attestation,
                 RevokedAtNonce, RevokeSequence }
WarmKeyHistory  map[slot][]WarmKeyBinding
```

`FromNonce` is the diff nonce whose application introduced the binding and
`RevokedAtNonce` the nonce of the revoke entry, zero while the key is live.
`Sequence` and `RevokeSequence` are the granter's account sequences of the
chain transactions that attest the bind and the revoke
([below](#how-a-binding-enters-consensus)); `Height` is the height the bind
was confirmed at, `ExpiresAt` the grant's expiry, and `Attestation` the
signed transaction itself, so the history carries its proofs into snapshots.

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
- Repair requests and answers, and every other out-of-band admission, are
  local decisions, not consensus: the receiver uses its history head. A key
  rotated in may repair an old turn; a revoked key may not, whatever
  `RefNonce` it names.

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

The attestation is the chain transaction the operator already signs with
the cold key: the `MsgGrant` that creates the grant for a bind, the
`MsgRevoke` that ends it for a revoke, as signed transaction bytes. Nothing
new is signed and no new key custody appears: dapi holds only the warm
signer (`apiconfig.ApiAccount.SignerAccount`; `AccountKey` is a public key),
and the cold key is used exactly where it is used today, by the operator at
grant time. Every host verifies the attestation from the log alone: the
signature by the cold key of the slot, the chain id, the granter equal to
that cold key, the grantee equal to `address`, the current grant marker, and
the account sequence. The sequence is what stops replay: a bind must carry a
sequence above every entry the slot has seen, a revoke above the binding it
ends, so a bind attestation cannot bind a revoked key again and an old
revoke cannot end a later binding (`WarmKeyHistory.Bind`, `Revoke`). The
expiry in the grant is copied into the binding. `height` is the height the
transaction was confirmed at, informational.

A compromised warm key gains nothing here: it cannot produce a `MsgGrant`,
and the only input it can make is to send an existing attestation to the
gateway, which is public anyway. Who may request a bind is therefore not an
authorization boundary; the attestation is.

dapi keeps the signed grant and revoke transactions for its own account
(it submits them, or fetches them by hash from the chain) and serves them to
the host next to the grant events.

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

**Expiry.** A grant with an expiry needs no revoke transaction to end:
the binding ends at the first diff whose heightstamped time (the v7 item that
puts chain time into diffs for timeouts) is at or after `ExpiresAt`. That is
a function of the log, so every host ends it at the same nonce. Until that
item lands, bindings with an expiry are refused at bind time, so an expiry
can never leave a dead grant live in a session.

## Grant source

The grant source answers "which keys does granter G authorise, and since which
height" without a chain round trip on the apply path.

1. dapi host events. `GetHostEvents` (`common/nodemanager/nodemanager.proto`)
   gains `HOST_EVENT_KIND_KEY_GRANTED` and `HOST_EVENT_KIND_KEY_REVOKED` with
   `{granter, grantee, height, expiration, tx_hash}`, produced by the dapi
   event listener from the chain's authz `EventGrant` / `EventRevoke` on the
   current marker only. Events on the legacy marker are ignored: a legacy
   grant is stored separately on chain and its revoke says nothing about a
   `MsgClaimRewards` grant, so translating it would end a live
   authorization. The query's legacy translation stays what it is, a query
   convenience.
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

- The attestation shape. Signed transaction bytes are self-contained but
  bigger than a bare signature, and the hosts need the cold key's public key
  to verify them (recoverable from the transaction's signer info). An
  alternative is a chain proof of the grant against a block header, which
  needs the light-client story. Attestations by the old warm key were
  considered and rejected: a compromised warm key could then hand the slot to
  a successor of its choosing.
- Expiry before heightstamped time lands: refusing grants with an expiry is
  the safe default; is it acceptable for operators?
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
   transactions with their attestation and its verification (cold-key
   signature, chain id, granter, grantee, marker, sequence), staged
   application, expiry at heightstamped time, journal undo/redo, snapshot
   codec carrying the attestations, every verifier (HeightAck, repair,
   gossip, settlement) reading the history by its own nonce rule.
3. `bridge`: grant log fed by dapi host events with the reset rule, direct-query
   fallback with a TTL; the three forever-caches removed.
4. `cmd/devshardctl`: bind and revoke proposals in the sequencer with the
   "first use after the bind" rule; admin listing.
5. `cmd/devshardd`, `user`: fetching the signed grant transaction from dapi,
   sending the bind, replay and snapshot paths.
6. dapi: keeping and serving the signed grant and revoke transactions, event
   kinds, grant snapshot on reset, event listener on the current marker.
7. Tests: rotation mid-session; lagging host replaying a bind and a revoke
   after the grant was revoked and pruned on chain; dapi down; snapshot
   restore; a settlement with old-key signatures across a rotation; a slot
   reassigned to another validator; two HA replicas sharing a key.
