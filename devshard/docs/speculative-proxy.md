# Speculative Proxy Logic

This note describes how `devshardctl` runs speculative inference in the devshard proxy.

The goal is simple:

- start with one host
- if that host looks unhealthy or too slow, add another host for the same user request
- keep doing that until one attempt wins or the proxy runs out of allowed attempts

This is the logic implemented in `devshard/cmd/devshardctl/speculative.go`.

## Why speculative execution exists

Devshard hosts can fail in several different ways:

- the host never responds
- the host sends a receipt but never produces tokens
- the host is alive but much slower than another host
- the host fails immediately at send time

Without speculation, one bad host can hold the whole user request hostage. The speculative runner reduces tail latency by allowing another host to race the original attempt.

## Core model

Each user request is represented by a set of in-flight attempts.

Each attempt has:

- a nonce
- a target host index
- send time
- optional receipt time
- optional first-token time
- final response or error

The proxy starts with one primary attempt, then may add more attempts over time.

Important detail: the proxy does not manually choose "host 2", "host 3", and so on. Instead, every call to `PrepareInference()` advances the session nonce, and the nonce naturally maps to the next host in the devshard group. That means repeated preparation automatically walks through the host set.

## High-level flow

For each request:

1. Prepare the primary inference.
2. Decide whether to start an extra attempt immediately or wait.
3. Start the primary attempt.
4. Keep watching all active attempts.
5. If no winner exists and one attempt crosses a fallback threshold, start one more attempt.
6. The first attempt that produces output becomes the winner for streaming.
7. When all attempts are resolved, process the winner and clean up failed attempts with timeout handling.

This is a progressive fanout strategy, not a broadcast-to-all-hosts strategy.

## Initial decision

Before the primary starts, the engine makes one initial decision:

- `primary_unresponsive`: if the primary host has a bad responsiveness history, start another attempt immediately
- `secondary_faster`: if the next host looks much faster from recorded performance samples, start another attempt immediately
- `receipt_timeout`: otherwise start only the primary and wait for a receipt timeout before escalating

This decision only controls the initial shape of the race. After that, the runtime escalation loop takes over.

## Escalation triggers

After the request starts, the engine keeps scanning all active attempts and asks:

"Does any current attempt justify adding one more host?"

An attempt can trigger escalation for four reasons.

### 1. Receipt timeout

If an attempt has not produced a receipt by `ReceiptTimeout`, it is treated as stuck and the proxy adds another attempt.

Use case:

- host is unreachable
- host accepts the request very late
- host stalls before confirming start

### 2. First-token timeout

If an attempt has produced a receipt but is streaming and does not produce the first token soon enough, the proxy adds another attempt.

The timeout is:

- `max(FirstTokenTimeoutCap, input_tokens * PerInputTokenFirstTokenLag)`

This scales the waiting time with prompt size while still enforcing a minimum threshold.

### 3. Non-streaming response timeout

If the request is non-streaming and the attempt does not fully finish soon enough after send time, the proxy adds another attempt.

The timeout is:

- `max(NonStreamResponseFloor, input_tokens * PerInputTokenResponseLag)`

This is the non-streaming equivalent of the first-token fallback.

### 4. Immediate attempt failure

If an attempt finishes with an error before succeeding, the proxy does not wait for the old timeout window. It can immediately escalate to the next host.

This is what makes the "first host dead, second host dead, third host wins" case work.

## What changed in the universal version

The earlier version only handled:

- primary
- optional one secondary

That meant:

- if the primary was dead, the proxy could try one more host
- if that second host was also dead or also too slow, the request still failed

The current version treats the request as a list of attempts instead of a fixed pair.

So now:

- a slow primary can trigger a secondary
- a slow secondary can trigger a tertiary
- a dead primary can trigger a secondary immediately
- a dead secondary can trigger a tertiary immediately

This makes the fallback logic universal across the whole devshard group, subject to the configured attempt limit.

## Winner selection

For streaming requests, the winner is the first attempt that produces output chunks. Once a winner is selected:

- only the winner's stream is forwarded to the client
- loser streams are suppressed
- the proxy may still keep waiting briefly for the other attempts to finish so it can process or time them out cleanly

For non-streaming requests, a finished successful attempt is also treated as the winner even if no stream chunk was observed.

## Attempt limit

The runner does not have to use every host forever.

`MaxSpeculativeAttempts` controls the upper bound:

- `0` means "allow up to the full group size"
- any positive number caps the total attempts for one user request

This is important because every extra attempt is real devshard work and may later require timeout cleanup.

## Cleanup and timeout handling

