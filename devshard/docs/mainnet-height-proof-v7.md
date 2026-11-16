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
block count. A self-signed replacement set establishes no authority.

The verified checkpoint (height, block hash, next validator hash, block time
and chain ID) is part of the state root, mutation journal and snapshot. Failed
apply and previews roll it back with the other state. Carried proof bytes use
the ordinary signed transaction journal and transport, so replay needs no
current oracle, wall clock or later validator set. Consumers of a past diff
can read its authenticated height and time directly from its prelude.

Freshness is separate live signing policy. A host withholds new state
signatures if the checkpoint is over two minutes old, over ten seconds in
the future, over two blocks behind its own tip, or disagrees with its own
canonical header lookup. The canonical lookup also keeps an old escrow
anchor from authorizing a long-range fork after its validator keys have left
the trusting period. These checks do not invalidate already accepted history.
A fresh local canonical header is required for new signatures, while replay
of the authenticated journal remains deterministic.

The NodeManager `GetBlockHeader` RPC has an optional `include_light_block`
request and a light-block response field. Ordinary hash-only lookups keep
working with older servers. Requesting evidence from an old or hash-only
server fails explicitly; v7 cannot downgrade to trusting an unsigned height.
The producer obtains commits and paginated validator sets from CometBFT.
Historical bootstrap/transition evidence is not limited to the short tip
cache window. This does not implement the separate Strong envelope dispute
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
