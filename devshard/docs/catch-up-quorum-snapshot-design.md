# Host catch-up from signed checkpoints

Draft for discussion. Replaces the gateway pushing a host's entire
missed history before an inference. Written against
`ak/grpc-devshard-transport` (`87275536f`). Related review:
`[pr-1754-catchup-budget-findings.md](./pr-1754-catchup-budget-findings.md)`.

[Decisions](#decisions) records what is agreed and why.
[Design gaps](#design-gaps) lists **G1 … G16**: problems the design
itself has once it is built as described, with options and a proposed
default. [Implementation prerequisites](#implementation-prerequisites)
lists what the current code has to change first.

## Problem

Every host-bound request carries every diff after that host's
`hostSyncNonce`. A host 5,000 nonces behind receives 5,000 diffs plus the
prompt in one body, which crosses the 10 MiB host cap. PR 1754 splits
that into chunks of at most 200 diffs sent ahead of the inference, but
the gateway still pushes all 5,000, in about 25 round trips, on the chat
request, while the executor waits.

A host that far behind does not need the history. It needs a state the
group has signed, and the diffs after it.

## Overview

- **Checkpoints.** Every **K = 500** nonces, each host signs the state
root it computed at that nonce and returns the signature to the
gateway. When signatures reach 2/3 of slot weight, the gateway holds a
**certificate** for that checkpoint.
- **Normal path.** A host that is behind the latest certified
checkpoint takes that checkpoint's snapshot from any host that signed
it, checks the snapshot and the gateway's diff at that checkpoint
against the certificate, pulls the diffs after it from peers, and
applies the last **W = 4 × slotsNum** diffs, which arrive with the
request.
- **Background catch-up.** Every message advertises the gateway's tip
and latest certificate. A host that sees a gap starts catching up from
peers at once, so it is usually current before its next inference.
- **Forks are caught at certified boundaries.** Every diff catch-up
takes is checked against a certificate where one exists, so a
gateway that signs two branches is caught with proof.
- **Fallback.** When the latest certificate is old, because a third of
the weight is lagging or down, or because the lagging host itself
holds more than a third, the host fetches the missing diffs from other
hosts in parallel chunks of 500.
- **The gateway does not push history.** It provides the certificate
and the last W diffs. The host heals itself from other hosts.
- **Stop only when more than half the slot weight is unreachable.** The
gateway keeps minting while a minority lags, and stops only when it
cannot reach a majority.

## Terms

- **Checkpoint C.** A nonce that is a multiple of K. Hosts already
write a local snapshot at these nonces (`SnapshotInterval`).
- **Checkpoint signature.** A host's state signature,
`StateSignatureContent{StateRoot, EscrowId, Nonce=C}`, over the root
it computed by applying diff C itself.
- **Certificate.** Checkpoint signatures at C from 2/3 of slot weight
(`state.QuorumThreshold`), all over the same root. About 1 KB for 16
slots. It travels in diffs as an optional field and is never part of
the state, the root, or the snapshot.
- **Ccert.** The latest checkpoint the gateway holds a certificate
for.
- **L.** A host's applied nonce. **T.** The request's own nonce, the
gateway's tip.
- **W.** The tail the gateway sends inline: **4 × slotsNum** diffs, 64
for 16 slots, about 64 KB. Nonces rotate over the slots, so a host
addressed on rotation is about slotsNum behind; the factor 4 is slack.

## Diff sizes

Estimated from the message fields, protobuf encoding:


| Part                                                   | Size                               |
| ------------------------------------------------------ | ---------------------------------- |
| Diff envelope (nonce, user signature, post-state root) | ~110 B                             |
| `MsgStartInference`                                    | ~150–250 B                         |
| `MsgConfirmStart` (receipt and executor signature)     | ~200–300 B                         |
| `MsgFinishInference`                                   | ~150–250 B                         |
| `MsgValidation`, `MsgValidationVote`                   | ~100–200 B each                    |
| `MsgHeightAck`                                         | ~150 B per slot per heartbeat turn |


An inference takes one nonce and contributes a start, a confirm, a
finish, and usually a validation to some diff. That averages about 1 KB
per nonce, so 500 diffs are about 0.5 MB, or about 0.7 MB as base64
JSON. That fits well under the 10 MiB caps.

There is no hard bound. `composeDiffLockedInclude` puts every pending
mempool transaction into the next diff, so after an outage one diff can
carry thousands of transactions. Every transfer below is still capped
in bytes. The numbers should be confirmed from a real journal.

## Checkpoint signing

1. When a host applies diff C, by any path (request, replay), it signs
  the root it computed at C. The acceptance check applies: a host that
   disputes the state does not sign.
2. It returns the signature in its next response to the gateway.
  A replay that crossed several checkpoints returns one per checkpoint.
3. A host that jumped to C through a snapshot does not sign C; it did
  not compute it. It signs later checkpoints it reaches by applying
   diffs.
4. The host keeps its snapshot at C at least until a newer checkpoint is
  certified, so it can serve C.

## Checkpoint snapshots

- **Never skipped.** A host writes a snapshot at every checkpoint it
  applies. If the previous write is still in flight, the new one is
  queued, not dropped.
- **Retained.** A host keeps the snapshot at the latest certified
  checkpoint until a newer one is certified, and keeps the snapshots at
  later, not yet certified checkpoints until then. A new checkpoint
  does not overwrite the certified one.
- **Served by any host that has it.** Signers first. A host that jumped
  to C holds the snapshot it restored and can serve it too; the root
  check does not depend on who sent it. If a host lacks it, try
  another.
- **Sent without derived parts.** The transferred snapshot holds what
  the root covers: balance, host stats, fees, live inferences,
  `SealedAcc`, warm keys, height-sync flags, the floor window, and the
  tracker. It omits `committedEntries` (rebuilt from live inferences),
  `sealedNonces` (not needed after a jump), and `Config` and `Group`
  (from chain); see
  [Decisions](#everything-in-a-snapshot-is-covered-by-the-root).
- **Authenticated by the root, not signed.** No one signs snapshot
  bytes. The certificate covers only the 32-byte root at C; the host
  recomputes the root from the transferred fields and compares. Encoding
  can differ between hosts without breaking anything.
- **Chunked transfer.** A snapshot RPC with its own cap sends chunks
  and a manifest of chunk hashes. The manifest catches corruption in
  transit. A peer that sends wrong content consistently is caught only
  by the root check at the end; the host then retries another host.
  At most a third of the weight can do that.

**Size.** The snapshot no longer grows with the escrow's life. It is
dominated by live inferences, which are real state in the root and
cannot be derived without replaying diffs back to the oldest live
start:

| Part | Bound | Size |
| --- | --- | --- |
| Balance, host stats, fees, warm keys | Group size | A few KB |
| Live inferences, terminal (Validated, Invalidated, TimedOut) | Seal after about `InferenceSealGraceNonces` plus one auto-seal sweep (150 nonces) | Small |
| Live inferences, Finished | Seal after `InferenceSealGraceSeconds` + `ExecutionTimeout` from `ConfirmedAt`, 3,600 + 1,920 s, about 1.5 h | Rate × 5,520 s × ~190 B |
| Live inferences, Pending, Started, Challenged | In flight, bounded by timeouts | Small |
| Floor window | `DefaultFloorWindow` = 4,096 entries | At most ~250 KB |
| Tracker | Retained turns | Tens of KB |

A valid `MsgValidation` only adds to `VotesValid`; the inference stays
Finished. Only an invalid validation (to Challenged, then a vote) or a
timeout reaches a terminal status early. So nearly every successful
inference stays live for the full 1.5 h clock gate (Trigger C in
`inference-lifecycle.md`).

**Per record**, as `InferenceRecordProto`, about 190 B for a Finished
inference:

| Fields | Bytes |
| --- | --- |
| `prompt_hash`, `response_hash` (32 B each) | ~68 |
| `model` string, e.g. `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` | ~20–40 |
| `validated_by`, always 16 bytes (`Bitmap128.Bytes`), field tag above 15 | ~19 |
| `inference_id`, `status`, `executor_slot`, lengths, token counts, costs, votes | ~35 |
| `started_at`, `confirmed_at`, `started_at_height`, `confirmed_at_height` | ~24 |

At 50 inferences per second that is about 276,000 records, about 52 MB;
at 5 per second about 5 MB; at 1 per second about 1 MB. Each is far
below a diff replay of the same 1.5 h (about 1 KB per nonce). These
estimates should be confirmed on a real escrow.

**Making it smaller.** Snapshot size sets how fast a host that is far
behind catches up, so the plan reduces it:

- **Split the record at Finish (planned).** Keeps a stub of about 63 B
  live instead of about 190 B, cutting the live set by about two thirds:
  at 50 inferences per second from about 52 MB to about 17 MB. See
  [Decisions](#split-the-inference-record-at-finish).
- **Compress the transfer.** Model strings repeat and most integers
  are small; only the two hashes are incompressible. Needs no protocol
  change.
- **Shorter clock gate.** `InferenceSealGraceSeconds` is a chain
  parameter frozen per escrow; the live set scales linearly with it.
- **Close the validation window explicitly.** The lifecycle doc marks
  Trigger C as temporary, to be replaced by a protocol-visible "window
  closed" event. That would bound the live set by the validation
  window instead of 1.5 h.
- **Compact entries** (model as an index into the session config) save
  a further ~20 B per Pending or Started record; Finished stubs no
  longer carry the model.

## Gateway side

1. **Collect.** The gateway checks each checkpoint signature against
  chain-resolved keys only (see
   [Decisions](#checkpoint-signatures-verify-against-chain-keys-only))
   and compares the root with its own `PostStateRoot` at C. A different root is divergence evidence (G12). At 2/3 of slot
   weight it stores the certificate.
2. **Distribute in the next diff.** The gateway attaches each new
  certificate to the first diff it mints after the certificate forms,
  in an optional field outside the signed diff content (see
  [Decisions](#certificates-travel-in-diffs-outside-the-state)). Every
  host receives every diff, in a tail or from peers, so every host
  sees it. Hosts store it in the journal row at C and can serve it to
  peers through `GetSignatures(C)`.
3. **Send.** Every host-bound message (inference, heartbeat, vote,
  challenge, sync) to a host with cursor L carries:
  - **The tip T and the nonce of Ccert**, always.
  - **Diffs max(L+1, T−W+1) … T.** A current host gets everything it
   is missing; a host further behind gets the last W and fills the gap
   from peers. The PR 1754 byte budget still applies to the tail.
   Exception: while the half rule has stopped minting, sync messages
   carry up to K diffs, since peers may not hold them (see
   [Decisions](#stop-only-when-more-than-half-the-slot-weight-is-unreachable)).
  - **The certificate at Ccert**, when L < Ccert, in the same optional
   field on the first diff of the tail. Diff Ccert, which the
   fork check needs, is in the tail when Ccert > T − W, and otherwise
   comes from the first chunk the host fetches from a signer (see
   [Decisions](#forks-are-checked-at-certified-boundaries)).
  The gateway takes L from the applied nonce each host reports in its
  responses, so background catch-up is not resent.
4. **No push.** The gateway does not drain older diffs to a host.
   While more than half the slot weight is unreachable it mints nothing
   and sends only no-nonce sync messages (see
   [Decisions](#stop-only-when-more-than-half-the-slot-weight-is-unreachable)).
5. **Finalize** waits until 2/3 of slot weight has caught up and signed
  the final root. That is already true today.

## Host side

On any host-bound request (inference, heartbeat, vote, challenge), with
diffs starting at **F**:

1. **F ≤ L + 1.** Apply as today.
2. Otherwise, pick a start point by cost (see
  [Choosing diffs or a snapshot](#choosing-diffs-or-a-snapshot)):
  - **Own state at L**, if it is valid and pulling L+1 … T−W from
   peers costs less than the snapshot path. Always when L ≥ Ccert.
  - **Snapshot at Ccert** otherwise: verify the certificate's
   signatures against chain-resolved keys, never against warm keys
  from a snapshot. If the signatures that verify fall below 2/3,
  for example after a warm-key revocation, treat the certificate as
  missing: use an older one that verifies, or start from own state
  (see [Decisions](#checkpoint-signatures-verify-against-chain-keys-only)).
  Fetch the snapshot from a host that has it, signers first (see
  [Checkpoint snapshots](#checkpoint-snapshots)), restore it into a
  scratch state machine, and accept
  it only if its root equals the certified root. The root covers
  every field the snapshot carries, and the rest is taken from chain
  or rebuilt (see
  [Decisions](#everything-in-a-snapshot-is-covered-by-the-root)).
3. **Fork check at Ccert** (snapshot start). Before applying any diff
  after Ccert, check that diff Ccert's signed post-state root equals
  the certified root. The diff comes from the request's tail when
  Ccert > T − W, and otherwise from the first chunk fetched from a
  signer. If the roots
  differ, fetch diff Ccert from a signer, keep both diffs as proof of
  equivocation, and do not apply or serve the request (see
  [Decisions](#forks-are-checked-at-certified-boundaries)).
4. If the start point is before F − 1, fetch the gap from other hosts
  (below).
5. Apply the request's diffs. Then serve.

While catching up, the host does not serve. That is a non-answer, and
the normal timeout and challenge decide whether it is a miss.

One catch-up per escrow at a time. Concurrent requests for the same
escrow wait on it or get a "catching up" answer.

## Choosing diffs or a snapshot

Diffs are the default. A snapshot is only worth it when the host is far
behind or its own state is unusable.

1. **Is own state valid?** The host's state at L is usable if its
  journal is intact from its base (nonce 1, or the checkpoint it
  jumped to) up to L, and its root matches every certificate it holds
  at or below L. A mismatch means it diverged or was fed a fork: it
  takes the snapshot path (G12,
  [Decisions](#forks-are-checked-at-certified-boundaries)). A host
  with no state at all has only the snapshot path, or diffs from
  nonce 1.
2. **Ask the sizes.**
  - **Diff path:** the bytes of L+1 … T−W. A peer's journal-range
   query returns the byte size of a range (G7); without it, the host
   estimates from the nonce count and the average diff size it has
   seen.
  - **Snapshot path:** the size of the snapshot at Ccert, from a
   signer (a size-only call, or the manifest header), plus the bytes
   of Ccert … T−W.
  - **Sizes are not signed.** A peer that lies only skews the choice.
   The host aborts a download that runs well past the advertised
   size and takes the other path.
3. **Compare bytes and apply time.** Replay verifies the signatures in
  every diff (user signature, receipts, proposer signatures); restoring
  a snapshot only hashes. Weight each diff by a measured apply cost.

**The break-even is a time behind, not a nonce count.** Both sides grow
with the inference rate r: the live set is about r × 5,520 s records,
and the gap is about r × (time behind) diffs of about 1 KB each. So the
break-even time behind is about 5,520 s × (record size / 1 KB),
whatever the rate:

| Live record | Break-even time behind |
| --- | --- |
| Full record, ~190 B (today) | ~17 min |
| Stub after the split, ~63 B | ~6 min |

A host that restarted or lost its network for a few minutes replays
diffs. A snapshot is for long absences, divergence, or a host with no
state. At low traffic, heartbeat
diffs make the diff side relatively heavier and the break-even
shorter. Measure both on a real escrow.

## Background catch-up

A host does not wait for an inference to find out it is behind.

1. **Trigger.** Any message whose advertised tip T is ahead of the
  host's L starts a catch-up, unless one is already running for the
  escrow. The steps are the Host side steps, with no request to
  serve at the end.
2. **Head.** Peers hold diffs only up to their own tip, and the newest
  ones reached only the host addressed at that nonce. The host takes
  what peers have; the rest arrives in the tail of its next message.
3. **Report.** The host returns its applied nonce, and any checkpoint
  signatures it produced while replaying, in its next response. The
  gateway updates the host's cursor from it, so the next tail starts
  after L.
4. **Serving.** A request that arrives during a background catch-up
  waits on it, like any concurrent request.

Heartbeats every 12 s advertise the tip to every host, so a host that
fell behind usually starts catching up within one heartbeat interval,
before its next inference.

## Diffs from other hosts

Used for the gap between the host's start point and the request's
tail: always when the host is more than W behind, and for longer
ranges when the latest certificate is old (fallback).

1. **Find holders.** Ask peers which range they hold (journal-range
  query, below). A host that jumped holds nothing before its jump
  point; a host that is behind holds nothing past its tip.
2. **Split** the gap into chunks of at most K = 500 nonces, aligned to
  checkpoints: C … C+499, C+500 … C+999, and so on. The first and last
  chunks may be shorter.
3. **Fetch chunks in parallel**, each from a different holder, through
  `GetDiffs`. Ranges at or before Ccert come from the certificate's
  signers first, since they applied the certified branch.
4. **Apply in nonce order.** Each diff carries the gateway's signature and
  its post-state root, so a peer cannot forge one. It can still serve
  a branch the gateway forked.
5. **Check** the root at every certified checkpoint in the range, and
  check that the request's first diff applies on top of the last
  fetched one. On a mismatch, locate the fork, keep the evidence, and
  continue on the certified branch (see
  [Decisions](#forks-are-checked-at-certified-boundaries)).

**Journal-range query.** A peer answers "I hold A … B" for the escrow,
and, for a requested range, its byte size. The size feeds the choice
between diffs and a snapshot. The answer is advisory: a peer that
claims a range and does not serve it is treated as withholding.

**`GetDiffs` limits.** Every response, to any caller, is capped on both
axes:

- **At most K = 500 nonces.** A host never returns more than 500 diffs
  in one response, whatever `from` and `to` ask for.
- **At most a byte cap**, below the 10 MiB message caps on the gRPC
  branch. Burst diffs can make 500 nonces exceed it.
- **Paging.** A response stops at whichever limit comes first and
  returns the next nonce to ask for. The caller continues from there.
  Diffs in a response are contiguous from `from`; a gap or a short
  page without a next cursor counts as withholding.

**Timeouts and retry.** Each chunk has its own timeout. On a timeout,
an empty or short answer, or a diff that fails its signature or root
check, the host re-fetches the rest of the chunk from another holder
and deprioritizes that peer for this catch-up. Applied diffs are kept;
only the remainder is re-fetched.

**Memory bound.** Chunks download in parallel but apply in order. The
host keeps at most a fixed window of chunks downloaded or in flight
ahead of the one being applied, for example 4. The next chunk starts
only when the oldest is applied. Memory is then bounded by the window
× the byte cap.

A host holding more than a third of the weight always uses this path
when it falls behind: newer checkpoints cannot be certified without it.
It starts from its own state, or from an older certificate that its own
signature helped form.

## Optional last step: certificates without the gateway

Implemented only after everything else. Without it, a gateway that
withholds certificates costs speed, not correctness: hosts start from
their own state and pull diffs.

A certificate is valid because of the host signatures in it, not
because the gateway collected them. Every host signs checkpoint C when
it applies diff C (Checkpoint signing), so a catching-up host can
collect the signatures itself.

1. **Target.** The host asks peers which ranges they hold (journal-range
  query) and picks the newest checkpoint most of them have passed,
  C = floor(min tip / K) × K.
2. **Stored certificate.** It calls `GetSignatures(C)` on a few peers.
  If one holds the certificate the gateway distributed, it verifies it
  against chain keys and stops here.
3. **Assembled certificate.** Otherwise it asks every peer for that
  peer's own checkpoint signature at C. It verifies each against chain
  keys, groups them by root, and checks whether one root has 2/3 of the
  weight. If so, that set is a certificate. The host stores it and
  serves it onward.
4. **Older checkpoints.** If no root at C reaches 2/3, it tries C − K,
  C − 2K, and so on, then falls back to own state and diffs.
5. **Continue as usual.** Choose the start point by cost, fetch the
  snapshot from a signer, pull diffs from peers. Every diff carries the
  gateway's signature, so nothing the host applies bypasses the
  gateway's ordering.

- **Cost.** One call to each peer, about 100 B per signature, and only
  when no stored certificate is found.
- **Not the dropped dynamic path.** It uses checkpoint signatures hosts
  already hold, at nonces where snapshots exist; no replay and no
  fan-out on the normal path.
- **Needs** each host to keep its own checkpoint signature in the
  journal row at C, not only the certificate, and to return it through
  `GetSignatures(C)`. A host that jumped to C did not sign C and has no
  signature to give.

## Worked example

Group of 16 slots, K = 500, T = 5,210. Host A is at 100. The others are
current. The checkpoint at 5,000 was certified at about 5,020.

W = 4 × 16 = 64.

- The request to host A carries the tip, the certificate at 5,000,
and diffs 5,147 … 5,210, about 64 KB.
- Host A fetches the snapshot at 5,000 from one of the 11 or more
signers, and diffs 5,000 … 5,146 from peers, about 0.15 MB. It checks
the root against the certificate and against diff 5,000, and installs
the snapshot.
- It applies 5,001 … 5,146, then 5,147 … 5,210 from the request, and
serves.
- If the snapshot is larger than the 5,000 or so diffs host A is
missing, it starts from its own state at 100 instead and pulls
101 … 5,146 from peers (Host side, step 2).
- With background catch-up, host A usually starts all this on the
heartbeat that first shows it the gap, and is current by the time
its inference arrives.

Fallback: six of 16 slots have been down since 3,000, so the latest
certificate is at 3,000. The request carries the certificate at 3,000
and diffs 5,147 … 5,210. Host A takes the snapshot at 3,000 and
fetches 3,000 … 3,499 from one host (diff 3,000 is checked against the
certificate, not applied), 3,500 … 3,999, 4,000 … 4,499,
4,500 … 4,999, and 5,000 … 5,146 from four more, in parallel. It
applies them in order, then the request's diffs.

## What stays from PR 1754

The byte budget on every host-bound body, the refusal of a prompt that
cannot fit before a nonce is spent, and the "our refusal is not a host
fault" accounting. The inference-path drain loop and the per-host gate
held for the whole drain go away.

## Decisions

### Certificate threshold is 2/3

One honest signature would be enough to make the root correct. The
threshold is 2/3 to keep settlement as hard to attack as today. A host
that jumps to a false state keeps applying valid diffs on top of it and
then signs the final root, adding its weight to that state. With a
threshold t, dishonest hosts holding t of the weight, together with the
gateway, could recruit lagging honest hosts into a false settlement. A
threshold below 2/3 lowers that bar from 2/3 to t.

### Disputes withhold checkpoint signatures

A host that disputes the state at nonce **D** does not sign D or any
later checkpoint, since every later state descends from D. It still
signs checkpoints before D, so a certificate from it is never later
than D − 1.

- A dispute by less than a third of the weight does not block
certificates.
- A dispute by more than a third blocks certificates at and after D.
Lagging hosts then catch up through the fallback path. Settlement
fails at finalize, as it does today.
- The only acceptance check today is the staleness check
(`StalenessChecker`): the host withholds while one of its own
transactions has waited in the mempool for more than `grace` nonces.
The dispute ends when the transaction lands at nonce E, and the host
signs again from E.
- A host that already signed a checkpoint cannot take it back. A dispute
applies from the first checkpoint it has not signed.

### Not serving while catching up is a miss

A host that cannot catch up does not serve, and gets no special
treatment. The gateway escalates, the refusal timeout runs, and the
verifier challenges. If the host has caught up by then, it produces the
receipt; otherwise it takes the miss. Staying behind on purpose costs
the same as refusing.

### A verifier's own catch-up failure abstains

A timeout vote request is a host-bound request like any other: it
carries the tip, the certificate, and the last W diffs, and a verifier that
is behind catches up first (Host side). Today a failed challenge call
counts as "accept the timeout" (`VerifyRefusedTimeout`), and so does an
unreachable executor mempool (`VerifyExecutionTimeout`). If the
verifier's own catch-up failure landed on that path, the verifier would
blame the executor for its own lag.

The verifier separates the two:

| Outcome | Cause | Vote |
| --- | --- | --- |
| Verifier's own failure | Its catch-up failed or did not finish; it does not hold the inference in the expected status; its catch-up used up the vote's time budget; the executor rejected the challenge content as invalid, which may be the verifier's fault | **Abstain** |
| Executor's failure to answer | Executor unreachable; it answered "catching up" or failed its own catch-up (see [Decisions](#not-serving-while-catching-up-is-a-miss)); it answered without a receipt | **Accept the timeout** |
| Executor answered | Receipt produced, or a Finish in the executor's mempool | **Reject** |

- **Order.** The verifier finishes its own catch-up before it contacts
  the executor, and the challenge gets its own time budget. A
  catch-up that runs past the vote's deadline abstains; it never
  reaches the challenge.
- **Tally.** The gateway counts only accept weight
  (`HasSufficientTimeoutVotes`: accept weight above `VoteThreshold`),
  so an abstain weighs like a reject. It cannot punish the executor.
- **An abstain is not a host failure.** The gateway does not reset the
  verifier's cursor for it (`forgetHostState` runs on vote errors
  today) and does not collect recovery transactions from it.
- **Retry.** Abstentions can keep the accept weight below the
  threshold, so a refusing executor is timed out later rather than
  never. When a vote round ends without quorum and abstentions are why,
  the gateway retries it after the next heartbeat round, when lagging
  verifiers have caught up.
- **Not scored.** An abstain carries no per-vote penalty. A verifier
  that abstains is behind, and a host that is behind already takes
  misses on its own inferences. Abstentions are counted per verifier in
  metrics, so a host that abstains persistently is visible.

### Stop only when more than half the slot weight is unreachable

A lagging minority does not stop the gateway. A lagging host can
always catch up, from a certificate or from its own state, through
other hosts' journals. Settlement needs 2/3 at finalize, and finalize
already waits for lagging hosts today.

The gateway stops minting, every kind of nonce, while more than half
the slot weight is unreachable:

- **Why half.** With a majority unreachable, most executor slots fail
  and escalate and nothing can settle, so stopping costs little. It
  bounds the diffs that no host received to about K, and stops a
  reachable minority from becoming the only holder of new diffs. A
  third would stop service while two thirds can still serve.
- **Unreachable, not merely behind.** A slot counts only if its
  confirmed cursor (`hostSyncNonce`) is more than K nonces behind
  **and** either the gateway's last send to it failed or it has not
  returned a successful response for two heartbeat intervals.
  - A healthy host that hears only heartbeats is 600 nonces behind
    between contacts at 50 nonces per second (G16), but its last send
    succeeded, so it does not count. Nonce distance alone would stop
    the gateway for nothing.
  - A failed send counts at once. Waiting two heartbeat intervals
    (24 s, about 1,200 nonces at 50 per second) would break the bound
    of K.
- **How it clears.** While stopped, the gateway sends sync messages that
  mint no nonce, as `SyncHosts` and `sendCatchUp` do today. Each carries
  the latest certificate and the last diffs, up to K. A host that
  answers catches up (the rest from peers, as usual) and its cursor
  reaches T. When no more than half the weight is unreachable, the
  gateway resumes.
- **Clients** get a retryable 503 while stopped.
- **All nonces stop.** Timeout and finalize diffs would fail while a
  majority is unreachable, and finalize needs 2/3 anyway.

**Diffs minted that no host received.** Compose persists the diff and
advances the nonce before sending it (`composeDiffLockedInclude`), so a
failed send still spends the nonce. Without the rule, a gateway that
loses its path to every host at nonce 5,000 for 10 minutes keeps
minting: escalation spends a nonce per attempt and heartbeats mint
every 12 s. At 50 attempts per second that is about 30,000 nonces, about
30 MB, that exist only on the gateway. They do not fit the byte budget,
the gateway does not serve history, and no host can catch up. With the
rule, the gateway stops after about K = 500 nonces, about 0.5 MB, which
fits one sync message. The first host reached receives all of them and
serves them to the others. A partial partition is covered the same
way: if the gateway reaches only 40% of the weight, it stops instead of
making that minority the only holder of new diffs.

**Accepted: every holder of a range is lost.** Group of 16 equal slots.
Six hosts have been down since nonce 3,000, so the latest certificate
is at 3,000. Six of 16 is 37.5%, so the gateway keeps going, and the
other ten apply 3,001 … 5,400. If all ten then lose their data at once,
for example a shared storage failure in one operator's infrastructure,
only the gateway holds 3,001 … 5,400. The escrow cannot settle, since
no 2/3 can sign any state after 3,000, and it expires through the
chain's escrow timeout. This needs a mass outage and a mass data loss
together. The gateway does not serve history for this case.

### Certificates travel in diffs, outside the state

The certificate rides on the diff envelope as an optional field, next
to the user signature and outside `DiffContent`:

- **Not signed by the gateway, not in the root, not in the snapshot.**
  `UserSig` covers `DiffContent{Nonce, Txs, EscrowId, PostStateRoot}`;
  the certificate field is outside it, so attaching one does not change
  what the gateway signs or what any host computes. A certificate
  authenticates itself: 2/3 of slot weight, checked against chain keys.
- **Which diffs carry it.** The first diff the gateway mints after the
  certificate forms, and the first diff of the tail sent to a host
  behind Ccert. Every host receives every diff, so the certificate
  reaches every host within one rotation or one heartbeat.
- **Peers propagate it for free.** A host stores each diff as received,
  field included, and the certificate in its journal row at C. A host
  fetching that range through `GetDiffs` gets the certificate with it.
- **Optional.** A diff without the field is valid, and applying a diff
  never reads it. A host with no certificate starts from its own state
  and pulls diffs. The protocol works without certificates at all;
  they only make the snapshot path possible. Older binaries ignore the
  unknown field.

### Checkpoint signatures verify against chain keys only

A signature can come from the slot's validator key or from a warm key.
Warm-key bindings live in the escrow state, so a lagging host does not
know the ones bound after its tip, and taking them from the fetched
snapshot is circular: the snapshot is what the certificate vouches for.

Every checker, the gateway when collecting and a host when catching up,
resolves keys without the snapshot:

1. **Slot owner.** The slot's validator address comes from the escrow's
  group on chain (`bridge.EscrowInfo`), not from the snapshot.
2. **Accept** a signature that recovers to that validator address, or
  to a warm address the chain confirms as granted by it
   (`ChainBridge.VerifyWarmKey`, an authz grant lookup with a cache).
3. **Never** accept a warm key only because it appears in
  `sm.WarmKeys()` or in the snapshot's warm-key map.

A host whose warm key is not granted on chain cannot contribute to a
certificate. Checking a 16-slot certificate costs up to 16 grant
lookups, mostly served from the cache.

**A revoked grant strands a certificate.** The grant lookup answers "is
it granted now", not "was it granted when it signed". If a host revokes
its warm key after signing C, its signature at C stops verifying, and
the certificate may fall below 2/3. The checker counts only the
signatures that verify now. Below 2/3, it treats the certificate as
missing:

- The host uses an older certificate that still verifies, or starts
  from its own state and fetches diffs (fallback path).
- This costs speed only. Nothing is accepted that the chain does not
  confirm now.
- `ChainBridge` caches grant answers, so two checkers may disagree for
  a while. Each acts on its own answer: one takes the snapshot path, the
  other the fallback. Both reach the same state.

### Everything in a snapshot is covered by the root

A peer must not be able to serve a snapshot whose root matches and
whose other contents are wrong. Today the root covers host stats, fees,
version, phase, balance, `SealedAcc`, live inferences, warm keys, and
the seven height-sync flags. The rest of the snapshot is handled as
follows:

| In the snapshot | Used for | Treatment |
| --- | --- | --- |
| `committedEntries` | Encoded bytes of live inferences; sealed ones are deleted at seal | Not sent. Rebuilt from `state.Inferences` (`rebuildCommittedEntriesLocked`) |
| `sealedNonces` | Error type for sealed ids, duplicate-id guard, observability index | Not sent; not needed after a jump (below) |
| `heightSyncFloor` | L0: later stamps are checked against the floor as of their producing nonce, which can be before C | Covered by a floor hash chain in the root |
| Turn tracker, `HeightSyncLastCompletedHeight`, `HeightSyncLatestTurnStart` | L1–L3 checks on heartbeats and acks | Added to the snapshot; covered by a tracker digest in the root |
| `Config`, `Group` | Costs, seal gates, timeouts, slot owners | Taken from chain at bind (`SessionConfigAtBind`, `EscrowInfo`); the sent copy is ignored. Also hashed once into the root |
| `FinalizeNonce` | Finalize rounds | In the root |
| `EscrowID`, `LatestNonce` | Identity and position | Already signed in `StateSignatureContent` |

**Root change.** A new `rest_hash` version:

```text
rest_hash_v3 = sha256(balance || inferences_hash_v2 || warm_keys_hash || height_sync_hash || aux_hash)
aux_hash     = sha256(floor_chain_hash || tracker_digest || finalize_nonce || config_group_hash)
```

- **`floor_chain_hash`.** A running hash over floor entries, updated
  only when the floor rises. An in-place raise of the last entry
  rehashes from the hash before it. The snapshot sends the retained
  window and the hash before its first entry, and the host recomputes
  forward.
- **`tracker_digest`.** A hash over the tracker's retained turns and its
  seed values, updated on diffs that carry height-sync traffic.
- **`config_group_hash`.** Computed once per escrow.

**Why it is cheap.**

- **No chain change.** The chain rebuilds the root from HostStats, Fees,
  `rest_hash`, version, and phase, and treats `rest_hash` as opaque
  (`devshard_settlement_test.go`). `rest_hash` stays 32 bytes, so
  settlement on inference-chain is untouched.
- **Small state.** It grows by about 100 bytes.
- **Rare hashing.** Extra hashing runs only when the floor rises or a
  height-sync diff lands.

**Why `sealedNonces` is not needed.** The map records, for every
inference sealed over the escrow's life, the nonce it sealed at. A host
that jumped to C does not need it for ids at or before C:

- **Duplicate start.** A start's id must equal its diff nonce, so no
  start after C can reuse an id at or before C.
- **Late transactions.** A validation, vote, timeout, or finish for a
  sealed id fails with or without the map; only the error type differs
  (`ErrInferenceSealed` or "sealed" versus `ErrInferenceNotFound`).
  The gateway drops failing transactions before signing, so a signed
  diff never depends on which error a host would return.
- **Observability.** The sealed-inference index on a jumped host starts
  at C (G11).

The host can treat "id at or before C and not live" as sealed, so its
error types match other hosts. It fills the map normally for seals
after C. Without the map, the snapshot does not grow with the escrow's
life.

The new formula needs a new protocol version tag, because every party
must compute the same root. Escrows on older versions keep `rest_hash`
v2.

### Split the inference record at Finish

The snapshot is dominated by Finished inferences waiting out the 1.5 h
validation window. After a successful Finish, most of a record never
changes again, yet the whole record stays live until it seals. The
split keeps only the fields later transactions can read or change:

| Stays live (stub) | Read by |
| --- | --- |
| `inference_id`, `status` | Every later transaction |
| `executor_slot` | Self-validation check; `HostStats` on refund |
| `actual_cost` | Refund on Invalidated or error-miss |
| `response_hash` | Signed in error-miss votes (`ErrorMissVoteContent`) |
| `validated_by`, trimmed to the group (2 B for 16 slots) | Dedup of validations and votes |
| `votes_valid`, `votes_invalid` | Tallies |
| `confirmed_at` | The Finished seal clock gate |

Everything else (`model`, `prompt_hash`, `input_length`, `max_tokens`,
`input_tokens`, `output_tokens`, `reserved_cost`, `started_at`, and both
heights) is folded at Finish into a new accumulator and dropped from
the live record:

```text
finished_acc = sha256(finished_acc || inference_id || immutable_entry)
```

- **Root.** The inferences hash covers `finished_acc`, and the live
  part hashes stubs for Finished and Challenged inferences and full
  records for Pending and Started ones. At seal the stub is folded into
  `SealedAcc` as today.
- **Size.** About 63 B per Finished inference instead of about 190 B,
  so the live set shrinks by about two thirds: at 50 inferences per
  second from about 52 MB to about 17 MB. RAM shrinks the same way.
- **Nothing in the state machine loses data.** `MsgValidation`,
  `MsgValidationVote`, `MsgErrorMiss`, and seal read only stub fields.
  Timeouts do not apply to Finished inferences.
- **Validators and challenge voters** still need the folded fields off
  chain, to fetch, check, and re-run the payload. They read them from
  their own journal (the start diff is at nonce = inference id) or the
  host-local inference index, not from the state.
- **A jumped host** has no journal before C. It fetches the start diff
  from a peer, or skips the validation, which is not scored (G11); the
  response hash is in the stub.
- **Protocol version.** It changes the inferences hash, so it ships
  under the same new version tag as `rest_hash` v3.

### Forks are checked at certified boundaries

The gateway is the only sequencer. It can sign two different diffs for
one nonce and send them to different hosts. Each diff on either branch
is valid on its own: it carries the gateway's signature and matches its
own post-state root. A certificate says which branch 2/3 of the weight
applied at C, so catch-up checks what it takes against a certificate
wherever one exists.

- **Snapshot start.** Diff Ccert's signed post-state root must equal
  the certified root. Equal roots mean the request's diffs after Ccert
  build on the certified state. If they differ, the host fetches diff
  Ccert from a signer. Two gateway-signed diffs at one nonce with
  different roots prove equivocation without trusting anyone. The host
  keeps both and does not apply or serve the request.
- **Own-state start.** The host's own state at L may itself be on a
  branch the gateway fed it earlier. If the first diff fetched from a
  signer does not apply on it, the host compares that diff with its own
  journal's diff at the same nonce, keeps both as evidence, and takes the
  snapshot path instead.
- **Fallback ranges.** Ranges at or before Ccert come from the
  certificate's signers first. The host checks the root at every
  certified checkpoint in the range, and checks that the request's first
  diff applies on top of the last fetched one. On a mismatch, it
  fetches the same range from another host and compares the two copies'
  post-state roots, by binary search, to find the first nonce where they
  differ. The two diffs there are the evidence. The host continues on
  the branch that matches the certificate. If no certificate decides,
  because the fork is after Ccert, it holds proof of equivocation and
  does not serve the request.
- **Not caught during catch-up.** A branch that forks after Ccert and
  stays consistent up to the request looks valid. A current host is in
  the same position today. The fork shows at the next checkpoint: the
  host's root there differs from the other signers', so its signature
  cannot join their certificate (G12, G13).
- **Consequence.** What proven equivocation triggers is open; see
  [G13](#g13).

### No dynamic certificates

A certificate at an arbitrary recent nonce N, collected on demand, would
save at most one chunk of 500 diffs over the latest checkpoint. It
needs the same 2/3, a fan-out to every slot per catch-up, and a replay
to rebuild the state at that nonce. Checkpoints are collected during
normal work at almost no cost: one signature per host per 500 nonces.

**Why the saving is small.** The certificate is about 1 KB of
signatures over a 32-byte root. It only authenticates; the host still
downloads the state behind the root, and that state is dominated by
live inferences (see [Checkpoint snapshots](#checkpoint-snapshots)):

| | Checkpoint path | Dynamic path |
| --- | --- | --- |
| Signatures | Certificate arrives with the request | Ask every host for its tip, then collect 2/3 of signatures at N: two fan-outs on the chat path |
| State | Snapshot at C, already stored; 5 MB at 5 inferences/s, 52 MB at 50/s | Snapshot at N, the same size, built by a peer restoring C and replaying up to 499 diffs |
| Diffs after it | At most 500, about 0.5 MB, pulled from peers in parallel | A few |

- **The saving is at most 0.5 MB** out of a download of 5 to 52 MB,
  paid for with two round trips to every host and a replay on the
  serving peer. The tip moves while the fan-out runs.
- **For small gaps no snapshot is worth it.** A host less than about
  17 minutes behind (about 6 minutes after the record split) catches
  up faster from own state plus peer diffs than from any snapshot,
  fresh or not (see [Choosing diffs or a snapshot](#choosing-diffs-or-a-snapshot)).
  A snapshot pays off only for large gaps, where 500 diffs are noise.
- **Signing an old nonce is cheap; the rest is not.** Every host keeps
  the post-state root of every diff it applied, so a signature at N
  costs nothing. The fan-out and the state at N are what cost.
- **Neither path helps** a host holding more than a third of the
  weight; it replays from its own state or an older certificate.

## Design gaps

Status:

- **Closed.** Resolved in the design above.
- **Defense in depth.** Optional hardening. The design is correct
  without it; each entry says what happens if it is never built. Not
  required for implementation.
- **Known limitation.** Accepted and documented. Nothing to implement.


| ID          | Gap                                                   | Status           | Proposed default                                                                        |
| ----------- | ----------------------------------------------------- | ---------------- | --------------------------------------------------------------------------------------- |
| [G1](#g1)   | Forks from the gateway, served directly or by peers   | Closed           | Checked at certified boundaries; see Decisions                                          |
| [G2](#g2)   | Stored certificates after a warm-key revocation       | Defense in depth | Gateway re-verifies Ccert when attaching it; revocation does not wait                   |
| [G3](#g3)   | Snapshot bytes outside the root are not authenticated | Closed           | Covered by `rest_hash` v3; see Decisions                                                |
| [G4](#g4)   | The snapshot at Ccert may not exist, or may be large  | Closed           | Never skipped, retained, sent without derived parts; see Checkpoint snapshots           |
| [G5](#g5)   | Diffs that only the gateway holds                     | Closed           | Half rule and accepted double failure; see Decisions                                    |
| [G6](#g6)   | A verifier that is behind cannot vote correctly       | Closed           | Own catch-up failure abstains, not scored; see Decisions                                |
| [G7](#g7)   | Fetching diffs from peers: ranges, sizes, withholding | Closed           | Journal-range query; 500 nonces and a byte cap per response; see Diffs from other hosts |
| [G8](#g8)   | Certificate availability depends on the gateway       | Closed           | Certificates travel in diffs; peer lookup and assembly as an optional last step         |
| [G9](#g9)   | Certificates stop forming while a third lags          | Closed           | Not a problem: diffs from peers; late signatures complete them                          |
| [G10](#g10) | Request size on the chat path                         | Closed           | Tail of W diffs; background catch-up                                                    |
| [G11](#g11) | A jumped host loses duties and evidence before C      | Closed           | Not a problem: duties read live state; validation is not scored                         |
| [G12](#g12) | A host that diverged: roll back or halt               | Defense in depth | Halt and alert; operator-triggered rollback                                             |
| [G13](#g13) | Equivocation and conflicting checkpoint signatures    | Defense in depth | Keep evidence; decide what proven gateway equivocation does                             |
| [G14](#g14) | Snapshot format across binaries and protocol versions | Known limitation | Incompatible snapshot fails the root check; use another peer or diffs                   |
| [G15](#g15) | The skipped range is trusted to a quorum              | Known limitation | Same trust model as settlement                                                          |
| [G16](#g16) | Rarely addressed hosts catch up on every contact      | Closed           | Background catch-up from own state                                                      |


### G1

**Closed.** Forks from the gateway, served directly or by peers.
Catch-up checks diffs against the certificate at every certified
boundary, prefers signers as sources, and locates a fork by comparing
two copies; see
[Decisions](#forks-are-checked-at-certified-boundaries). What proven
equivocation triggers is in [G13](#g13).

### G2

**Defense in depth.** Stored certificates after a warm-key revocation.
A checker treats a certificate stranded by a revocation as missing and
falls back (see
[Decisions](#checkpoint-signatures-verify-against-chain-keys-only)).

- **Without it.** The gateway may attach a stranded certificate; the
host's own verification rejects it and it falls back to an older
certificate or diffs. One wasted check, no wrong state.

Two optional refinements:

- **Does the gateway re-check stored certificates?** Proposed: it
re-verifies Ccert each time it attaches it, which is cached grant
lookups. If it no longer reaches 2/3, it attaches the newest
certificate that still does. A stranded certificate can also recover:
a lagging host that later replays through C signs it, and the gateway
adds that signature (G9).
- **Should revocation wait until the escrow settles?** Proposed: no.
The authz revoke is a chain transaction that devshard cannot delay
without a chain change, and the fallback already makes a revocation
cost speed only.

### G3

**Closed.** Snapshot bytes outside the root. Every snapshot field is now
in the root, taken from chain, or rebuilt; see
[Decisions](#everything-in-a-snapshot-is-covered-by-the-root).

### G4

**Closed.** The snapshot at Ccert may not exist, or may be large.
Checkpoint snapshots are never skipped, retained until a newer
checkpoint is certified, served by any host that has it, sent without
derived parts, and transferred in chunks; see
[Checkpoint snapshots](#checkpoint-snapshots).

### G5

**Closed.** Diffs that only the gateway holds. Diffs minted that no
host received are bounded to about K by the half rule, and losing every
holder of a range is an accepted double failure; see
[Decisions](#stop-only-when-more-than-half-the-slot-weight-is-unreachable).

### G6

**Closed.** A verifier that is behind cannot vote correctly. Its own
catch-up failure abstains, the executor's failure to answer accepts the
timeout, and an abstain is not scored; see
[Decisions](#a-verifiers-own-catch-up-failure-abstains).

### G7

**Closed.** Fetching diffs from peers: ranges, sizes, withholding. A
journal-range query finds holders; `GetDiffs` returns at most 500
nonces and a byte cap per response, with paging; chunks of at most 500
nonces have their own timeout and retry on another holder; a fixed
window bounds memory. See [Diffs from other hosts](#diffs-from-other-hosts).

### G8

**Closed.** Certificate availability depends on the gateway. The
gateway cannot forge a certificate, only withhold one. Certificates
travel in diffs, so peers pass them on with the diffs they serve (see
[Decisions](#certificates-travel-in-diffs-outside-the-state)); a host
with none starts from its own state. Asking peers for a stored
certificate, or assembling one from their checkpoint signatures, is an
optional last step (see
[Optional last step](#optional-last-step-certificates-without-the-gateway)).

### G9

**Closed; not a problem.** Certificates stop forming while more than a
third of the weight lags. Catch-up then uses the newest certificate
that exists, or own state and diffs from peers
([Diffs from other hosts](#diffs-from-other-hosts)). That path is
always available, so a missing certificate costs speed only. Assembling
one from peers (optional last step) fails for the same reason, since
fewer than 2/3 signed C, and also falls back to diffs. When lagging
hosts replay through C they sign it, and those signatures reach the
gateway in their next response
([Background catch-up](#background-catch-up), step 3), so the
certificate at C completes late.

### G10

**Closed.** Request size on the chat path. A request carries at most
the last W = 4 × slotsNum diffs, about 64 KB for 16 slots, under the
PR 1754 byte budget; the host pulls the rest from peers, usually in the
background before its inference arrives. See the send rule in
[Gateway side](#gateway-side) and
[Background catch-up](#background-catch-up).

### G11

**Closed; not a problem.** A host that jumped to C has no journal
before C. Nothing it must do depends on that journal:

- **Validation and votes.** `collectValidationJobs` reads the live
  records in the state, which the snapshot carries, and the payload
  comes from the executor as for any validator. After the record split,
  the folded fields come from the start diff, fetched from a peer.
  Validation is not scored (`RequiredValidations` and
  `CompletedValidations` are sent as zero and never read on chain), so
  a skipped validation costs coverage only, not the host.
- **Timeout votes.** Pending and Started records are in the snapshot.
- **Serving diffs before C.** Other hosts serve that range; a jumped
  host answers the journal-range query from C.
- **Disputes.** A host's disputes concern its own mempool and state
  after C.
- **Observability.** The host-local inference index starts at C.

### G12

**Defense in depth.** A host that diverged: roll back or halt. A host
whose own root at a checkpoint differs from the certificate has a bug
or corrupted state. The gateway sees it when collecting signatures.
Overwriting the host's state with the snapshot keeps it serving but
hides the bug.

- **Without it.** The host's checkpoint signature cannot join the
certificate, and its own state fails the validity check on its next
catch-up ([Choosing diffs or a snapshot](#choosing-diffs-or-a-snapshot),
step 1), so it moves to the certified snapshot. The escrow stays
correct; the bug is only less visible.
- **Proposed.** Halt the escrow on that host and alert. Rollback to the
certified snapshot is an operator action, and the divergent state is
kept for diagnosis.

### G13

**Defense in depth.** Equivocation and conflicting checkpoint
signatures. Catch-up can end with proof that the gateway signed two
diffs at one nonce (see
[Decisions](#forks-are-checked-at-certified-boundaries)). Two
certificates at one C with different roots need more than a third of
the weight to sign both, which is outside the trust model. A single
slot signing two roots at one C is a smaller, provable fault.

- **Without it.** The fork check still refuses to apply or serve a
forked branch; a conflicting signature still cannot join a
certificate. Only the evidence is dropped and nobody is penalized.
- **Proposed.** The gateway and hosts keep both kinds of evidence.
Decide what proven gateway equivocation triggers: hosts stop serving
the escrow, or the evidence goes on chain. Decide whether conflicting
host signatures feed scoring or go on chain.

### G14

**Known limitation.** Snapshot format across binaries and protocol
versions. A snapshot from a peer on a newer or older binary may not
restore on the fetching host. The root includes the protocol version,
but the encoding may change across binaries within one protocol
version.

- **Accepted.** An incompatible snapshot fails to decode or fails the
root check, so it is never installed. The host tries another signer,
then own state and diffs, which do not depend on snapshot encoding.
A version tag on the encoding would only save a wasted download.

### G15

**Known limitation.** The skipped range is trusted to a quorum. A
jumped host does not re-verify each gateway-signed diff before C. That
is the same assumption as settlement, which accepts a quorum of state
signatures.

- **Accepted.** State it in the protocol notes. A host still checks
every diff after C against its user signature and post-state root.

### G16

**Closed.** Rarely addressed hosts catch up on every contact. A host
that hears only heartbeats, every 12 s
(`heightsync.DefaultHeartbeatInterval`), is about 600 nonces behind
between them at 50 nonces per second. Each heartbeat advertises the tip,
so the host pulls the gap from peers in the background
([Background catch-up](#background-catch-up)), starting from its own
state, since a few hundred diffs cost less than a snapshot (Host side,
step 2).

## Implementation prerequisites

Changes to current code the design depends on. Not design questions.

- **Checkpoint signatures in responses.** `signIfAccepted` signs only
`resp.Nonce`. Add signing at each checkpoint applied, and a response
field that carries them.
- **Certificates.** Gateway-side collection and storage. An optional
certificate field on the diff envelope, outside `DiffContent`, so it
is not covered by `UserSig` or the post-state root. The journal stores
diffs with the field; hosts store the certificate in the journal row at
C and serve it through `GetSignatures`.
- **Snapshot retention.** Checkpoint snapshots must not be skipped
(`snapshotInFlight`) or overwritten before a newer checkpoint is
certified.
- **Snapshot RPC.** A chunked or streamed "snapshot at C" call with its
own cap and a chunk manifest. The current peer caps are 10 MiB. It
sends the catch-up form, without `committedEntries`, `sealedNonces`,
`Config`, and `Group`; restore rebuilds `committedEntries` and starts
`sealedNonces` empty.
- **Peer diff fetch.** `HandleGetDiffs` today returns `from … to` in one
response with no limit. It must cap every response at 500 nonces and a
byte cap and return a next cursor. Its only caller today is the gossip
gap fill (`gossip.go`), which `devshardd` does not run. A
journal-range query that also
returns the byte size of a range. A chunk fetcher with per-chunk
timeout, retry on another holder, and a bounded window.
- **Half rule.** The gateway tracks, per slot, the last send result and
the time of the last successful response. A mint gate covers every
nonce type and returns a retryable 503. `SyncHosts` sends the
certificate and up to K diffs without minting, and runs while stopped.
- **Fork refusal.** A catch-up result that refuses the request when it
proves equivocation. Storing the pair of diffs as evidence is defense
in depth (G13) and optional.
- **Journal base.** `verifySnapshotRoot`, `AccumulateGossipSig`,
`GetDiffs`, and the reconcile gap fill assume a journal from nonce 1.
A jumped host needs a base record at C (root, certificate, snapshot).
- **Session bind without nonce 1.** A cold executor creates the session
from the creator-signed start in diff 1. It must bind from chain
escrow info and the certificate instead.
- **Verify-timeout body.** `ServeVerifyTimeout` loads diffs
`1 … LatestNonce` and forwards them in one `ChallengeReceipt`. It must
send the tip, the certificate, and the last W diffs instead, in the
same release.
- **Split record.** A stub type for Finished and Challenged
inferences, `finished_acc` folded in `applyFinishInference`, and stub
bytes in `SealedAcc`. `collectValidationJobs` and payload checks read
the folded fields from the journal or the host-local inference index.
Same version tag as `rest_hash` v3.
- **Start-point choice.** A snapshot size call (or manifest header),
byte sizes in the journal-range query, and a measured per-diff apply
cost.
- **Tip and cursor.** Every host-bound message carries the tip T, the
nonce of Ccert, and the tail of at most W diffs. Every host response
reports the host's applied nonce, and the gateway sets `hostSyncNonce`
from it.
- **Background catch-up.** A per-escrow worker on the host, started by
any message whose tip is ahead of L, sharing the one-catch-up-per-escrow
lock with request handling.
- **Abstain outcome.** `VerifyTimeoutResponse` gains an abstain outcome
with a cause. `VerifyRefusedTimeout` and `VerifyExecutionTimeout`
return typed errors that tell the verifier's own failure from the
executor's; `HandleVerifyTimeout` runs the verifier's catch-up before
the challenge, under its own deadline. The gateway skips
`forgetHostState` on an abstain and retries a round that missed quorum
because of abstentions. A per-verifier abstention counter in metrics.
- **Dispute record.** Today `signIfAccepted` withholds and keeps no
record. A host must persist each dispute's start nonce, reason, and
end nonce, so its checkpoint signing stays consistent across restarts.
- **`rest_hash` v3.** `ComputeRestHashV2`, `BuildSettlement`, and the
host and gateway root computation get a v3 path under a new protocol
version tag. The snapshot gains the turn tracker and the floor's base
hash.
- **Tracker digest scope.** Confirm which tracker fields the L1–L3 checks
read, and how far back a heartbeat or ack can reference a turn. That
sets what the digest and the snapshot must hold. Today, restoring from
a snapshot without a journal only seeds `lastCompleted` and
`latestTurnStart` (`SeedCompleted`). Either the checks tolerate a
missing tracker history, or that existing fallback can split an escrow
today; if it can, it is a bug to fix regardless of this design.
- **Version gate.** Escrows on older protocol versions keep the PR 1754
chunked push.
- **Optional, last: certificates without the gateway.** Hosts keep their
own checkpoint signature in the journal row at C and return it through
`GetSignatures(C)`; a catching-up host looks up a stored certificate
from peers or assembles one. Implemented after everything above.