When one attempt succeeds and others fail, the request still succeeds for the user. The losers are cleaned up separately:

- finished successful attempts are processed into session state
- failed attempts go through timeout vote collection and timeout diff submission

If no attempt succeeds, the whole request fails.

## Answer to the "slow first token" question

Yes: the new logic changes that path too.

Previously:

- primary is slow to first token
- proxy starts one secondary
- if the secondary is also slow, the logic stops there

Now:

- primary is slow to first token
- proxy starts a secondary
- if the secondary is also slow to first token, it can trigger a tertiary
- this continues until some attempt wins or the attempt limit is reached

The same principle applies to:

- slow receipt
- slow non-streaming completion
- immediate host failure

## Regression coverage

The codebase includes a regression test for the multi-dead-host case:

- `TestRunInference_SpeculativeFallsThroughMultipleDeadHosts`

That test kills the first two routed hosts and verifies that the third attempt wins.

## Proposed improvement: pairwise A/B routing signal

The current `secondary_faster` decision compares hosts using the rolling
`PerfTracker` estimate:

- average receipt time
- plus average combined time-to-first-token lag (`cTTFL`) scaled by input tokens

That estimate is useful for hosts that fail before receipt or before first
token, but it is weaker when comparing samples with different prompt sizes and
it does not model post-first-token generation speed. A host can therefore look
healthy, win by producing the first token early, and still make the user wait
because the remaining chunks arrive slowly.

To make the decision more reliable, add a low-rate pairwise A/B sampler.

### Sampling strategy

For a small fraction of real user requests, start two comparable attempts at
the same time:

1. Prepare the current nonce as usual.
2. Prepare the next eligible nonce as the A/B opponent.
3. If the immediate next nonce is a no-send probe or otherwise unavailable,
   skip it and compare against the next real host (`n+2`, `n+3`, etc.).
4. Start both attempts immediately.
5. Forward only the normal winner to the client, but record both attempts'
   timing data.

The sampler must be budgeted separately from normal failover so it cannot
explode inference cost. Start with:

- maximum A/B overhead: `5%` additional attempts over the rolling window
- adaptive sparse-pair overhead: allow up to `20%` additional attempts for
  requests whose current eligible pair has fewer than `3` valid comparisons
- budget percentile cutoff: configurable, default `p90` / top `10%` of
  predicted marginal speedups
- maximum proactive fanout per request: configurable, default `3` additional
  attempts
- no A/B sampling when the request already needs emergency fanout
- no A/B sampling for admin/debug requests
- no A/B sampling when the request would exceed the configured speculative
  attempt cap

The budget is counted in attempts, not requests. For example, with `10,000`
normal attempts in the window, the sampler may add at most `500` extra A/B
attempts under the normal budget. The `20%` sparse-pair budget is a temporary
data-collection accelerator: once the pair has at least `3` valid comparisons,
it falls back to the normal `5%` sampling probability and budget.

Sampling is evaluated from the latest attempt the router has already decided to
start, not always from the original primary. If the router does not start `B`
for speed, the sampler may add `B` to collect `A/B`. If the router already
starts `B`, the sampler may add `C` to collect `B/C`. If the router already
starts `B` and `C`, the sampler may add `D` to collect `C/D`, subject to the
same probability and proactive fanout cap.

### What to record

Store comparisons by participant pair, not by slot number. Slot positions can
move across escrows and rotations; participant identity is the stable unit of
performance.

The comparison store is global within the gateway process and persisted by
participant identity:

- storage key: `(model_id, participant_a, participant_b, request_shape_bucket)`
- not keyed by escrow, runway, or slot index

Routing decisions are still made per runway. At request time, the gateway maps
the current nonce order to participants (`A`, `B`, `C`, ...), looks up the
global pair data for those participants, and applies the ordered-chain decision
to the current runway.

For each ordered pair `(primary_participant, opponent_participant)`, keep the
last `10` direct comparisons. Each comparison should record:

- model id
- input token count
- output token or content chunk count
- primary receipt time
- opponent receipt time
- primary first-token time
- opponent first-token time
- primary total attempt time
- opponent total attempt time
- primary finalized time
- opponent finalized time
- winner by first token
- winner by finalized attempt duration
- whether either attempt failed, was a probe, was suppressed, or was cancelled
- failure class for each side, when present
- failure time for each side, when present

The primary routing signal should be total attempt duration from the moment the
attempt is sent to the host until the attempt is finalized. Do not score only
post-first-token generation time. Generation time remains useful diagnostic
data, but routing should optimize the user-visible end-to-end attempt cost.

When normal speculative fanout starts additional attempts, those attempts can
also contribute to the A/B comparison store if they are comparable:

