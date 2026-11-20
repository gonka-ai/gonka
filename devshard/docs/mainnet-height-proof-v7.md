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
the ordinary signed transaction journal and transport. Replaying the carried
checkpoints needs no current oracle, wall clock or later validator set. Full
session recovery can still need historical header times for height-sync floor
entries outside the local cache. Snapshot restore independently
rebuilds an anchor-to-checkpoint proof before trusting its header time or
validator set; the snapshot cannot replace the original chain-derived anchor.
This recovery check needs retained bootstrap/transition evidence from the
mainnet oracle. Consumers of a past diff
can read its authenticated height and time directly from its prelude.

Freshness is separate live signing policy. A host withholds new state
signatures if the checkpoint is over two minutes old, over ten seconds in
the future, over two blocks behind its own tip, or disagrees with its own
canonical header lookup. A cached canonical header can satisfy the lookup.
On a cache miss, the host can use verified Comet evidence at the checkpoint
height. The canonical lookup also keeps an old escrow
anchor from authorizing a long-range fork after its validator keys have left
the trusting period. These checks do not invalidate already accepted history.
Without a fresh WebSocket tip, dAPI gets a bounded head start before the host
queries chain gRPC for a fresh tip. If both eligible replies are ready, the
higher one wins. A recent observation can reject an already stale checkpoint
without another RPC. Signing an eligible checkpoint still requires a live
observation within the request deadline. A host can reuse a fresh observation
from the same request and checkpoint for state signing and the outgoing
height-sync anchor. Reuse ends at the earlier observation or request deadline.
Newer unexpired local observations still apply. Timeout verification also uses
the observed height to recognize plausible evidence while its header is pending.
A fresh local canonical header is required for new signatures, while replay
of the authenticated journal remains deterministic. Settlement is not an
exception: replaying a never-signed stale history into its terminal phase must
not make that history eligible for signatures.
An unavailable or stale live checkpoint withholds the state signature without
aborting recovery or the executor receipt/job of an authenticated diff. A
later fresh diff can resume state signing; a canonical hash mismatch is still
an error.
When only a temporary oracle failure withheld a signature, signature collection
also asks an already caught-up v7 host to retry signing its current state without
appending a diff. The ordinary freshness and acceptance checks still run; an
outdated checkpoint cannot be repaired by repeating this request.

There is a known recovery limit at the escrow's absolute `max_nonce`. If the
gateway persists that last diff and crashes before collecting a quorum, the
checkpoint can become stale before restart. No further nonce is available for
the usual fresh-checkpoint diff, so this path cannot currently recover
settlement. Already collected settlement evidence is a separate case; the
limitation concerns missing signatures, not the deletion of existing ones.
The user-approved scope split tracks a finalization recovery protocol in
[proposal PR #2274](https://github.com/gonka-ai/gonka/pull/2274). This PR retains
the limitation and does not exempt terminal states from freshness checks.

The NodeManager `GetBlockHeader` RPC has an optional `include_light_block`
request and a light-block response field. Ordinary hash-only lookups keep
working with older servers. Requesting evidence from an old or hash-only
server fails explicitly; v7 still requires a signed height. The host's
configured `NODE_RPC_URL` supplies direct evidence if dAPI cannot serve it.
CometBFT supplies commits and paginated validator sets. The host checks the
commit and verifies the evidence against the escrow's pinned checkpoint.
Historical bootstrap/transition evidence is not limited to the short tip
cache window. Gateway recovery falls back to full carried-proof journal replay
if external bootstrap evidence makes a snapshot unavailable; it never trusts
unverified snapshot metadata. A journal with a missing prefix still fails closed.
Before persisting a v7 diff, the sequencer checks that its encoded chat
request fits the existing transport limit. Catch-up splits contiguous
diffs by encoded request size when the normal batch would exceed that limit.
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
