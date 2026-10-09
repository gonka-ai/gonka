# Refusal snapshot catch-up (v4.2)

Two optimizations reduce the data sent during refusal checks:

1. Before challenging the executor, the verifier asks for its latest nonce and
   state hash. If they match a diff in the verified package, it sends only the
   diffs after that nonce, without the snapshot or signatures. If that request
   fails or returns no receipt, it retries once with the full package.
2. An executor behind the snapshot can catch up from `State(N)` and
   `Diff(N+1), ..., Diff(T)`, with quorum signatures covering states in that
   range. It replays the diffs, checks the roots and signatures, and imports the
   verified state at T. It does not need the history before N. Signatures need
   not cover every nonce or all cover the same nonce.

The gateway sends verifiers a snapshot, up to 1,000 diffs after it, and host
state signatures. With no usable snapshot, the next committed diff creates a
candidate. Otherwise it creates one every 100 nonces if none is pending. It
keeps one snapshot with enough signatures and one candidate.
A snapshot is usable when signatures at its nonce reach quorum, or signatures
strictly after it reach quorum. These sets are counted separately. Each owner's
slot weight counts once in each set. See [the proof](snapshot-validation.md).

The main functions are:

- `BuildRefusalPackage`: collect the snapshot, diffs, and signatures.
- `VerifyRefusalPackage`: check the proof and replay the diffs in temporary state.
- `CatchUpForChallenge`: apply missing diffs, or verify and import the package if
  the executor is behind the snapshot.
- `ImportVerifiedSnapshot`: save the verified state before installing it.

An executor at or past the snapshot nonce applies only missing diffs. It does
not decode the snapshot. Proof verification checks signing-key authorization
independently of the snapshot's claims.

Without a usable snapshot and signatures, refusal voting waits. After startup
or restart, the gateway must build a new proof through normal activity. A
snapshot expires once more than 1,000 diffs follow it. There is no fallback to
loading the full history. Receipt, no-receipt, and executor-error handling stay
the same after a valid challenge.

Import saves the snapshot, its state root, the latest nonce, and `imported_nonce`
in one transaction. If a stale instance tries to write a diff covered by an
import, it reloads the saved state before continuing. Startup and HA recovery
check the saved snapshot root, even when no diff is stored at the imported
nonce. Old snapshots remain readable. Later host snapshots also include a root.

Imported snapshots omit the index of sealed inference IDs. Caches are rebuilt
from the remaining records. Late transactions for sealed IDs are still rejected,
but may report `inference not found` instead of `inference sealed`. Import does
not recreate historical per-inference diagnostic rows. State roots and
transaction signatures stay the same.

Deploy this as v4.2 and update all hosts together. Set the protocol version at
build time. The branch name does not set it. Do not ship this as a v4.1 patch.
Older binaries cannot handle the new snapshot format, import metadata, or
refusal package.

The 1,000-diff limit does not limit bytes. Snapshot size depends on the number
of unsealed inference records, not the total nonce count. Records contain
hashes and metadata, not prompts or response text.

`GET /sessions/:id/state` returns the host's local `nonce` and `state_root`
(base64). Use the usual signature and timestamp headers, signing an empty body.
The session owner and group hosts, including authorized warm keys, can call it.
`HTTPClient.GetState` handles the request.

After verifying the full proof, the verifier queries the executor. If its nonce
and root match a diff in the verified tail, the challenge sends only the diffs
after that nonce, with a `base_root` and no snapshot or signatures. At the target
nonce, no diffs are needed. Otherwise it sends the full package. This includes
query failures and nonces at or before the snapshot, whose root is not stored
separately in the package.

The executor checks the shortened request against its local state. If the GET
and challenge reach different HA instances and the tail no longer connects,
it returns HTTP 409. An error or empty receipt from a shortened challenge triggers one retry
with the original full package, without another GET. A receipt rejects the
timeout. No receipt or an error on that full challenge allows the timeout vote.
Repeated 409 responses cannot suppress voting.
A shortened request cannot be used as a verifier proof or imported snapshot.