- same user request
- same model
- both attempts were real sends, not probes
- both attempts reached a terminal state that can be scored or classified

This lets the system learn from both deliberate A/B exploration and production
fanout that happened for routing reasons.

For scoring, compare normalized deltas instead of raw times:

- first-token delta: `opponent_first_token_ms - primary_first_token_ms`
- total-attempt delta: `opponent_total_attempt_ms - primary_total_attempt_ms`
- generation delta: `(opponent_total_ms - opponent_first_token_ms) -
  (primary_total_ms - primary_first_token_ms)`

The total-attempt delta is the key routing addition. It captures hosts that are
quick to start but slow to finish, and hosts that are simply slow end to end.

### Pairwise score

For each pair, average the last `10` comparisons and derive:

- `primary_faster_first_token_rate`
- `primary_faster_total_attempt_rate`
- `avg_first_token_delta_ms`
- `avg_total_attempt_delta_ms`
- `avg_generation_delta_ms`
- `avg_total_attempt_ratio`

Use only comparisons with similar request shape. A comparison is valid for
ranking when:

- both attempts used the same model
- input token counts are within a small tolerance, for example `±10%`
- output chunk/token counts are within a small tolerance, or both reached the
  request's configured max output

This avoids drawing conclusions from comparing a tiny prompt against a large
prompt or a short answer against a long answer.

### Failed-side comparisons

A/B samples should still be recorded when one or both sides fail, but they
should not all enter the total-duration ranking in the same way.

Classify each side's result:

- `success`: finalized successfully with usable output
- `application_error`: model returned a valid OpenAI-style error response
- `empty_stream`: receipt arrived but no usable content was produced
- `transport_failure`: send failed, connection failed, or HTTP inference route
  failed
- `timeout_or_stall`: receipt/token/chunk progress timed out
- `cancelled_or_suppressed`: attempt was stopped because another attempt won
  before enough timing data was available
- `probe_or_skipped`: no real inference was sent

Scoring rules:

- `success` vs `success`: use total attempt duration.
- `success` vs `transport_failure`, `empty_stream`, or `timeout_or_stall`:
  score the failed side as a strong loss and record the failure class.
- `success` vs `application_error`: record the comparison, but do not use it
  for slow-speed ranking unless the application error is known to be host-caused.
  Client/model validation errors should not make a host look slow.
- `failure` vs `failure`: record for reliability stats, but exclude from
  speed ranking unless the failure classes clearly distinguish one side.
- `cancelled_or_suppressed`: use only the partial timing fields that are known
  (for example receipt or first-token), and exclude from total-duration ranking
  unless the attempt reached a terminal success/failure state.
- `probe_or_skipped`: do not count as an A/B comparison.

Keep separate scores for speed and reliability:

- speed score: based on successful total attempt duration
- reliability score: based on failure rate and failure severity

Routing can combine them as a penalty-adjusted expected time:

`expected_cost_ms = expected_success_total_ms + failure_penalty_ms * failure_probability`

This prevents a fast-but-flaky host from ranking as good just because its few
successful responses are quick.

### Slow-host ranking

Pairwise comparisons can be non-transitive:

- `A` can beat `B`
- `B` can beat `C`
- `C` can beat `A`

This is expected when load, prompt shape, and answer shape vary. Do not build
the top-slowest list by applying pair winners as hard ordering constraints.
Instead, treat comparisons as a weighted graph and derive a global score.

For each valid comparison between participants `A` and `B`:

1. Compute total attempt durations from send to finalized attempt:
   `a_total_ms` and `b_total_ms`.
2. Compute a bounded log-ratio margin:
   `margin = clamp(log(a_total_ms / b_total_ms), -max_margin, max_margin)`.
   Positive means `A` was slower; negative means `B` was slower.
3. Weight the comparison by confidence:
   - `0.33` when the pair has `1` valid comparison
   - `0.66` when the pair has `2` valid comparisons
   - `1.0` when the pair has `3+` valid comparisons
4. Add `+margin * weight` to `A`'s slow score.
5. Add `-margin * weight` to `B`'s slow score.

Then build a process-wide participant ranking from pairwise results:

1. For each participant, aggregate all recent pairwise comparisons.
2. Divide each participant's accumulated slow score by total comparison weight
   so heavily sampled hosts do not dominate only because they have more edges.
3. Sort participants from highest slow score to lowest slow score.
4. Mark the top `10` slowest participants as `slow-ranked`.

This is effectively a lightweight Bradley-Terry/Elo-style ranking without a
hard dependency on a solver. It handles cycles because each comparison nudges a
continuous score instead of forcing a strict pairwise order.

