# Accounting findings

What each finding code means, what it points at, and what to check. The API carries only the code, the severity and the two numbers it was flagged on — `part` and `whole` — so an explanation is written once here instead of crossing the network with every response. `whole` is absent when the finding counts rather than measures a rate.

A finding is never raised below **20** nonces in its denominator: a rate off four attempts describes noise, not a host.

## What the host answers for

| code | rate of | warning | critical |
|---|---|---|---|
| `execution_timeouts` | nonces acknowledged and never finished, over nonces that reached the host | 1% | 5% |
| `refusals` | nonces never acknowledged, over nonces that reached the host | 5% | 20% |
| `answers_unused` | finished answers nobody used, over answers delivered | 20% | — |
| `slow_receipts` | acknowledgements slower than 5s, over answers delivered plus unfinished | 5% | — |
| `slow_chunks` | answers that stalled mid-stream longer than 1.5s, over answers delivered | 5% | — |
| `clock_drift` | receipts stamped more than 5s from this gateway's clock | 1% | — |
| `logprobs_not_token_ids` | answers naming logprob tokens by text instead of by id, over answers delivered | 0.1% | 1% |

**`execution_timeouts`** — the host accepted the work and delivered nothing, so the nonce is held to the execution deadline instead of freed at the refusal one. Check the requested output length against the host's decode rate.

**`refusals`** — the host did not take the work at all. Cheaper than a timeout but it still spends the nonce, and it points at capacity or reachability rather than at speed.

**`answers_unused`** — the host finished after another had already answered. A throughput problem, not an availability one.

**`slow_receipts`** — the receipt is the host's first sign of life, before a single token is generated, so a slow one points at admission rather than at generation: the request waited to be picked up.

**`slow_chunks`** — the host began answering and then went quiet between chunks. A client reads this as a hang rather than as slowness, and a long enough gap ends the attempt outright.

**`clock_drift`** — the chain measures the execution deadline from the timestamp the host signs into its own receipt, so a drifted clock moves that deadline. Running ahead makes the network wait past a deadline that has already passed; running behind gets a nonce voted timed out while the answer is still being written. Check NTP on the host.

The offset is measured against the midpoint of the send-to-receipt round trip, not against dispatch, so the host is not charged for the outbound leg. Half a second is added back because the executor stamps whole seconds downward.

**`logprobs_not_token_ids`** — a validator replays the answer from the token ids in its logprobs and cannot replay text, so it votes the inference invalid and the host loses the reward. It is a serving-stack defect rather than a model one, and it costs the host on every inference it is sampled on. Expect `chain_recorded_invalid` to follow. The thresholds are the lowest here for that reason.

## What the chain says

| code | rate of | warning | critical |
|---|---|---|---|
| `chain_recorded_misses` | assigned nonces recorded as missed on chain | 1% | 5% |
| `chain_recorded_invalid` | assigned nonces invalidated on chain | 1% | 5% |
| `challenges_unresolved` | count of challenges with no verdict | any | — |

**`chain_recorded_misses`** — the chain's own verdict, taken from settled host statistics. This is the number that costs the host its reward, and it should track `execution_timeouts` above.

**`chain_recorded_invalid`** — a validator replayed the work and got a different answer. Not about speed: check the model and the runtime version the host serves, and check `logprobs_not_token_ids` first.

**`challenges_unresolved`** — a dispute with no verdict yet. Until it resolves the nonce counts as neither valid nor invalid.

## What this gateway did

| code | rate of | warning | critical |
|---|---|---|---|
| `throttled_by_gateway` | assigned nonces burned without being sent | 10% | — |
| `quarantined_by_gateway` | assigned nonces handled under quarantine | 10% | — |
| `failure_origins` | nonces that reached the host and produced no usable answer | any | — |

**`throttled_by_gateway`** — our decision, not the host's failure. The per-host window narrows after failures and widens as they stop, so this trails the other findings rather than leading them.

**`quarantined_by_gateway`** — also ours: the host was being probed, shadowed, or held on probation, so these nonces were not served the way a healthy host's are.

**`failure_origins`** — how many failures reached the host at all, counting the excused ones. Which failure each was is in the `counters` array beside the finding: read `failure_origin` there, where only `host_response` is the host's, and `dispatch_phase` or `timeout_evaluation_phase` of `poc` marks a failure that is expected.

Its denominator counts excused failures and the rates above do not, which is deliberate: sharing one denominator once produced a numerator larger than its whole.

## What needs reporting

| code | flagged on | warning | critical |
|---|---|---|---|
| `reasons_unknown` | classified nonces carrying a reason the ledger could not name | 5% | — |
| `ledger_disagrees_with_chain` | nonces the ledger and the chain disagree about | any | — |
| `ledger_overcounted` | nonces beyond what the chain assigned | any | — |

**`reasons_unknown`** — a gap in this gateway's instrumentation, not a host fault: a terminal state reached through a path the ledger cannot name.

**`ledger_disagrees_with_chain`** — expected drift while an escrow is live, and it converges on its own. A gap that survives settlement means one of the two is wrong. The four numbers behind it are in `cross_checks`: applied timeouts against chain misses, recorded invalid against chain invalid.

**`ledger_overcounted`** — more nonces than the chain ever assigned to the slot. No host behaviour produces this, so it is a broken invariant and is flagged at any volume. Report it.
