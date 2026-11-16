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
MsgWarmKeyRevoke { slot_id, address, grant_id }
```

Only signed diff transactions modify this history. A bind must advance the
grant ID for that key on that slot; a revoke must name its live binding's ID.
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
4. Revoke the old key on chain. Diffs whose authenticated checkpoint reaches
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

## Release coordination

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