When two hosts have close scores, prefer the one with more direct evidence as
the ranking candidate. If both score and confidence are close, keep the previous
ranking for stability instead of flapping.

This ranking should be soft state. It should not quarantine hosts and should
age out automatically as newer comparisons arrive.

### Routing behavior

Use budgeted expected speedup before the current `secondary_faster` estimate:

1. Prepare the primary host.
2. Estimate the primary's penalty-adjusted total attempt cost for the request
   shape.
3. Walk eligible non-probe hosts in nonce order.
4. Evaluate candidates as an ordered chain, not as independent pair edges.
5. Start the candidate immediately if its accepted-chain speedup is above the
   configured percentile cutoff, default `p90` / top `10%` of recent simulated
   opportunities.
6. Continue while candidates clear the cutoff, but cap this proactive fanout at
   the configured maximum, default `3` additional attempts.
7. Keep the existing receipt-timeout, first-token-timeout, non-streaming
   timeout, and immediate-failure escalation paths as fallback.

The ordered-chain rule matters when the next-after host is much better than the
next host. Suppose the nonce order is `A`, `B`, `C`:

- `A -> B` is the speedup from adding `B` to a request that would otherwise
  only run `A`.
- `B -> C` is only relevant after `B` has already been accepted. Do not sort
  `B -> C` as a standalone opportunity when the request is still only running
  `A`; that would distort the budget because it pretends `C` can be reached
  without paying for `B`.
- If `A -> B` clears the cutoff, start `B`, then evaluate the marginal speedup
  of adding `C` against the best expected cost among `{A, B}`.
- If `A -> B` does not clear the cutoff, evaluate the bundled jump `A -> C`.
  Estimate `A -> C` from direct pair data when there are enough direct samples,
  otherwise infer it by chaining adjacent pair ratios (`A/B * B/C`) with reduced
  confidence. If bundled `A -> C` clears the cutoff, start both `B` and `C`
  immediately because reaching `C` consumes both nonce steps.

In other words, opportunities are budgeted by executable actions:

- single extra attempt: `{A} -> {A, B}`
- second extra attempt after accepted first: `{A, B} -> {A, B, C}`
- bundled two-attempt jump: `{A} -> {A, B, C}`

Each opportunity's score should be normalized by the number of extra attempts
it consumes, so a two-attempt bundle must deliver enough expected speedup to
justify spending two units of the budget.

### Direct vs chained pair estimates

Store non-adjacent comparisons too. If the gateway ever runs `A`, `B`, and `C`
for the same request, it should record:

- `A/B`
- `B/C`
- `A/C`

When estimating `A -> C`, prefer direct `A/C` evidence when the pair has enough
valid samples. Start with:

- use direct `A/C` if there are `>3` valid `A/C` comparisons
- otherwise infer `A/C` from chained pair ratios

For chained estimates, compose ratios in total-attempt-cost space:

`ratio(A,C) = ratio(A,B) * ratio(B,C)`

where:

`ratio(X,Y) = expected_cost(X) / expected_cost(Y)`

Because chained estimates accumulate error, reduce confidence for each hop. For
example:

- direct pair with `3+` samples: confidence `1.0`
- two-hop chain: `min(conf(A/B), conf(B/C)) * 0.75`
- three-hop chain: `min(edge_confidences) * 0.5`

Do not use long chains for routing unless there is no better evidence; they are
mainly useful to seed exploration.

The percentile cutoff is recalculated over a rolling window. For example, if
the budget allows `10%` additional attempts, simulate all recent candidate
opportunities, sort them by marginal expected speedup, and set the cutoff so
only the top `10%` would have been accepted. At runtime, start any candidate
whose marginal speedup is at or above that cutoff.

This changes slow-host handling from "wait until the primary misses receipt or
first-token thresholds" to "preemptively spend bounded extra attempts only where
recent data says they buy the most expected speedup."

### Probe and quarantine interaction

A/B opponents must be real inference candidates. Do not count skipped probes as
pairwise comparisons:

- quarantined host ghost probes
- session-picker no-send probes
- hosts excluded by request-local attempt history
- hosts unavailable for the requested model

If `n+1` is a probe, compare `n` with `n+2` or the next eligible real host.
If no eligible host exists inside the attempt cap, skip A/B sampling for that
request.

### Why pairwise comparison is better than global cTTFL

Pairwise sampling controls for request shape because both hosts receive the
same prompt at the same time. This removes most of the noise from:

- different input token counts
- different answer lengths
- changing external load during the test window
- escrow rotation and slot movement

