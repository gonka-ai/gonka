# Observability

Reference for what the gateway reports about itself: log lines, Prometheus metrics, traces and the admin routes that read its state. Use it to find the signal that answers a question; what to do about a symptom is in the [runbook](./runbook.md), "When something is wrong".

## Logs

The gateway writes a line for every event that **moves money, changes what it will serve, or is an operator's own doing** — and for very little else. Failures on the money path are not logged separately: each is returned as an error naming its own step (`resolving signer for escrow X`, `building settlement for escrow X`) and the escrow tick logs the joined result once as `escrow tick failed`. A busy or in-flight settle is deferred to a later tick, not a failure, and is not part of it. A success has no such carrier, which is why the successful transitions are the ones written down.

Lines are JSON objects by default. Promtail lifts `level` into a Loki label (`deploy/join/observability/promtail-config.yaml`), and the Loki panels parse the rest with `| json`, so a gateway switched to `DEVSHARD_LOG_FORMAT=text` empties those panels.

Every lifecycle line is written by the journal (`journal/`) in the order its steps happened. A file named beside a line below is the step's producer, unless it names a `journal/render_*.go` renderer or says "written by". A test fails on a lifecycle line written around the journal and on a key the log vocabulary does not declare (`journal/guard_test.go`, `journal/keys_test.go`).

### The trace

Always on, with no level knob — a trace that ships off by default is not there for the incident that already happened, and switching it on afterwards cannot recover what was not written. Roughly five lines per request.

| Line | Carries |
| --- | --- |
| `nonce committed` (emitted by `engine/pick.go`, written by `journal/render_race.go`) | request, escrow, nonce, participant, slot, role, why this attempt started, and the two sizes its reserve is made of: `input_bytes` (the body length the host checks as `input_length`) and `max_tokens`; the chain reserves `(input_bytes + max_tokens) × token_price + fee_per_nonce` for the nonce |
| `attempt finished` (emitted by `engine/report.go`, written by `journal/render_race.go`) | the same identity, the terminal verdict, the host's `usage_tokens` and `usage_prompt_tokens` as its stream reported them (what the chain charges is `(prompt + completion) × token_price`, never more than the reserve), whether the nonce was finished, whether the host diverged on state, and each deadline its host was narrowed for (`missed_receipt_deadline`, `missed_first_token_deadline`) |
| `served winner finish landed` (emitted by `engine/winner_sample.go`, written by `journal/render_race.go`) | request, escrow, nonce and participant of a winner whose nonce was still open when its stream ended, once its Finish is seen, with `finish_lag_ms` from the attempt's end ([race.md](./race.md), "A served winner's sample waits for its finish") |
| `nonce stranded` (emitted by `engine/race.go`, written by `journal/render_race.go`) | **Warn** — a committed nonce nobody will answer for; the shape every recurring settlement defect takes |

The journal's consumer writes these lines, so under load, or while the nonce ledger copies itself for a snapshot, it can drop `nonce committed`, `attempt finished` and `served winner finish landed` — counted in `devshard_gateway_journal_progress_dropped_total` and announced by `journal skipped progress lines` — while `nonce stranded` is refused only past the money ceiling.

A line the journal writes is stamped when the journal's consumer writes it, not when the event happened, so it can trail — for example, while the consumer waits for the nonce ledger's lock as `Book.Snapshot` copies the ledger. Journal lines keep their order among themselves. Lines written directly — the shared `devshard/user` session lines and the gateway's process, admin and store lines — are stamped when they happen, so the two can interleave out of order. `duration_ms` and the other `*_ms` fields are measured at the event, and are the timings to trust.

Follow one request by grepping its request id; follow one nonce through commit, dispatch and verdict by grepping the nonce.

`attempt finished` carries the coordinator's reading of the attempt at the moment it completed: `racedTerminal` (`engine/report.go`) makes a backstopped attempt read `hard_timeout`, a stalled one `stalled`, an answer its host closed with neither `[DONE]` nor a finished nonce — a loser's as well as the crowned one's — `sse_truncated`, and the winner `won`. Only a later `abandonedByHosts` reclassification, made once the whole race has finished, reaches the ledger without reaching this line.

### When a host stops taking work

