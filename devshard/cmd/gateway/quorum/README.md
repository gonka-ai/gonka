# `quorum` — which timeout votes can pass

A timeout is applied only when the accept weight of the voters is strictly above the escrow's vote threshold, and every slot of the executor's validator address is left out of the voters (`user/session.go`, the timeout vote collection; `state/machine.go`, `applyTimeout`). A nonce whose executor address holds `groupSize − threshold` slots or more therefore has fewer voters than it needs: no round of votes can apply its timeout, and the record is charged in full at settlement whatever the gateway does.

`Voteable` is that arithmetic and nothing else. It counts by validator address, the key the voter dedup uses, not by participant label, and it reads the escrow's own group and threshold, so it is recomputed per escrow and never cached per host.

The engine reports such a nonce as `skipped` with the reason `unvoteable` instead of posting its vote, and the registry's sweep skips it and counts it. Neither drops a vote that could pass.