The result is a more trustworthy answer to: "For this kind of request, is host
A actually faster than host B?"

The existing `PerfTracker` can remain useful for hard failure and pre-token
signals, but the pairwise score should drive slow-finisher avoidance.

### Relationship to the current mechanism

This mechanism should replace the current `secondary_faster` decision as the
primary speed-based fanout policy once enough pairwise data exists.

Keep the existing logic as fallback:

- `primary_unresponsive` remains useful for hosts with bad responsiveness
  history.
- `receipt_timeout`, first-token timeout, response timeout, and immediate
  failure escalation remain runtime safety nets.
- The current `PerfTracker` estimate can be used when a pair has no direct or
  chained evidence yet, but it should have lower confidence than A/B pair data.

Decision precedence:

1. Hard block/quarantine and request-local exclusions.
2. Existing `primary_unresponsive` fallback.
3. Probation routing for recently unquarantined hosts.
4. Pairwise budgeted expected-speedup fanout.
5. Existing timeout/failure escalation loop.

### Rollout and legacy fallback

Gate the speed-based fanout policy behind `redundancy.speed_policy`:

- `legacy`: use the existing `secondary_faster` estimate only.
- `pairwise`: use only pairwise budgeted expected-speedup fanout for immediate
  speed decisions. If there is not enough pairwise data, fall back to timeout
  escalation rather than `secondary_faster`.
- `hybrid`: use pairwise first, then the old `secondary_faster` path while
  pairwise data warms up.

Keep the `secondary_faster` code active but mark it as deprecated fallback in
the code. Do not comment it out; the gateway needs a rollback path while
pairwise data is sparse. Once `pairwise` mode has been stable for a release and
debug data shows the fallback is no longer needed, delete the legacy path.

Expose the rollout knobs in admin settings:

- `redundancy.pairwise_budget_percentile`: percentile cutoff for consuming the
  extra-attempt budget, default `0.90`.
- `redundancy.pairwise_max_proactive_attempts`: max immediate extra attempts,
  default `3`.
- `redundancy.pairwise_min_direct_comparisons`: direct pair samples required
  before direct evidence beats chained estimates, default `4`.
- `redundancy.pairwise_winner_hold_ms`: maximum time to buffer an earlier
  weaker first content chunk while a directly proven better speculative attempt
  is already running, default `500`.
- `redundancy.pairwise_winner_hold_min_speedup`: minimum direct expected
  completion speedup required before the hold can apply, default `0.10`.
- `redundancy.pairwise_winner_hold_min_samples`: minimum direct samples for the
  hold decision, default `6`.

### Pairwise winner hold

`pairwise_budgeted_speedup` may start a better host immediately, but the old
streaming rule still crowned whichever host produced content first. Because
first-token latency is not strongly correlated with full completion latency, the
gateway should allow a short bounded hold when the pairwise data is confident.

The hold applies only when all of these are true:

- the extra attempt was started by `pairwise_budgeted_speedup`
- the candidate has direct pairwise evidence over the attempt that triggered it
- direct samples are at least `pairwise_winner_hold_min_samples`
- expected completion speedup is at least
  `pairwise_winner_hold_min_speedup`
- `pairwise_winner_hold_ms` is greater than zero

When an earlier/weaker attempt emits the first content chunk, the gateway buffers
that chunk for at most `pairwise_winner_hold_ms`. If the preferred attempt emits
content during the hold, it becomes the winner and the early chunk is discarded.
If the preferred attempt does not produce content before the hold expires, the
early attempt wins and streams normally. The hold is never used for legacy
`secondary_faster` or timeout-only fanout.

### Debug access

Expose pairwise state through admin/debug endpoints so operators can see why a
host was routed around:

- `GET /v1/debug/pairwise`
  - list all stored pair summaries
  - filter by `model_id`, `participant`, `request_shape_bucket`
- `GET /v1/debug/pairwise/{participant_a}/{participant_b}`
  - show the last `10` direct comparisons for that ordered pair
  - show direct averages and confidence
  - show chained estimates used when direct evidence is sparse
- `GET /v1/debug/pairwise/routing`
  - show the current dynamic percentile cutoff
  - show recent simulated opportunities
  - show which opportunities would consume the attempt budget
  - show winner-hold thresholds and recent hold decisions

Each pair summary should include:

- sample count
- last updated time
- request shape bucket
- average total attempt duration for each side
- average first-token duration for each side
- average generation duration for each side
- success/failure counts by failure class
- penalty-adjusted expected cost
- direct confidence
- chained confidence, when applicable
- last `10` raw comparison records

The debug output should be per model, because host performance can differ by
model and request shape.