Three mechanisms withhold work from a host, each on its own trigger, and each is a gauge in Prometheus. A gauge is sampled every 15 or 30 seconds while the first rung of two of them lasts 30 seconds and 5 seconds, so the shortest withholdings pass entirely between two scrapes. Each producer therefore also narrates each edge through the journal, which writes one line for it, and nothing in between: the volume follows the number of hosts and their own windows, never the request rate.

| Line | Trigger | Carries |
| --- | --- | --- |
| `host withheld from routing` (`perf/tracker.go`) | five failures in a row, or a failure rate from 15% over a volume from 20 | **Warn** — which trigger fired, the rung, the run length, the rate and its volume that tripped it, and how long the withholding lasts |
| `host back in routing` (`perf/tracker.go`) | first sample after the withholding lapsed, or the epoch boundary cleared it | the rung it decayed to |
| `host cut off after transport faults` (`limits/reaction.go`) | three cut-off faults in a row — a transport fault, an empty answer that left its nonce open, or a `429`/`503` from a host that carried nothing else of ours for this model when it refused — or one failed half-open probe | **Warn** — which of the two, the backoff depth, and how long the cut-off lasts. The line's name and its `consecutive_transport_faults` reason are what dashboards and log queries match on, so both keep the wording they have whatever the run was made of |
| `host back after its cut-off` (`limits/reaction.go`) | the probe answered, or the epoch boundary closed the cut-off | the backoff depth it decayed to |
| `host stats reset at epoch` (`app/epoch_reset.go`) | the effective epoch rose | the epoch, the pairs the windows held, how many hosts were withheld and how many cut off |
| `host denied the crown` (`engine/crown_strikes.go`) | three content-free answers in a row | **Warn** — the strike count. The host keeps drawing nonces and starts a second attempt beside itself, so this is a spend, not only a quality signal |
| `host crowned again` (`engine/crown_strikes.go`) | one answer with content | — |

One more line belongs to the same family, on the money side rather than the routing one: `execution timeouts swept` (`escrow/manager.go`), written by the escrow tick only when the sweep found nonces to re-vote, carrying how many were due, applied and failed. Silence means nothing was owed.

### The request record

`request finished` (`journal/render_request.go`), one line per completed race: Info when it went out clean, Warn when it did not. It answers what a finished request can no longer be asked:

| Field | What it settles |
| --- | --- |
| `api_key_id` | the client API key the request was made with: the first eight hex characters of the key's SHA-256 (`printf %s "$KEY" \| sha256sum \| cut -c1-8`), `admin` for the admin key, `none` when no configured client key matched (no key or an unknown one). The limiter's refusal line carries it too, so one key's share of the traffic and of the refusals can be read from the log |
| `escrow` | the escrow the race ran on; absent when no escrow was picked |
| `input_tokens` / `input_tokens_source` | the gateway's own prompt count and where it came from (`tokenizer_exact`, `tokenizer_sampled` or `bytes_estimate`), so a log answers whether the tokenizer counted a request |
| `host` / `hosts` | `host` is the crowned attempt's host; with nobody crowned, `hosts` lists every host tried, comma-separated; a race that ran no attempt carries neither |
| `bytes` | how much actually reached the client, counted at the socket — the strip rewrites events on the way out |
| `terminated` | whether the SSE terminator went with them; without it a client waits out its own timeout on a reply it already has |
| `outcome` | `served`, `failed_mid_stream`, or `failed_before_first_byte` — the last distinguishes a reply the client can retry from one it cannot; `served` with an `error` field is a host error the gateway answered in its own words |
| `deliver_error` | the failure that reached the client instead of the last bytes: the case no status code can express, because a stream commits 200 on its first byte; a write-deadline (`i/o timeout`) error here means the client stopped reading for `clientWriteTimeout`, or read the reply slower than `GATEWAY_MIN_CLIENT_READ_BYTES_PER_SECOND` allows (`api/README.md`, "Streaming the reply") |
| `nonce_finished` | whether the crowned attempt's nonce closed on the chain. A served request with `false` here answered the client and left the escrow owing a vote for that nonce. Absent when nobody was crowned |

