# Warm keys in v7: rotation and deterministic catch-up

Design note for the v7 item "warm keys handling refactoring: make them
available to rotate and be deterministic on catch-ups". Companion to the
catch-up rework (diff pulls and snapshots), which replays the same diffs on
every host and therefore needs the key decisions to be a function of the log.

**Status.** One pull request, in the order under
[Implementation order](#implementation-order). The rule is built on diff
nonces and the chain's warm-key log alone; the stricter "live at the diff's
height" rule waits for the v7 item that gives diffs a verified height
([Open questions](#open-questions)).

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

## The chain log: a question whose answer does not change

Every part of the v6 problem comes from asking a question whose answer changes
with time: "is W a warm key of G" is true today and false after a revoke, so
two hosts asking at different moments disagree, and a host replaying old
diffs after a revoke cannot reproduce what the others decided.

v7 asks the chain a question whose answer never changes. The inference module
keeps an append-only **warm-key log** per participant
(`inference-chain/x/inference/keeper/warm_key_log.go`,
`proto/inference/inference/warm_key_log.proto`):

```
WarmKeyLogEntry { granter, id, grantee, granted_at_height, revoked_at_height, expires_at_unix, end }
```

- A grant of the marker appends an entry with the next `id` for that granter
  (ids start at 1 and never go back). A revoke, an expiry or a replacement
  (a second `MsgGrant` for the same pair, which authz stores over the first)
  sets `revoked_at_height` and `end` (`REVOKED`, `EXPIRED`, `REPLACED`);
  nothing is deleted. A replacement in the same block as its predecessor gets
  a larger id, so ids order grants where heights cannot.
- The module learns about grants from a post handler on `MsgGrant`,
  `MsgRevoke` and the messages nested in `MsgExec`
  (`inference-chain/app/post_warm_key_log.go`), which runs in FinalizeBlock
  after the authz messages executed and fails the transaction if the log
  cannot be written, so the log and authz never disagree. Expiry is the
  module's EndBlock (`ExpireWarmKeys`), the same block time authz prunes by.
- Queries: `WarmKeyEntry(granter, id)` returns one entry and that answer is
  the same for ever, on every node, however much state has been pruned
  since; `WarmKeyLog(granter)` returns all entries in id order. The log is
  small (one entry per grant) and lives in the module store like any other
  record. `GranteesByMessageType` keeps working for current grants.
- Events `warm_key_granted{granter, grantee, grant_id, height, expires_at}` and
  `warm_key_revoked{granter, grantee, grant_id, height, end}` are emitted on
  every change, for decentralized-api to forward as host events.
- The log is seeded when it is introduced: inference module consensus version
  16 migrates by walking the authz grants of that moment and writing one live
  entry per unexpired grant of the marker, at the upgrade height, in store
  order, so every warm key in use has an entry to point at without a new grant
  transaction. History from before is not reconstructed; no v7 session
  exists before it.

This is the chain part of the item. It ships with the 0.2.18 chain release
that collects the settlement changes for v7.

## Model

A slot has a history of bindings instead of one binding
(`signing/keyhistory.go`):

```
WarmKeyBinding { Addr, FromNonce, GrantID, RevokedAtNonce }
WarmKeyHistory  map[slot][]WarmKeyBinding
```

`FromNonce` is the diff nonce whose application introduced the binding and
`RevokedAtNonce` the nonce of the revoke entry, zero while the key is live.
`GrantID` names the chain log entry the binding points at. A bind must name a
grant id above every earlier grant of the same key on the slot, and a revoke
must name the live binding's grant id (`WarmKeyHistory.Bind`, `Revoke`), so an
old grant cannot bring a revoked key back and a stale revoke cannot end a
later binding.

The acceptance rule for a host signature is a function of the history and of
one nonce (`WarmKeyHistory.Allows`): the cold key; a warm key whose binding
for that slot covers the nonce; while the slot has no binding covering it, a
key that covers it on a sibling slot of the same validator, unless the key was
revoked on this slot at or before it. No chain call on the apply path, no
wall clock, no height. Replaying the same diffs on any host yields the same
answer.

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
  head **and** the grant source at the current moment, so a key revoked on
  chain is refused there even when the session has not advanced since. A key
  rotated in may repair an old turn; a revoked key may not, whatever
  `RefNonce` it names.
- Admission without a session (`ServeUnboundGetPayload`, `payloadRoster`,
  `scanRosterWarmKey`) has no history to read and uses the grant source
  ([below](#grant-source)), as it does today, minus the forever-cache.

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
MsgWarmKeyBind   { slot_id, address, grant_id }
MsgWarmKeyRevoke { slot_id, address, grant_id }
```

The gateway is the sequencer and the only author of these entries, but it is
not the authority for them: its signature authenticates the diff, not the
grant. The authority is the chain log, and the entry names the log record by
its id. Every host checks the entry against the chain log before it counts
the key: the entry `grant_id` of the slot's cold key exists and its grantee is
`address`. That is a fact about an append-only record, so a host that checks
it today and a host that checks it after the grant was revoked, replaced or
expired reach the same verdict. For a revoke the host checks that the entry
exists and names the live binding's grant; whether the chain has ended it is
not part of the rule ([Revocation](#revocation-and-what-the-log-cannot-enforce)).

The gateway proposes from its grant source. Before each diff it compares the
group's live grants with the history: a live grant of a slot's cold key with
no live binding on that slot becomes a `MsgWarmKeyBind`; a live binding
whose entry the source shows as ended becomes a `MsgWarmKeyRevoke`. A host's
first message by a key the gateway has not seen yet passes the gateway's
admission through the grant source, which falls back to the chain on a
miss, and the bind follows in the next diff. The gateway binds keys
proactively, not on first use, so a host that rotated before its first
message is bound before it needs to be.

**First binds.** The first diff of a session binds every live grant of the
group's cold keys, and so does the first diff after the gateway's grant
source learns of a new one. There is no anchor height: the entries exist, and
that is all the check asks.

The check needs the entry; a host whose chain node, or log copy, does not
have it yet waits for it rather than deciding without it, bounded by the
diff's own timeouts. Availability delays a host, it never forks it, and the
answer, once given, is the same on every host for ever.

A bind the chain log does not confirm is a gateway fault. The host refuses
the diff like any other invalid diff, does not sign the post-state root, and
the escrow stalls on that gateway: the same outcome as any other misbehaving
sequencer today, and nothing the gateway can do forges a host signature.

Application is staged: the bind and revoke entries of a diff apply before its
other transactions, whatever their order on the wire, so a key bound at nonce
N is live for the messages of nonce N and a key revoked at N is refused for
them. Compose, the log-plane checks (`checkLogPlaneLocked`), application and
replay all evaluate host signatures against the history as of the diff being
applied, with that diff's own entries included.

## Revocation, and what the log cannot enforce

A revoke on chain reaches a session through the gateway: its grant source
shows the entry ended, its next diff carries `MsgWarmKeyRevoke`, and from that
nonce the key is refused in every diff and on every host. The hosts also stop
serving the key locally as soon as their own grant source shows the end:
repair, gossip admission and unbound payload fetches refuse it without
waiting for the diff. Expiry is one more end in the log, handled the same
way.

What the rule does not give is a revoke that takes effect inside a session
against the gateway's will. A gateway that withholds the revoke entry keeps
the key acceptable in the diffs it composes, because the consensus rule reads
nonces and the log entry's existence, not the entry's end. The scenario is a
compromised gateway together with a compromised host key; the gateway is the
single sequencer already and can delay or reorder any message, and the extra
power here is to extend a key the operator has revoked until the hosts'
local refusals stop it. The stricter rule, "a binding counts only while its
grant is live at the diff's height", needs a per-diff chain height the
gateway cannot set back; the v7 item "timeouts use heightstamped time, as it
is verified by protocol" is where that height comes from, and the rule plugs
into it when it exists ([Open questions](#open-questions)).

## Grant source

The grant source is the hosts' and the gateway's view of the chain log
without a chain round trip per question: a copy of the log per granter,
append-only like the log, kept in memory by the bridge
(`bridge/warmkeylog.go`) and filled from two feeds.

1. dapi host events. `GetHostEvents` (`common/nodemanager/nodemanager.proto`)
   gains `HOST_EVENT_KIND_WARM_KEY_GRANTED` and
   `HOST_EVENT_KIND_WARM_KEY_REVOKED` with `{granter, grantee, grant_id,
   height, expires_at_unix, end}`, produced by the dapi event listener from
   the module's events, from transactions (grants, revokes) and from the
   block's end events (expiry). They are not filtered by participant: a host
   needs the keys of every peer in its groups, and grants are rare. The
   stream keeps its cursor and `needs_reset` contract: on a reset devshardd
   drops its copies and reloads from the chain on the next question. One
   existing loss path in dapi closes with this: a poll that times out
   re-reads the ring and advances `NextCursor` while returning no events
   (`decentralized-api/nodemanager/host_events_rpc.go`), so an event that
   arrived between the timeout and the re-read was skipped for good; the
   timeout response now returns what the re-read found.
2. Direct chain query, `WarmKeyLog(granter)` to fill a copy and
   `WarmKeyEntry(granter, id)` to confirm one entry, whenever the copy has no
   answer: an entry it has not seen, a granter it has never loaded, or a
   dapi that does not serve the event kinds (an old dapi answers the request
   without them).

A copy of an append-only log needs no TTL and no invalidation; an entry that
is in the copy is final except for its end, and an end that the copy has not
seen yet only delays a local refusal. The three forever-caches of v6 go away:
the bridge's `warmCache`, the session's `warmChecks` and the roster `peers`
decisions read the copy, or the history, which is consensus.

## Rotation, step by step

1. The operator grants a new warm key on chain (dapi admin flow, unchanged).
   The module appends a log entry.
2. The gateway sees the entry in its grant source and its next diff carries
   `MsgWarmKeyBind{slot, key, id}`. Peers apply the diff, confirm the entry
   against the chain log, and the new key is live from that nonce.
3. The host's devshardd starts signing with the new key, now or later: a key
   bound before its first use needs no catch-up.
4. The old key stays live, and the host may keep using it, until every escrow
   that has signatures by it in flight is past them, settlements included:
   the chain checks a settlement's warm signers against the grants at
   submission (`HasWarmKeyGrant`), so the operator revokes only after the
   settlements signed by the old key are submitted. Letting the settlement
   name a past height instead was considered and rejected: the submitter
   chooses that height, so it would let a compromised, revoked key sign
   settlements "in the past".
5. The operator revokes on chain; the entry is ended. The gateway emits
   `MsgWarmKeyRevoke{slot, key, id}` in its next diff; from that nonce the old
   key is refused. Diffs from before it replay unchanged.

## Catch-up and snapshots

A lagging host pulls diffs or a snapshot. Both carry the history, and every
bind and revoke names a chain log entry whose answer does not change, so the
host ends with the same key state as everyone else, however long it was away
and whatever has been revoked since. A host that replays diffs verifies every
entry against the log the same way a live host does; a host that restores a
snapshot confirms every binding of the snapshot's history the same way before
it serves. There is no shortcut through the quorum signatures over a
post-state root: those signatures are checked with the same history, so
trusting them for the history would let a fabricated snapshot vouch for its
own signers. Nothing is "pending verification" on the apply path, and nothing
depends on pruned state.

The snapshot (`proto/devshard/v1/snapshot.proto`) gains a
`warm_key_history` field. The old `warm_keys` map is not read by v7: a v6
binding has no grant id to verify, and a session is bound to one protocol
version, so no v7 session starts from a v6 snapshot.

## Compatibility

The new diff transactions are understood by v7 children only, so this is a
protocol version, not a patch to v6. The gateway emits them only on v7
sessions. The chain log, the post handler and the queries ship with the
0.2.18 chain upgrade, which seeds the log; until then the gateway's grant
source has nothing to propose from and a v7 session accepts cold keys only.
A dapi without the event kinds leaves the hosts on the chain fallback.

## Open questions

- The height rule. The consensus rule is nonce-only on purpose: height-sync
  as it stands gives no per-diff height the gateway cannot set back
  (`MsgHeartbeat.observed_height` is optional and absent from ordinary
  diffs, `checkL0` marks a stale sequencer height rather than rejecting it,
  the floor rises on any one host-signed claim). When the v7 item
  "heightstamped time verified by protocol" supplies one height per diff
  that a quorum of hosts bounds, the rule "a binding counts only while its
  grant is live at that height" is one check on top of this model, with the
  entry's `revoked_at_height` already in the log. Until then the limitation
  in [Revocation](#revocation-and-what-the-log-cannot-enforce) stands.
- Scope of the chain change: the log, the post handler, the two queries, the
  expiry in EndBlock and the seeding migration. This note assumes the 0.2.18
  branch takes them.
- Residual risk at settlement. A warm key compromised and then revoked can
  still have signed settlement material before the revoke, and the chain
  cannot tell when a signature was made. Revoking before pending settlements
  are submitted costs the operator those settlements; that is the price of
  the rule "the chain checks grants at submission".
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
   tests.
2. `inference-chain`: the warm-key log with ids, `WarmKeyEntry` and
   `WarmKeyLog`, the post handler, the events, expiry in EndBlock, the
   seeding migration at consensus version 16.
3. `types`, `state`: `EscrowState.WarmKeyHistory`, its hash, the two
   transactions, staged application, journal undo/redo, snapshot codec,
   every verifier (HeightAck, repair, gossip, settlement) reading the
   history by its own nonce rule, the grant source as the local-admission
   fallback in place of bind-on-first-use.
4. `bridge`: the log copy, `WarmKeyEntry` and `WarmKeyLog` as the chain
   fallback, confirmation of a bind by id; the three forever-caches removed.
5. `cmd/devshardctl`, `user`: bind and revoke proposals in the sequencer from
   the grant source; the admin listing.
6. `cmd/devshardd`, `host`: entry confirmation against the log before a key is
   counted, replay and snapshot paths, the unbound-payload path on the copy,
   the host events feed.
7. dapi: event kinds, event listener on the module events, the timed-out poll
   returning what its re-read found.
8. Tests: rotation mid-session; a lagging host replaying a bind and a revoke
   after the revoke; dapi down; snapshot restore; a settlement with old-key
   signatures across a rotation; a slot reassigned to another validator; two
   HA replicas sharing a key; a gateway proposing a bind through a grant of
   another grantee and before its log entry exists; a revoke the gateway
   never emits.
