# Accounting findings

What each finding code means, what it points at, and what to check. The API carries only the code, the severity and the two numbers it was flagged on — `part` and `whole` — so an explanation is written once here instead of crossing the network with every response. `whole` is absent when the finding counts rather than measures a rate.

A finding is never raised below **20** nonces in its denominator: a rate off four attempts describes noise, not a host.

## What the host answers for

| code | rate of | warning | critical |
|---|---|---|---|
| `execution_timeouts` | nonces acknowledged and never finished, over nonces that reached the host | 1% | 5% |
| `refusals` | nonces never acknowledged, over nonces that reached the host | 5% | 20% |
| `answers_unused` | finished answers nobody used, over answers delivered | 20% | — |
| `slow_receipts` | acknowledgements slower than 2.5s, over answers delivered plus unfinished | 5% | — |
| `slow_chunks` | answers that stalled mid-stream longer than 5s, over answers delivered | 5% | — |
| `clock_drift` | receipts stamped more than 5s from this gateway's clock | 1% | — |
| `slow_decode` | answers written at more than 40 ms per output token, over answers delivered | 10% | — |
| `logprobs_not_token_ids` | answers naming logprob tokens by text instead of by id, over answers delivered | 0.1% | 1% |

**`execution_timeouts`** — the host accepted the work and delivered nothing, so the nonce is held to the execution deadline instead of freed at the refusal one. Check the requested output length against the host's decode rate.

**`refusals`** — the host did not take the work at all. Cheaper than a timeout but it still spends the nonce, and it points at capacity or reachability rather than at speed.

**`answers_unused`** — the host finished after another had already answered. A throughput problem, not an availability one.

**`slow_receipts`** — the receipt is the host's first sign of life, before a single token is generated, so a slow one points at admission rather than at generation: the request waited to be picked up. The threshold sits just past the observed p99, so it stays silent unless a host is genuinely out of family.

**`slow_chunks`** — the host began answering and then went quiet between chunks. A client reads this as a hang rather than as slowness, and a long enough gap ends the attempt outright.

**`clock_drift`** — the chain measures the execution deadline from the timestamp the host signs into its own receipt, so a drifted clock moves that deadline. Running ahead makes the network wait past a deadline that has already passed; running behind gets a nonce voted timed out while the answer is still being written. Check NTP on the host.

The offset is measured against the midpoint of the send-to-receipt round trip, not against dispatch, so the host is not charged for the outbound leg. Half a second is added back because the executor stamps whole seconds downward.

**`slow_decode`** — the host answered, and answered correctly, but wrote at a fraction of its peers' rate. Measured over the window after the first content chunk, so the prompt it had to read is not charged to how fast it writes. The threshold sits in a gap the measurements leave rather than at a round number: hosts cluster at 10-25 ms per token or at 64 ms and worse, with nothing observed between, so a host past 40 ms is an outlier and not merely a large model. A participant flagged here is serving traffic at a fraction of the throughput its share of the group implies.

**`logprobs_not_token_ids`** — a validator replays the answer from the token ids in its logprobs and cannot replay text, so it votes the inference invalid and the host loses the reward. It is a serving-stack defect rather than a model one, and it costs the host on every inference it is sampled on. Expect `chain_recorded_invalid` to follow. The thresholds are the lowest here for that reason.

## What the chain says

| code | rate of | warning | critical |
|---|---|---|---|
| `chain_recorded_misses` | assigned nonces recorded as missed on chain | 1% | 5% |
| `chain_recorded_invalid` | assigned nonces invalidated on chain | 1% | 5% |
| `challenges_unresolved` | assigned nonces whose challenge has no verdict | 1% | 5% |
| `timeouts_undecided` | timeout rounds that reached no verdict | 10% | 50% |

**`chain_recorded_misses`** — the chain's own verdict, taken from settled host statistics. This is the number that costs the host its reward. It tracks `execution_timeouts` above, and also `throttled_by_gateway` and `blocked_by_capability` below: a nonce the gateway declined to send now raises a refusal timeout of its own, so a host that refuses everything is charged for it instead of disappearing from the count. Which nonce and which client request each miss came from is at `GET /api/v1/epochs/{epoch}/events`.

**`chain_recorded_invalid`** — a validator replayed the work and got a different answer. Not about speed: check the model and the runtime version the host serves, and check `logprobs_not_token_ids` first.

**`challenges_unresolved`** — a dispute with no verdict yet. Until it resolves the nonce counts as neither valid nor invalid, which is why it is flagged on the same scale as `chain_recorded_invalid`: it is an invalid that has not landed. A challenge closes only when validators vote, and a validator collects its jobs while serving a request, so a host that receives no traffic never judges anything and the disputes routed to it stay open.

**`timeouts_undecided`** — the gateway raised a timeout and the group never decided it, by failing to collect votes at all (`vote_collection_failed`) or by collecting too few to pass the threshold (`insufficient_votes`). The nonce is already spent and the round cannot be raised again, so the host keeps the miss it earned. Rounds the gateway skipped are excluded: they never went to a vote. Read this together with `chain_recorded_misses` — a host with few recorded misses and a high share here is not clean, it is unjudged. The per-verifier reason is in `timeout_outcomes` and the `verifier_*` classes beside it. Note the arithmetic that makes a high share possible at all: accept votes are weighted by slots per address and the threshold is half the group, so a participant holding half a group's slots or more cannot be outvoted by the rest of it.

## What this gateway did