The record carries no request or response body — capture files exist for that, sampled and bounded. Its `error` field is truncated at 256 bytes, and that is not tidiness: a host error with no message renders its raw upstream payload as the error text, so an untruncated field would write a whole SSE event, generated tokens included, once per failed request. The cap is `maxLoggedErrorBytes`, and the renderer reserves `loggedFieldSlots` keyvals up front, every one a finished request can carry, so the line is never copied to grow (`journal/render_request.go`).

`gateway limiter turned a request away` sits on the journal's progress lane rather than this line's money lane, so it can be skipped when the progress backlog is full — the exact condition a refusal storm creates. A skipped line is counted in `devshard_gateway_journal_progress_dropped_total`; every refusal, logged or not, is still counted in `devshard_gateway_limit_rejections_total`.

`request body not received: connection closed` is the line for a request whose body did not arrive whole: the read deadline fired, the client left or its stream broke. The gateway answers that request 400, closes the connection, and writes the line once with the request and the `error` that stopped the read, which carries the client's socket address, cut at `maxLoggedErrorBytes`. It sits on the progress lane like the limiter line above, so a backlog may drop it.

### Lines that mean something happened

| Line | Why it matters |
| --- | --- |
| `escrow created` / `escrow settled` | the moments funds were committed and left; the settle line is the audit record |
| `settled escrow record dropped` | that row named the only key able to settle the escrow — removal is irreversible |
| `session storage removed past the host horizon` / `session storage left in place` / `session retention pass failed` / `session retention raised to the host horizon` | `removed` escrow session directories left the disk for good; one directory could not be read or removed and is tried again next epoch; a pass that failed runs again on the next snapshot; a configured `DEVSHARD_SESSION_RETENTION_EPOCHS` below the hosts' horizon was raised to it ([operations.md](./operations.md), "Session storage") |
| `shadow probe served` / `shadow probe failed` | how a probe sent to a shadow participant ended: its outcome, duration, output tokens, the first-token time when a byte streamed, and the error when it failed |
| `escrow recovered from commitment` | a create landed while the gateway was down, so `escrow created` never ran; the escrow exists in no other line |
| `commitment cleared` | a creation intent was abandoned, with the reason — one of which (`transaction created no escrow`) means the transaction *did* commit |
| `escrow gone from chain, taken out of service` | `escrow retired` also fires for settlement parking, so this is the only line carrying the cause; it also fires for a row already out of service whose escrow the chain pruned past its settle deadline, which leaves the unsettled count with it |
| `escrow chain epoch and amount unresolved` | the row's `chain_epoch` and `amount` could not be read, with the reason; written once per row until it resolves, so the row's `chain_epoch: 0` in the admin JSON is the only sign it is still unresolved. A row out of service past its settle deadline whose escrow the chain pruned is marked gone from chain, and leaves this line for good |
| `escrow marked for replacement` | **Warn** — an escrow reached its nonce cap and is marked for the next escrow tick, with the `reason`; written once per escrow. A balance mark writes no line, since nothing replaces it: the funding planner's lines below say what happens to the money |
| `regular escrows promoted to temp` | the bridge funded fewer temps than `temp_count` — no room under `max_unsettled`, a model the network does not serve, or a failed create — so the regulars keep serving as temps instead of being retired; `rotation_status.create_error` names the cause (`bridge funded fewer temps than temp_count` for a partial fill) |
| `escrow parked at its settlement deadline` / `escrow deadline passes unsettled` / `settle margin shorter than two settle windows` / `escrow deadlines read past a stale chain height` | an escrow came within `rotation.settle_margin_blocks` of `settle_by`, the switch block by which its chain epoch must settle, and was parked; a row inside its margin that nothing is allowed to settle, or whose deadline has passed (Error, with `reason`: `settlement_disabled`, `operator_deactivated`, `key_missing`, `deadline_passed`; once per row while it lasts; `settle_by` is left out once the switch block is no longer known, as on `deadline_passed`), a margin shorter than two settle windows at the measured block time, and a chain snapshot older than `chain_snapshot_max_age_seconds` whose deadlines are read at `projected_height` (`height` plus `stale_for_ms` over the measured block time, or one block a second), once per episode |
| `escrow funding planned` / `funding guarantee broken` / `model amount cannot fund a full escrow` / `escrow budget reached` | the funding planner's decision for a model and the start of each episode. Progress lane, so a backlog may drop them |
| `nonce burned for nobody` (`journal/render_money.go`) | a committed nonce that will serve nobody, with the escrow and the reason, and under `burned_during_request` the request it was charged to, when there is one |
| `a host stopped mid-answer: reply served, not cached` (`journal/render_request.go`) | a reply served as an answer never reached a terminal `finish_reason`, so nothing replays it. An answer whose host closed the stream with neither `[DONE]` nor a finished nonce never gets here: it is a failed request, a 502 with an error body when buffered and an error event with no `[DONE]` when streamed (`engine/failure.go`, `ErrAnswerTruncated`). This line names the rest, a reply the host terminated or finished without a terminal reason, and only one the cache would otherwise have stored: with `chat_cache_max_bytes` at 0, for a body past the per-entry bound, or when the client had already left, it still passes unnamed |
| `escrow stopped burning nonces at its budget` | the escrow now queues callers rather than spending on requests it cannot serve |
| `escrow warmed` / `escrow warmup voted on its unfinished nonce` | a new escrow's group was taught the escrow, and the probe's own nonce was settled; the vote line is **Warn** when the vote failed, and `catch_up_error` appears only when the catch-up failed |
| `read trimmed diffs from store` (`user/session.go`) | a host behind the diffs the session holds in memory could not be taught its gap, because the escrow's store read failed; nothing was sent, and the next request to that host retries |
| `heartbeat send failed` (`user/heartbeat.go`) | a heartbeat did not reach the host; `backoff` is how long the host is skipped for, doubling per failure up to eight intervals and cleared by any answer ([escrows.md](./escrows.md), "Height sync") |
| `host blocked for state divergence` (`journal/render_race.go`) | the block does not lift while the process runs and no metric exposes it — "why is this host never picked" is answerable only here |
| `chain snapshot stale` / `chain snapshot recovered` | written on the **edge** only; a failed refresh keeps routing on the previous participants until the last poll that read the epoch and the participants passes `chain_snapshot_max_age_seconds`, after which requests are refused 503. The nonce-ceiling and preserved-set reads fall back within the poll and do not hold that clock back |
| `chain epoch` / `chain blocked requests` / `chain unblocked requests` | written on the **edge** only: `phaseNarrator` (`app/observers.go`) decides the change and the journal writes it; a snapshot that carries no epoch — a first poll that failed — announces none |
| `models are open` (`app/compose.go`) | written at boot and after every config change while the network `access` or any model's `access` is `open`; `open_models` names the models any caller reaches without a key (`*` first when the network `access` is open), each spending the escrows that serve it |
| `admin request failed` / `admin request refused` (`api/errors.go`) | the operator mutation lines are written on the successful path only, so a failed operator action would otherwise be invisible |

