# Authenticated mainnet height in each v7 diff

V7 diffs begin with one `MsgMainnetHeight`. The creator signature covers its
height, exact block time (Unix nanoseconds), and carried CometBFT light blocks.
Every apply path, including persisted replay and best-effort composition,
authenticates this prelude before applying any messages. A repeated height has
an empty proof; rollback and multiple or misplaced preludes are invalid.

The trust anchor is the consensus block height and hash frozen on chain when
`CreateDevshardEscrow` executes. It comes from the queried escrow, not from a
peer-supplied validator set. The first proof supplies the matching signed
header and validator set. Each later header must be signed by strictly more
than two thirds of the exact validator set authenticated by the preceding
checkpoint's `NextValidatorsHash`. Unchanged sets can be skipped; transitions
need intermediate authenticated headers. Adjacent headers must link by parent
hash. The builder bisects transitions, with bounded fetches, proof bytes and
block count. Prelude construction also shares one ten-second RPC deadline,
including callers such as heartbeats that have no request deadline. A
self-signed replacement set establishes no authority.

The verified checkpoint (height, block hash, next validator hash, block time
and chain ID) is part of the state root, mutation journal and snapshot. Failed
apply and previews roll it back with the other state. Carried proof bytes use
the ordinary signed transaction journal and transport, so replay needs no
current oracle, wall clock or later validator set. Snapshot restore independently
rebuilds an anchor-to-checkpoint proof before trusting its header time or
validator set; the snapshot cannot replace the original chain-derived anchor.
This recovery check needs retained bootstrap/transition evidence from the
mainnet oracle. Consumers of a past diff
can read its authenticated height and time directly from its prelude.

Freshness is separate live signing policy. A host withholds new state
signatures if the checkpoint is over two minutes old, over ten seconds in
the future, over two blocks behind its own tip, or disagrees with its own
canonical header lookup. The canonical lookup also keeps an old escrow
anchor from authorizing a long-range fork after its validator keys have left
the trusting period. These checks do not invalidate already accepted history.
A fresh local canonical header is required for new signatures, while replay
of the authenticated journal remains deterministic. Reaching settlement alone
does not authorize signing unchecked history. An unavailable or stale checkpoint
withholds ordinary state signatures without aborting recovery or execution of
an authenticated diff. A later fresh diff can resume ordinary signing only over
a prefix accepted under the host admission policy; it cannot authorize unchecked
ancestors. A canonical hash mismatch remains an error. The dependent warm-key
code adds [live admission and durable prefix provenance](warm-keys-v7.md#first-seen-work-and-accepted-local-history)
before persisting newly received warm-signed work.

### Retrying a frozen final state

At `max_nonce`, a gateway cannot append a checkpoint-refresh diff. V7 hosts
instead attest the fixed final identity `(escrow_id, nonce, state_root)` through
an explicit final-state signing operation, separate from ordinary live diff
signing. This operation runs for both fresh and stale final checkpoints:

- The host's durable `host_admission_nonce` covers the whole prefix, and the
  stored root at the requested nonce equals the root being signed. The identity
  must also match the state machine's actual latest settlement state.
- The frozen checkpoint still matches the host's canonical chain source.
- A recent carried checkpoint within the ordinary age/lag window supplies the
  authority evidence without an additional light-block proof request. Otherwise
  the host authenticates a fresh tip from the existing light-block source.
- The signer is authorized at the final nonce and remains authorized at the
  checkpoint used for the attestation. Warm-key checks require the complete
  grant view through that height and its exact block time. Cold slot keys need
  no warm-grant lookup.

The shared checkpoint authenticator returns a checkpoint and its proof without
creating a transaction or changing state. The sequencer serializes this evidence
into `MsgMainnetHeight` when advancing a diff. Final attestation uses the evidence
only to check signer authority; it never constructs a refresh transaction.

Root, accounting and nonce stay unchanged. Final attestation keeps the existing
`StateSignatureContent` format. Signature collection invokes it through the
existing empty-diff retry and first tries reading durable signatures, so a
retained quorum needs no new signature. The same checks run after restart and
HA takeover.

The gateway does not assign an unlabeled terminal warm signature from a normal
response to the contacted participant: the same key may serve another participant
whose grant is still valid. After catch-up or a signing retry, collection reads
the issuer's existing slot-indexed signature endpoint and verifies the returned
signatures. V7 final warm-signature collection therefore requires that endpoint
and issuer storage; an unlabeled response is not a fallback. Cold signatures
identify their participant directly and keep their ordinary response path.

The local host response carries the exact slots selected by the signing
operation. Both ordinary request gossip and recovered-diff gossip retain those
labels; neither re-derives them from broader historical authority at the frozen
checkpoint. This metadata is not part of the wire response or signed content.

An active/finalizing state or a host without durable admission provenance keeps
the ordinary checkpoint freshness rule. Unchecked remote history cannot acquire
provenance through this retry. A replacement warm key must already be bound at
the frozen final nonce; a key first granted/bound afterwards is outside this
recovery path. Rotate with overlapping bindings before finalization, or use an
otherwise eligible signer. Missing evidence or an ended/expired signer grant
withholds the new signature.

Recovery still needs an eligible reachable quorum. Mainnet applies its existing
current-grant checks and current/previous-escrow-epoch settlement window when the
transaction is submitted. This change adds no chain upgrade, protobuf fields,
signature domain or operator configuration beyond the v7 admission dependency.

The NodeManager `GetBlockHeader` RPC has an optional `include_light_block`
request and a light-block response field. Ordinary hash-only lookups keep
working with older servers. Requesting evidence from an old or hash-only
server fails explicitly; v7 cannot downgrade to trusting an unsigned height.
The producer obtains commits and paginated validator sets from CometBFT.
Historical bootstrap/transition evidence is not limited to the short tip
cache window. Gateway recovery falls back to full carried-proof journal replay
if external bootstrap evidence makes a snapshot unavailable; it never trusts
unverified snapshot metadata. A journal with a missing prefix still fails closed.
This does not implement the separate Strong envelope dispute
and escalation machinery described in the height-sync specification.

## Release order

1. Ship the chain binary that stores escrow anchor fields 20 and 21. Field 19
   remains available for the separately developed height-sync timeout clock.
2. Upgrade dapi/NodeManager to serve light-block evidence. Old hash-only
   servers still serve old versions but cannot supply v7 evidence.
3. Build host and gateway with `DEVSHARD_VERSION=v7`; approve that same
   protocol name in the version catalog only after the chain upgrade.
4. Create new v7 escrows. An escrow without the chain anchor fails v7 bind;
   old escrows continue under their original version and root rules.

No SDK authz hooks are required by this PR. Warm-key grant checks at the
verified diff height belong to the dependent warm-key PR #2042. Keeping this
PR independent lets it be reviewed, merged or cherry-picked on its own.

## Validation

The common verifier tests exercise signed validator transitions, wrong trust
anchors, forged commits, altered header time, insufficient voting power,
reordered proofs, rollback, cancellation and size limits. State tests cover
mandatory framing, atomic failure, preview/commit, signing coverage, exact
time, persisted replay and snapshot recovery. The v7 stand uses one actual
CometBFT-signed mock chain for NodeManager, events and escrow anchors, then
checks that the proof is persisted and survives gateway recovery.