| code | rate of | warning | critical |
|---|---|---|---|
| `throttled_by_gateway` | assigned nonces burned without being sent | 10% | — |
| `quarantined_by_gateway` | assigned nonces handled under quarantine | 10% | — |
| `blocked_by_capability` | assigned nonces burned because the host cannot serve at all | 1% | — |
| `failure_origins` | nonces that reached the host and produced no usable answer | any | — |

**`throttled_by_gateway`** — our decision, but it follows the host's: the window narrows after its failures and widens as they stop, so this trails the other findings rather than leading them. The nonces are not free to the host either. Every burn raises a timeout the verifiers decide, so a host that is merely busy clears itself when challenged and one that serves nothing ends up as visibly missed as one that accepts work and drops it.

**`quarantined_by_gateway`** — also ours: the host was being probed, shadowed, or held on probation, so these nonces were not served the way a healthy host's are.

**`blocked_by_capability`** — the gateway declined to send at all because the host cannot serve this shape of work: it does not support tools, its context is too small, or its build does not speak the escrow's protocol version. Unlike a throttle or an ejection this does not expire, because none of those conditions passes with time. The nonce is still spent — arithmetic decides which host a nonce lands on, and a nonce landing on a host that can serve nothing is burned. A participant sitting at a high rate here is one to take out of the group rather than to wait on. Which of the three conditions it is stands in the record's `capability` block: `protocol_version_unsupported`, `tool_choice_unsupported`, or a `context_limit`. The block is absent for a participant with nothing wrong.

These burns are charged like throttled ones, one timeout per burn.

**`failure_origins`** — how many failures reached the host at all, counting the excused ones. Which failure each was is in the `counters` array beside the finding: read `failure_origin` there, where only `host_response` is the host's, and `dispatch_phase` or `timeout_evaluation_phase` of `poc` marks a failure that is expected.

Its denominator counts excused failures and the rates above do not, which is deliberate: sharing one denominator once produced a numerator larger than its whole.

## Why a timeout gathered no votes

A round that ends without a verdict says so in its outcome — `vote_collection_failed`, `insufficient_votes`. The reason beside it says how, because the outcome alone repeats what the tally already showed.

| reason | what to do about it |
| --- | --- |
| `verifier_version_unsupported` | the verifier's build does not serve the escrow's protocol version, so it can neither work nor vote |
| `verifier_escrow_missing` | the verifier no longer holds the escrow |
| `verifier_inference_missing` | the verifier never saw the inference confirmed; its copy of the state is behind |
| `verifier_unreachable` | nothing came back at all |
| `vote_weight_short` | every verifier answered and the accepting weight stayed under the threshold |

The last one is the only shape that is not a reachability problem: the network answered and did not agree. The others say a quorum could not be assembled, which is a property of the group rather than of the host being voted on.

## Facts carried beside the findings

Three reasons in the counters name situations no rate captures, because each is a statement about one nonce rather than a rate over many.

**`model_burn_empty`** on a delivery reason marks an empty answer the model caused, not the host: it emitted completion tokens the runtime then stripped, which the reasoning route does at small `max_tokens`. It is separated from `empty_stream` because that one is a host defect and drives quarantine, while this one is a documented model outcome and must not cost the host anything. The signal is `usage.completion_tokens`, which the host reports about itself, so it is honoured only on the reasoning route — anywhere else a host could dodge the empty-stream verdict by inventing usage.

**`client_gone_before_delivery`** on a delivery reason marks a winner crowned after the client stopped waiting. The race outlives the client on purpose — its committed nonce still has to be settled, and settling needs the attempt to finish — so the host's work is real and is paid for. Only the delivery is a fiction, and without this reason the ledger would count the answer as one a client received.

**`unclassified`** is a plain number rather than a reason: the assigned nonces the ledger has not accounted for yet, `assigned - accounted` per slot of one escrow. Arithmetic decides how many nonces a slot is assigned, and each is accounted once it reaches a terminal state, so the gap is the work still in flight or still waiting on a fact that has not arrived. It falls on its own as an escrow drains and should reach zero by settlement; a share that survives settlement is a gap in this gateway's bookkeeping, not host behaviour. Its mirror image is `overclassified`, which no host behaviour produces and which `ledger_overcounted` flags at any volume.

**`escrow_gone_from_hosts`** on a timeout reason marks a vote no retry can win: the hosts no longer hold the escrow, so the nonce it would have settled is unsettleable and pays its full reserve at settlement. It is separated from a collection failure because that one is transient and this one is final. The verifier's own error never reaches the ledger — vote collection reports a count, not an error — so the fact is read from the attempt that was told the same thing.

## What needs reporting

| code | flagged on | warning | critical |
|---|---|---|---|
| `reasons_unknown` | classified nonces carrying a reason the ledger could not name | 5% | — |
| `ledger_disagrees_with_chain` | nonces the ledger and the chain disagree about | any | — |
| `ledger_overcounted` | nonces beyond what the chain assigned | any | — |

**`reasons_unknown`** — a terminal state the ledger cannot name. A round that ended without concluding — votes that never gathered, or a threshold never met — has no reason of its own, so it reads as unknown rather than repeating its own outcome. Why the votes were lost is a separate field: see the `verifier_*` reasons below. What remains here after those is a genuine gap in this gateway's instrumentation, not a host fault.

**`ledger_disagrees_with_chain`** — expected drift while an escrow is live, and it converges on its own. A gap that survives settlement means one of the two is wrong. The four numbers behind it are in `cross_checks`: applied timeouts against chain misses, recorded invalid against chain invalid.

**`ledger_overcounted`** — more nonces than the chain ever assigned to the slot. No host behaviour produces this, so it is a broken invariant and is flagged at any volume. Report it.