`escrow` is always the escrow id as text. The chain carries the id behind `escrow created`, `escrow recovered from commitment` and `settle tx broadcast` as a number, and `escrow/` and `chain/` convert it to text before the journal writes it, because a JSON collector reads a number as a different type from every other line's `escrow`.

Admin lines carry the action and its subject, **never a request body and never a key value**. An unkeyed call on an operator route is refused 401 and written down: that is the shape an intrusion attempt takes.

## Metrics

`/metrics`, Prometheus: 97 gateway families beside the Go runtime and process collectors. One of them is the nonce ledger's, the facts it dropped for an escrow it does not hold; everything else it knows is served by its own JSON API (see [accounting.md](./accounting.md)). Grouped by the question they answer:

| Question | Series |
| --- | --- |
| is the gateway serving | `devshard_gateway_requests_total`, `devshard_http_request_duration_seconds`, `devshard_gateway_attempts_terminal_total{visibility="user_visible_winner"}` |
| is it hiding failures | `devshard_gateway_requests_total{outcome="failure"}`, `devshard_gateway_user_requests_with_hidden_failure_total`, `devshard_gateway_attempt_failures_total{visibility="no_winner"}` |
| is it admitting or refusing | `devshard_gateway_limit_rejections_total`, `devshard_gateway_prompt_count_gate_full_total` for a request `DEVSHARD_STATS_TOKENIZER` left on the byte estimate because every prompt count slot was taken, `devshard_gateway_limiter_queue_depth`, and `devshard_gateway_inflight_requests_by_model` against `devshard_gateway_enforced_max_concurrent_requests_by_model`, the cap after overrides and capacity scaling (`devshard_gateway_effective_max_concurrent_requests` is the configured cap before either) |
| how are the hosts | `devshard_gateway_participant_*` (receipt, first content, inter-chunk, transport errors, missed deadlines), `devshard_gateway_host_ejected`, `devshard_gateway_participant_breaker_state`; a host's congestion windows are read from `GET /v1/admin/hosts`, not from `/metrics` |
| are the shadow hosts fit to be trusted | `devshard_gateway_shadow_probes_total`, `devshard_gateway_shadow_probe_first_token_seconds`, `devshard_gateway_shadow_probe_duration_seconds`, `devshard_gateway_shadow_probe_output_tokens_total`, and the `shadow probe served` / `shadow probe failed` lines ([runbook.md](./runbook.md), "The participants setting") |
| is money leaking | `devshard_gateway_ghost_nonces_burned_total`, `devshard_gateway_nonce_holds_total`, `devshard_gateway_timeout_actions_total`, `devshard_gateway_burn_budget_exhausted_total`, and `devshard_gateway_error_miss_verify_rejects_total` for an error the verifiers would not let the gateway claim back ([race.md](./race.md), "Error misses") |
| are the hosts reachable and on time | `devshard_gateway_host_ping_up`, `devshard_gateway_host_ping_rtt_seconds`, `devshard_gateway_host_ping_warm_rtt_seconds`, `devshard_gateway_host_clock_divergence_seconds`, with `devshard_gateway_host_ping_targets`, `devshard_gateway_host_ping_ticks_total` and `devshard_gateway_host_ping_last_probe_timestamp_seconds` saying the probe itself runs ([`hostping/README.md`](../hostping/README.md)) |
| is the shard behind on the votes it owes | `devshard_gateway_owed_timeout_votes`, against `engine_max_concurrent_timeout_votes` ([race.md](./race.md), "The timeout-vote queue"); `devshard_gateway_owed_vote_writes_lost_total` by `cause` (`shed`, `write_failed`), the saves and deletes of owed votes the store lost ([`store/README.md`](../store/README.md), "Owed refusal votes") |
| is the chain view healthy | `devshard_gateway_chain_snapshot_healthy`, `devshard_gateway_chain_snapshot_age_seconds`, `devshard_gateway_chain_epoch_phase`, `devshard_gateway_chain_requests_blocked` |
| is the escrow funding planner deciding sensibly | `devshard_gateway_escrow_money{model,class}`, `devshard_gateway_escrow_count{model,state}`, `devshard_gateway_planner_decisions_total{model,action,reason}` `devshard_gateway_funding_guarantee{model}` (below zero the guarantee is short), `devshard_gateway_planner_ignored_marks_total{model,reason}` (depletion marks the planner dropped; a rising `balance_floor` count is routing reporting what the planner already plans for) and `devshard_gateway_escrow_deadline_unsettled_total{model,reason}` (escrows narrated unsettled at or past their settle margin: `settlement_disabled`, `operator_deactivated`, `key_missing`, `deadline_passed`; any `deadline_passed` is money lost); the journal lines `escrow funding planned`, `escrow starved`, `funding guarantee broken` say why. |
| is memory bounded | `devshard_gateway_buffered_response_bytes`, `devshard_gateway_cache_bytes`, `devshard_gateway_capture_bytes_held` |
| are the request records keeping up | `devshard_gateway_accounting_rows_written_total`, `devshard_gateway_accounting_rows_lost_total`, `devshard_gateway_accounting_retention_sweeps_failed_total` |
| is the journal keeping up | `devshard_gateway_journal_money_refused_total`, `devshard_gateway_journal_progress_dropped_total`, `devshard_gateway_journal_late_events_total` |
| is the nonce ledger losing facts | `devshard_gateway_nonce_ledger_unknown_escrow_facts_total`: facts about an escrow the ledger does not hold — one its sweep has not opened yet, which a published escrow's wake keeps to the first sweep's latency, or one its retention pruned |
| where does a request wait inside the gateway | `devshard_gateway_escrow_queue_wait_seconds`, `devshard_gateway_session_step_seconds{step}` (below) |

