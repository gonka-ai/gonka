# `timeoutclaim` — claim a miss or vote a timeout

## What it owns

- `ClaimsMiss`: the choice between an error-miss claim and a timeout vote, made once. A non-empty Finish transaction claims an error miss with the response payload; otherwise the timeout is voted with the inference payload. The claim and the vote are different messages that the hosts judge by different checks, so the choice is made here, from the Finish, and nowhere else.
- `FinishTx`: the Finish an error-miss claim carries, the one the host's own reply carried for the nonce when it carried one, otherwise the one the session has queued (`FinishSource`, which `*user.Session` satisfies). A claim lands only if the record is finished when the claim applies, and a verifier checks the Finish against the committed response hash, so the reply's Finish is the one that matches whenever the session no longer queues it: a diff that already applied it, through another host's gossip or a compose before the gateway pinned it, empties the queue while the record is finished, and the reply's copy still lands the claim. Where the session never queued the reply's Finish (it failed verification or its height stamp, or the reply was refused before its mempool was read), the record is not finished and a claim from either source cannot land; the cost of the order is that a Finish failing verification is claimed and rejected where a timeout vote could have landed.
- `Payload`: the inference payload a timeout vote carries, built from the nonce's live record plus the prompt and the start time (seconds) it was dispatched with, and none when the nonce has no live record. A verifier re-checks the payload field by field against its own copy of that record, so the record is the authority for every field but the prompt and the start time; the record holds its start in block-time milliseconds and no verifier reads the payload's start time, so the caller passes the one it dispatched with and, for a nonce with a live record, the payload equals the one the host was dispatched with. A nonce leaves the live records only when it is sealed, which happens to a closed record or to every record once the escrow moves to settlement. A verifier that has applied the seal refuses a vote for such a nonce before it reads any payload, and the session's state machine cannot apply its timeout whatever the verifiers answer, so there is nothing to carry.

## How the gateway uses it

The gateway builds every vote's payload with `Payload` over its session's live records. It takes `FinishTx` of the Finish the attempt's reply carried, kept on its miss proof, for a miss claim that carries a proof, asks `ClaimsMiss` about it, and posts a vote in halves (the wait for the deadline, then `VoteTimeout`), so a vote whose deadline is still ahead is deferred.

## What it does not own

When a miss claim is attempted: whether the payload proves the error is `streamevidence.ProvesErrorMiss`, which the gateway applies before it gets here. Whether a nonce with no live record is voted at all, which the session decides. Deadlines and the wait before a vote.

## Imports

`devshard/host` and `devshard/types`.
