# Warm keys in v7: rotation and deterministic replay

PR #2042 implements warm-key rotation on top of the independent authenticated
height work in [PR #2271](https://github.com/gonka-ai/gonka/pull/2271).
The height branch is based on the shared `devshard/v7` and can be reviewed,
merged or cherry-picked separately. Authz integration, rotation tests and
warm-key checks at the authenticated height have their own commits.

## Chain history

V6 binds one warm key per slot on first use, from a current authz lookup.
That binding cannot rotate, and different hosts/replay can query different
chain states. V7 replaces this with an append-only chain log and explicit
binding transactions in the signed diff journal.

The warm-key marker is `MsgClaimRewards` (`WarmKeyGrantMarkerTypeURL`).
`GranteesByMessageType` still translates legacy `MsgStartInference` queries.
Each participant's log contains:

```
WarmKeyLogEntry {
  granter, id, grantee, granted_at_height, revoked_at_height,
  expires_at_unix, end
}
```

IDs increase per granter and never repeat. Granting the marker creates an
entry. Revocation or replacement closes it at the corresponding chain height
with `REVOKED` or `REPLACED`; records are never deleted. A replacement in the
same block still gets a new ID.

Expiration is stored in Unix nanoseconds. Equality with the expiration is
live; expiration is strictly after it, as in authz. It changes no historical
record and emits no event. Zero means no expiration. Unsupported dates that
cannot round-trip through a nonzero signed 64-bit Unix nanosecond timestamp
are rejected before changing the log. Migration skips such existing grants
with a warning; those grants must be renewed before use in v7.

`WarmKeyLog(granter)` returns the full history; `WarmKeyEntry(granter,id)`
returns an individual record. Later reads preserve the facts needed to check
an earlier height, including after authz pruning, replacement or expiration.
Grant/end events are forwarded through dapi host events.

## SDK hooks and legacy fallback

With Cosmos SDK #23, `WarmKeyAuthzHooks` records changes at the keeper's
`SaveGrant`/`DeleteGrant` boundary. This includes direct keeper writes and
authz operations executed through group or governance modules.

The application detects the hook setter once at startup from the compiled
SDK API. This is not an operator-controlled consensus flag. Hook-enabled
builds install the hooks and omit the post-handler decorator, so changes
are not recorded twice.

The current pinned SDK (`v0.53.3-ps19-observability`) has no hooks. In that
build, `app/post_warm_key_log.go` records successful `MsgGrant`, `MsgRevoke`
and nested `MsgExec`. It cannot observe group/governance execution or direct
keeper writes. Comments mark this fallback and the startup compatibility
adapter for removal when the baseline SDK includes #23. Tests exercise both
SDK modes; hook-only cases are skipped on the legacy SDK.

Inference module consensus version 16 seeds the log from live, unexpired
marker grants during the v0.2.18 upgrade, in deterministic store order. The
seed height is the upgrade height, not a reconstructed historical grant
height. No v7 session exists before that migration. Seeding is idempotent.

## Binding history and authenticated height

The devshard state stores and hashes every binding:

```
WarmKeyBinding { Addr, FromNonce, GrantID, RevokedAtNonce }
WarmKeyHistory  map[slot][]WarmKeyBinding

MsgWarmKeyBind   { slot_id, address, grant_id }
MsgWarmKeyRevoke { slot_id, address, grant_id, participant_sig }
```

Only signed diff transactions modify this history. A bind must advance the
grant ID for that key on that slot; a revoke must name its live binding's ID.
While its chain grant is still live, a revoke additionally needs consent from
the cold participant or that bound warm key. `participant_sig` signs the
domain-tagged `WarmKeyRevokeAuthorization` containing the escrow ID, slot,
warm address and grant ID. The creator cannot disable an executor on its own
and turn completed work into an unpaid timeout. If the authenticated chain
checkpoint shows the grant has ended or expired, the gateway can synchronize
the revoke without a participant signature. Unknown grant evidence pauses
apply, including persisted replay; it never counts as an ended grant.
Several keys can cover the same nonce range, allowing HA replicas to use
different live warm keys during rotation. The cold slot key remains valid.
A sibling slot of the same participant may supply a binding while this slot
has none; an explicit revoke on this slot prevents that fallback.

Every v7 diff begins with `MsgMainnetHeight`, which authenticates a mainnet
checkpoint from the escrow's chain-pinned block hash. Its proof, block time
and checkpoint survive the ordinary journal, state root and snapshots. See
[the height protocol and release order](mainnet-height-proof-v7.md).
After authenticating the height, apply pre-stages warm-key transactions before
other messages and signature checks. Composition preserves the height
prelude as the first serialized transaction.

A warm signature must have a binding at its authorization nonce **and** a
matching chain grant at that diff's verified H and exact block time:

- `granter`, `grantee` and ID match the binding and slot's cold key;
- `granted_at_height <= H`;
- revoke/replacement height is zero or greater than H;
- authenticated block time is at or before expiration, or expiration is zero.

Apply and persisted replay both run these checks. An invalid grant refuses
the signature even if the gateway omits `MsgWarmKeyRevoke`. Later revokes and
expiration do not erase a grant's earlier validity.

The latest append-only log can answer historical questions, but the node must
have applied at least H. Completeness comes only from a full query response's
`x-cosmos-block-height` header. Delivered events, individual entry reads and
cache age do not prove it. Missing headers, lagging nodes and query failures
pause apply instead of guessing that no revoke exists. A completed view is
shared across that granter's slots/keys and reused for covered heights.
Derived grant decisions are journaled with the checkpoint for preview and
rollback, and rebuilt on snapshot recovery; they are not hashed separately.

If authority evidence disappears while an inference completes, the host keeps
the completed result unsigned and retries publication during ordinary mempool
polling. Completed payloads are marked before publication starts, so a grant
lookup cannot make completed work appear absent to a timeout challenger.
A timeout challenge can rebuild a lost Finish after restart from the persisted
payload with `RecoveryStoredOnly`. Concurrent callers share that recovery;
individual cancellation leaves the task available to the other callers. It has
a ten-second deadline and is cancelled and awaited on host shutdown. Storage
still loading after 25 ms reports pending before ordinary peer RPC deadlines.

V7 challenge replies carry available receipt/Finish evidence together with an
independent `evidence_pending` flag. The separate receipt is selected from a
surviving authorized ConfirmStart. HTTP and Connect clients preserve those
proofs when reporting pending. A verifier can choose another independently
verified ConfirmStart when that receipt hint is unavailable in its grant view.
The verifier checks available proofs first,
using its own inference phase, and applies bounded deferrals when the proof
it needs is missing. The executor may still be pending while a verifier has
already applied ConfirmStart. An unresolved Finish therefore stays pending
even when a valid receipt is available; a valid Finish can independently
reject an execution timeout. Legacy v6 response behavior is preserved.

Challenge recovery and its final response inspect only the requested inference.
Finish publication uses cancellation-aware, lazy checks of its own slot;
a cold signer does not query unrelated grants. Pending-result batches share
lazy authorization decisions, start no more RPCs after a 100 ms budget, and
retain unresolved results. An in-flight query retains its source timeout.
At most 32 results are retried per pass. Their starting entry rotates even
when a pass visits the full queue, giving later results a chance to publish.
Once a receipt, Finish or participant validation is committed, other queued
HA signature variants of that logical operation are removed from committed
state. Future operations and work still owed by other participants remain.

## Which nonce is authorized

Messages carried in a diff use that diff's nonce and checkpoint. This includes
receipts, finishes, votes and HeightAck. A delayed message included after the
grant's end is refused even if it was signed earlier.

State signatures name a particular state nonce. Late signature verification
reads that nonce's already verified height/time from the stored diff prelude,
so a subsequent revoke does not invalidate a signature over earlier state.
An unbound key cannot sign a past nonce through a current live-grant lookup.

Transport membership, repair and unbound payload access remain live local
admission decisions. They consult the current grant source and refuse a key
whose end they have observed. They do not bind an unknown key into consensus;
the gateway must sequence its binding first.

## First-seen work and accepted local history

A canonical historical checkpoint proves the block, not when a warm signature
was created. Before persisting a first-seen diff, hosts additionally check each
warm signer used by its messages against a complete grant view at the host's
current authenticated mainnet tip and exact block time. This includes requests,
gossip recovery and remote catch-up, with or without a host store. The same
signer must still pass the deterministic binding and grant checks at the diff's
own checkpoint. Cold signatures do not acquire a warm-grant lookup requirement.
The live checkpoint is sampled once per validation batch; this is authority at
the observer's confirmed tip, not a wall-clock guarantee of observing every
revocation immediately.

A revoked, replaced or expired grant rejects the new work before persistence.
An unavailable tip, proof or complete grant view reports pending, leaving that
diff unapplied and unwritten. Already validated earlier diffs in a batch may
commit. Appending a fresh metadata-only diff cannot promote rejected ancestors.
The current signing freshness window (including the two-block lag allowance)
does not bypass this separate admission check.

Hosts persist a private contiguous `host_admission_nonce` with the admitted diff
batch, in the same SQLite/Postgres transaction. It is not wire data, a state-root
field, or a certificate peers may import. HA replicas sharing the same trusted
host database share this provenance. State signing refuses any stored prefix
that extends beyond this watermark. Restart and local reconciliation replay
marked history at each diff's original checkpoint, so a later key revocation
does not erase work the host already accepted. Snapshots may skip only a marked
prefix; snapshot bytes and existing state signatures cannot create a mark.

Old v7 databases start with a zero watermark. Recovery ignores snapshots beyond
it and revalidates unmarked journal rows under the live admission rule, advancing
the watermark only after successful validation. A formerly unchecked row using
a now-retired key can therefore stop recovery. Never set the watermark manually
to bypass this check. Trusted local SQLite-to-Postgres migration preserves the
source database's recorded prefix and leaves its unmarked suffix unmarked.

This is deliberately strict for an offline participant: remote history first
seen after its signer loses authority is refused even if other hosts accepted
it earlier. Such history needs a separately designed accepted-prefix attestation
or another recovery protocol; old warm signatures alone do not establish when
it was accepted. This change does not provide that protocol. Before revoking an
old key, drain its in-flight work and ensure replicas that need independent
catch-up have received the prefix. A replica sharing an admitted HA database can
still replay that prefix after revocation.

## Grant source and event delivery

The bridge keeps a log copy only for granters it has queried. Unrelated
network events allocate no entries. Initial use loads the full log; live
admission refreshes stale copies and throttles miss reloads. Ends are final:
an older grant event cannot revive an ended entry. The three permanent V6
allow/deny caches are removed.

End events preserve the cached grant's original height and expiration. If an
end arrives before its grant, the partial entry cannot authorize a historical
signature until a full chain read supplies the missing grant metadata.

Dapi forwards grant/end events for all participants. One writer reads block
results in height order, persists events before publication, and emits the
block marker last. The bounded ring is a hot tail over durable SQLite or
Postgres storage; older cursors read bounded pages. The current and previous
block epochs are retained, and reset/generation semantics make clients
reload incomplete copies. Timed-out polls return events found by their final
ring re-read rather than advancing a cursor while dropping them. A failed
store read never advances the client cursor.

The default store is `/root/.dapi/data/host_events.sqlite`.
`DAPI_HOST_EVENTS_STORE=postgres` uses the standard `PG*` settings. SQLite
uses a writer file lock and Postgres a session advisory lock. Startup restores
the feed generation, cursor and hot tail. Missing/pruned escrow metadata is
a cache hint to discard, not a reason to finalize a live session. A failed
event dispatch retries the unprocessed suffix of its page.

## Rotation and recovery

1. Grant the new warm key on chain and wait for its bind in the session.
2. Start signing with the new `KEY_NAME`. Keep `ACCOUNT_PUBKEY` set to the
   cold participant public key. HA replicas can overlap with different live
   warm keys while their signatures in flight drain.
3. Submit any pending settlements signed by the old key before revoking it:
   the chain still checks settlement grants at submission, not at a
   submitter-chosen historical height.
4. Drain in-flight old-key messages and bring independently stored replicas up
   to date before revocation. New live admission requires the signer to remain
   authorized at the receiving host's current authenticated tip.
5. Revoke the old key on chain. Diffs whose authenticated checkpoint reaches
   that end reject it; the gateway also emits a nonce revoke. Earlier valid
   diffs continue to replay.

Snapshots retain the full binding history and authenticated checkpoint.
Restore confirms historical binding identities against the chain log and
checks active bindings at the snapshot checkpoint before serving. Replay
checks each diff at its own authenticated checkpoint, never today's wall
clock. V6 snapshots with the old one-key `warm_keys` map are rejected rather
than silently dropping their bindings.

The Docker rotation test exercises overlapping HA signers, grant/end events,
revoked-key transport admission, an offline replica across revocation, an
actual nonce-500 Postgres snapshot, replica recovery and gateway restart.

Read-only state/debug views for inactive gateway escrows use the same historical
grant checks during journal replay and snapshot restore. They do not contact
hosts or route inferences, but v7 warm-key history still requires a complete
grant source, which may query the chain. A missing or unavailable grant view
refuses recovery rather than skipping these checks. Cold-only local views do
not acquire this grant-source requirement.

## Release coordination

Deploy this admission policy to all v7 hosts before approving v7 for production.
The additive storage migration requires no new operator flag. Do not overlap old
and new v7 writers for the same host database: an old writer does not record live
admission, and its suffix requires revalidation before a new host can sign it.
Keep the database and its admission watermark together when restoring backups.
Prototype journals containing retired-key work may fail revalidation; use new
test escrows rather than certifying unchecked history. Earlier protocol versions
retain their existing behavior and can use the additive schema.


Ship the v0.2.18 chain binary (module version 16, log seeding and escrow trust
anchors), then light-block-capable dapi, then approve the v7 catalog entry
and create new v7 escrows. V7 binding fails without the escrow anchor; it does
not silently fall back to unsigned heights or cold-only pre-upgrade sessions.
Keep old binaries for escrows pinned to earlier protocol versions.

SDK #23 is expected before release. Until it is in the pinned dependency,
retain the legacy post-handler fallback with its stated scope. A dapi without
warm-key event kinds can still use direct chain queries. V7 additionally
requires light-block evidence for its checkpoint; hash-only servers cannot
supply that evidence.

### HA peer RPC tokens

v7 replicas sharing PostgreSQL derive peer RPC token keys from a private,
cryptographically random 32-byte seed stored once in that database. The derived
key is scoped to the cold host address, protocol version and token key ID.
Replicas may use different warm signers without invalidating each other's tokens;
restarting or rotating a warm signer preserves the token key. Keep PostgreSQL
persistent across replica restarts. The additive migration also permits old v6
binaries to keep using the same database. v6 retains its signer-derived token key.
If the shared key is unavailable, the v7 child refuses startup and can restart
once PostgreSQL recovers. A configured shared-key backend error fails closed; it must not fall back to a
replica-local signer key. Standalone stores without PostgreSQL retain their
existing signer-derived key. The seed is private host infrastructure data and
must never be exposed through RPC, diagnostic exports or logs.

### Historical signature and slot boundaries

A future gossip notification may trigger catch-up, but a v7 replica does not
accumulate or overwrite state signatures until it has applied that nonce locally.
A root already present in shared HA storage does not establish which bindings
were authorized there. After catch-up, signatures use that nonce's verified
checkpoint and binding history.

An expired or chain-revoked grant disables its key without deleting the nonce
binding from slot policy. The occupied slot cannot silently inherit a sibling
slot's warm key. Sibling fallback requires an explicit nonce revoke; an explicit
bind can instead authorize that key directly. Cold slot keys
remain authorized, and live admission can still propose an explicit new binding.

Before issuing its own v7 state signature, a running replica rechecks its signer
against the signed nonce rather than trusting slot ownership cached at startup.
A revoked/expired signer withholds the signature while catch-up still succeeds.
Only authorized slot labels may be persisted or emitted during recovery; a stale
replica cannot overwrite a valid signature produced by another HA signer.

Outgoing timeout/error-miss votes, validations and repair probes select a live
signing slot rather than the startup primary slot. After a per-slot nonce revoke,
the same warm signer may continue through another authorized slot of its cold
participant. Async validations recheck this choice before publishing. A role
fixed to one slot (executor receipt, finish or HeightAck) is withheld when that
slot no longer authorizes the signer. Gossip labels a state signature using its
exact signed nonce. A fully revoked signer emits no replacement slot signature.

Queued host-signed proposals are checked again against live slot authorization
before they can block a validation retry or trigger stale-mempool withholding.
Invalid signatures from a revoked slot are removed, so a replacement signed
through a live slot can be queued. The same check covers receipts, finishes and
height acks; historical journal replay continues to use its authenticated nonce.

An unavailable grant lookup withholds an unresolved proposal from the live
mempool view but retains its original signed bytes. An events reset followed by
a temporary RPC failure must not erase a completed inference's queued Finish.
After evidence becomes available, the proposal is checked again. Definitively
revoked proposals can still be discarded and replaced through an authorized slot.