A race the client left counts as `devshard_gateway_requests_total{reason="client_cancelled"}` unless the escrow itself failed first, which outranks a departure as `reason="escrow_missing"` or `reason="balance_exhausted"`; the faults of its hosts stay on `devshard_gateway_attempt_failures_total{reason}` instead. A race whose reply the gateway itself refused to carry (the reply buffer full, the reply or one event past its size bound) counts as `reason="reply_refused"` instead of `client_cancelled`, and the request line's `error` begins `the gateway refused to carry the reply`.

A served winner whose nonce is still open when its race ends is counted on `devshard_gateway_attempts_terminal_total` and `devshard_gateway_attempt_failures_total` only when its perf sample is decided ([race.md](./race.md), "A served winner's sample waits for its finish"): as a winner's success when its Finish lands first, and as `failed_not_finished` with reason `not_finished` when its nonce is still open at its vote. Its request is counted on `devshard_gateway_requests_total` at race end; the request's hidden failure is counted at race end when another attempt failed, and otherwise at that decision, once either way.

`devshard_gateway_chain_snapshot_healthy` is the one to alert on first: with a stale snapshot every score, weight and preserved-set decision below it is being made on old data.

### Where the gateway spends a request's time

An escrow hands out nonces one at a time, so its throughput is the inverse of the time one nonce holds its session. Two histograms take that time apart without a profiler, in production and in a load run alike.

`devshard_gateway_escrow_queue_wait_seconds` is how long a request waited in an escrow's dispatch queue before it was handed a nonce (`scheduler/dispatcher.go`, `recordAssigned`). A queue that keeps filling shows here first, before it refuses with `escrow dispatch queue full`.

`devshard_gateway_session_step_seconds{step}` times the session itself (`user/step_timer.go`):

| step | what it times |
| --- | --- |
| `lock_wait_prepare`, `lock_held_prepare` | waiting for and holding the session lock while a nonce is composed |
| `lock_wait_process`, `lock_held_process` | the same while a host's response is applied |
| `store_append_diff` | persisting the nonce's diff, retries included, under the session lock |
| `store_add_signature` | persisting one host signature, off the session lock |
| `host_catch_up` | a send waiting for its host to be caught up |

`lock_held_prepare` bounds the escrow's rate: a mean of 80 ms is at most about twelve nonces a second. When it is high, `store_append_diff` says whether the store is the cause. `step` takes only these values, so the family stays at seven series.

### Cardinality rules

Route labels are **templated** (`/devshard/{id}/…`), never per-escrow, so cardinality does not grow with the escrow set. `/v1/admin/devshards/import` reports under the `/v1/admin/devshards/{id}` label so it lands in the same panel. A recorder keeps every label value it writes non-empty (`metrics/labels.go`, `metricLabel`): an empty label silently merges unrelated series, and a status with no recoverable code reports as `0` (`metrics/vocabulary.go`, `statusNoCode`) rather than as blank. A collector, by contrast, passes its source's values through unchanged, empty ones included.

### Other families

The families no question above needs, with the help text each one exports.

| family | what it reports |
| --- | --- |
| `devshard_gateway_cache_entries` | Replies the response cache currently holds. |
| `devshard_gateway_cache_misses_total` | Requests the response cache could not serve. Against hits this is the only measure of whether the cache earns its memory. |
| `devshard_gateway_capacity_weights_unobserved_by_model` | 1 while escrow scoring for a model runs on the membership-share fallback because the chain has reported no weights. |
| `devshard_gateway_captured_requests_failed_total` | Captures the sink could not write: the directory is unwritable, out of space, or gone. Distinct from refused, which is the byte cap doing its job. |
| `devshard_gateway_captured_requests_refused_total` | Captures refused because the directory is at its byte cap. Nothing evicts capture files, so a rising count means capture is off until an operator empties the directory. |
| `devshard_gateway_captured_requests_total` | Requests written to the capture directory. |
| `devshard_gateway_chain_block_height` | Latest chain block height the observer published. |
| `devshard_gateway_chain_block_reason` | Current request-block reason (1 for the active one). |
| `devshard_gateway_chain_epoch_index` | Latest epoch index the observer published. |
| `devshard_gateway_chain_epoch_switch_block_height` | Block height at which the current epoch switches. |
| `devshard_gateway_chain_max_nonce` | Escrow max-nonce parameter; 0 means not yet fetched. |
| `devshard_gateway_effective_max_input_tokens_in_flight` | Configured input-token cap, before per-model overrides and capacity scaling. |
| `devshard_gateway_enforced_max_input_tokens_by_model` | Input-token cap a model's requests are judged against, after its overrides and capacity scaling. |
| `devshard_gateway_escrow_drain_close_failures_total` | Drained escrows whose snapshot flush or session close failed. |
| `devshard_gateway_escrow_weight` | Per-escrow effective host weight used by the capacity-aware picker. |
| `devshard_gateway_host_seconds_per_output_token` | Slow-quartile decode cost per output token, measured from the first content chunk. |
| `devshard_gateway_participant_first_content_seconds` | Time from inference send until first content by participant and model. |
| `devshard_gateway_participant_inter_chunk_seconds` | Mean silence between streamed chunks within one attempt, by participant and model. |
| `devshard_gateway_participant_max_inter_chunk_seconds` | Longest silence between two streamed chunks within one attempt, by participant and model. |
| `devshard_gateway_participant_missed_deadlines_total` | Total receipt and first-token deadlines a participant missed on a model, each of which narrowed its window. |
| `devshard_gateway_participant_output_tokens_total` | Output tokens a participant generated on a model: the host's usage completion_tokens, else the logprob entries it streamed. |
| `devshard_gateway_participant_prefill_seconds_per_input_token` | Receipt-to-first-content time divided by input tokens, by participant and model. |
| `devshard_gateway_participant_receipt_seconds` | Time from inference send until receipt confirmation by participant and model. |
| `devshard_gateway_participant_total_attempt_seconds` | Total inference attempt time by participant and model. |
| `devshard_gateway_served_bindings_total` | Total served streams checked against the Finish their host signed, by verdict. |

### Watching the funding planner

The planner and the epoch bridge create and retire escrows. An escrow whose balance falls under the model's floor keeps serving the requests it can afford: routing spends starved residue first on small requests, and a depletion mark other than the nonce cap is dropped and counted instead of retiring the escrow. At least `full_context_slots` full regular escrows are kept (the guarantee; a standby does not count), capacity follows demand, and creates are bounded by `max_unsettled` and by a rate bucket of two at once, then one per refill. The epoch bridge keeps its window: inside it the planner funds only the guard shortfall, held to `temp_count`, and the capacity shortfall, both as temps at the bridge label, and retires nothing but a nonce-capped escrow. After PoC it funds the regular set and retires the old temps once the spread is met. Read `escrow funding planned` and `devshard_gateway_planner_decisions_total` to see what it decided.

What to watch:

| Signal | Healthy |
| --- | --- |
| `devshard_gateway_funding_guarantee{model}` | at or above 0; below 0 the guarantee is short, and `funding guarantee broken` says why (wallet, breaker, PoC); inside the bridge window a model whose `temp_count` is below `full_context_slots` can read below 0 without that line |
| `devshard_gateway_escrow_money{model,class}` | `free` follows demand; `stuck` stays near 0 |
| `devshard_gateway_planner_decisions_total{model,action,reason}` | creates and retires track demand; a steady stream of `capacity` creates with a flat `max_unsettled` is churn against the budget |
| `devshard_gateway_planner_ignored_marks_total{model,reason}` | balance marks dropped by design; a rising `balance_floor` count is routing reporting what the planner already plans for |
| `devshard_gateway_escrow_deadline_unsettled_total{model,reason}` | 0; any `deadline_passed` is money lost |

## Tracing

`DEVSHARD_OTEL_ENABLED` turns OpenTelemetry tracing on, exporting to `OTEL_ENDPOINT` with `OTEL_HEADERS`; spans carry the service name `devshardctl` (`observability.GatewayServiceName`), which the Tempo queries and dashboards select on. Off, the tracer is a no-op. A broken exporter or a malformed header degrades to no tracing and a warning, never to a gateway that will not start (`app/tracing.go`), and spans are flushed for up to ten seconds at shutdown.

Every chat request opens a `gateway.request` root span (`api/routes.go`, `chat`), and the race runs under it. The transport stamps `traceparent` and `X-Request-Id` on every host call made under a live span, over JSON and Connect alike, so a host's spans join the request's trace; `trace_id` is the join key with the logs. Timeout votes and settlement run after the request on their own context and start no trace. Ids belong in span attributes and log lines, never in metric labels ([`devshard/docs/gateway-tracing.md`](../../../docs/gateway-tracing.md)).

How each escrow reaches its hosts is in two series: `devshard_gateway_escrow_sessions_total{path}` counts each escrow-host binding once, by `h2` (Connect over HTTP/2) or `json`, and `devshard_gateway_host_rpc{peer,mode}` is 1 per host and mode while a live escrow binds it (`registry/registry.go`, `bindPeerRPC`; `metrics/peer_rpc_recorder.go`).

## Reading the gateway's state

| To see | Call |
| --- | --- |
| what is routable, and the config in force | `GET /v1/admin/state` |
| the stored admin overrides, as a `PUT` would write them back | `GET /v1/admin/settings` |
| the client API keys, without their values | `GET /v1/admin/api-keys` |
| every escrow row the process owns | `GET /v1/admin/devshards` |
| the full participant keys to paste into a `participants` list | `GET /v1/admin/hosts` — never a log line's short host label, which is the last eight characters and matches nobody |
| rotation's last outcome per model | `GET /v1/debug/rotation` |
| which slot answered which height-sync turn | `GET /v1/debug/heightsync` |
| one escrow's protocol state | `GET /devshard/{id}/v1/state` |
| what happened to one request's nonces | `GET /v1/requests/{id}` |
| hosts the gateway distrusts | `GET /v1/admin/suspicious-hosts` |
| which filters each model runs, and where they come from | `GET /v1/admin/filters` |
| why one host is or is not taking work | `GET /v1/admin/hosts` |

`GET /v1/debug/heightsync` reads each session's published height-sync view, not the session itself, so the read never waits on an escrow's lock (`registry/session.go`, `HeightSyncView`). A nonce commit and an applied response only mark that view stale and leave its snapshot, which takes the session lock again, to the view's trailing flush (`user.Session.publishHeightSyncViewOffPath`): the view lags the session by about its publish interval.

An escrow row in `GET /v1/admin/state` and `GET /v1/admin/devshards` carries, besides the gateway's own labels (`rotation_role`, `rotation_epoch`, `active`, `settlement_pending`), the chain facts the gateway resolved for it: `chain_epoch`, the epoch the chain stamped on the escrow, whose settle deadline the deadline rule reads (`0` while unresolved); `amount`, the escrow's funded amount (`0` while unresolved); and `gone_from_chain`, true once the chain no longer holds the escrow, which takes it out of service and out of the unsettled count. A `GET /v1/admin/devshards` row whose session is resident (published or still draining) also carries `runtime` with the session's current `nonce`; a row with no resident session has none, which is how a reader tells a live escrow from a stored one (`api/admin_devshards.go`).

### What a host answer carries

`GET /v1/admin/hosts` joins the two snapshots that already exist, one row per participant and model. Nothing is computed for it: each field is a value one of the two limiters or the tracker already holds, asked for on demand rather than waited for at the next scrape. For the congestion windows it is the only surface there is — no gauge carries them.

| Field | Where it comes from |
| --- | --- |
| `ejected`, `degraded`, `inflight`, `decode_seconds_per_token` | the performance tracker: whether routing withholds this host, whether it would without the pool-wide cap, and how fast it decodes |
| `input_window_tokens`, `output_window_tokens`, `inflight_input_tokens`, `inflight_output_tokens`, `cutoff`, `backoff_count`, `available` | the participant limiter: the size of each congestion window, what is in flight against it, the cut-off's state, how deep its backoff has gone, and whether the host would be admitted right now |
| `context_limit`, `version_refusals`, `tool_refusals`, `context_refusals` | what this host's build refused |
| `suspicious` | whether an operator pinned it |

`backoff_count` is the answer to "was this host cut off once or eleven times": it is what makes the next cut-off a minute long instead of five seconds. `degraded` beside a false `ejected` means the pool-wide cap is withholding the withholding, because too many of that model's hosts are failing at once.
