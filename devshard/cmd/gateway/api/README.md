# `api` — the HTTP boundary

The only package that speaks HTTP to a client, and the one that decides what a failure looks like from outside.

## What it owns

| File | What it holds |
| --- | --- |
| `routes.go`, `server.go` | the route table and the server; every route names its own label and whether the operator kill switch exempts it |
| `middleware.go` | authentication, body limits, request ids |
| `admission.go` | the chain check a request passes before it is queued |
| `limiter_slot.go` | the limiter slot and token budget a request holds until both its handler and its race are done |
| `stream.go` | `clientStream`: the writer the race streams through, and the fold for a non-streaming client |
| `buffer_budget.go` | the ceiling on the bytes every non-streaming reply holds at once |
| `cache.go` | the response cache, keyed on everything that varies the answer |
| `errors.go` | which error becomes which status |
| `admin*.go` | the operator surface: escrows, overrides, quarantine, accounting resets |
| `capture.go`, `inspect.go` | sampled request capture for diagnosis |
| `dispatch.go`, `timeouts.go` | the adapters that join a registry session to the race and to the vote path |

## Boundaries

- **The gateway's own limiter answers 429; a full shard answers 503.** A quota the caller exceeded is theirs to slow down; no room on the shard is not.
- **A reply is folded as it arrives**, so a non-streaming request holds the answer being assembled rather than the stream it came from. Past the process-wide ceiling it is refused with 503 rather than held until the kernel intervenes.
- **The status is chosen from the assembled body, not before it.** A body the assembler could not fold is answered as a failure, not as a 200 carrying an error object.
- **`clientStream` is written by attempt goroutines after the handler has returned**, so every path through it takes the lock and stops once closed, and every way a reply ends closes it: `Close`, `Fail`, `closeUnstarted` on a race that failed before the first byte, and `discard`. A late write is dropped and charges the buffer budget nothing.
- **The admin surface is disabled entirely when no admin key is set** — including escrow creation.

## What the server is given

`Deps` is everything the boundary reads or calls. `New` requires every field except `Version`, `RequestIDs`, `StopTokenVocabulary` and `PromptTokens`. The first two default to empty and to a random id generator; a nil `StopTokenVocabulary` refuses every request that carries `stop_token_ids`, since no id can be proven inside the model's vocabulary without it (see [`filters/README.md`](../filters/README.md), "Stop token ids"); a nil `PromptTokens` checks no prompt against its model's context length, which is what the composition root wires without a chain to read a tokenizer pin from.

`Operations` holds the lifecycle actions the operator routes trigger. They span the chain client, the escrow manager and the registry, so the composition root implements them and this package only calls them.

`admission` is the pre-queue chain check. Relaxed proof-of-compute mode is read at three further sites: the capacity fold in `main` (`modelCapacity.ModelWeights`), and the race's own bypass and generating checks in `engine`.

`Journal` receives the request lines. The boundary builds a `journal.RequestLine` on the handler goroutine, reading the client stream's byte count under the stream's mutex (`api/stream.go`), because attempt goroutines can still write the stream, and the [`journal`](../journal/) renders it.

## Authentication and the kill switch

`credentials` is one request's resolved identity, computed only where an answer is used: `resolveCredentials` returns an empty value when no `Authorization` header was sent, so an unauthenticated request never reaches a key comparison at all, and a route that needs neither answer never compares a key.

`keyGate` hashes both the presented bearer and every configured key before comparing them, and compares against all of them, so the time taken does not depend on which one matched. The scheme is read in any case (`bearer`, `BEARER`), as RFC 9110 defines schemes; the key itself stays case-sensitive, and a header that is not exactly a scheme and one key is refused (`bearerKey`).

`requireAdmin` is where the admin key comparison lives, on the operator routes themselves rather than in a blanket middleware. With no admin key configured the whole surface answers 404.

`disabled` is the operator kill switch. Routes marked `alwaysOn` — `/metrics`, `/healthz`, `/v1/status` and the operator surface — stay reachable through it: `/v1/status` is what a container healthcheck from the devshardctl deployment reads, and a gateway disabled on purpose is not unhealthy.

## Reading a body

- `chatIngestLimit` stops a body at the socket that the filters would reject after buffering it.
- `adminIngestLimit` bounds every operator body; the largest of them is a settings patch.
- `bodyReadTimeout` is armed per request rather than as `http.Server.ReadTimeout`, which stays armed for the whole exchange: there it would expire mid-response and cancel the request a long stream is still writing for.
- `bodyReadStart` is where every body begins, wide enough that an ordinary chat request is read into one buffer. It is a constant and never the declared `Content-Length`: the body read happens before authorization, admission and the limiter, so a reservation taken from a header would let an unauthenticated request that sends no bytes at all hold memory for the whole `bodyReadTimeout`. A body larger than the start grows the way `io.ReadAll` grows one, which keeps the memory held within a constant factor of the bytes actually received; `chatIngestLimit` is what bounds the body.
- `readBody` bounds ingest with `http.MaxBytesReader` rather than `io.LimitReader`: it returns a typed error the status mapper recognises, and it marks the connection so the server stops reading a hostile body instead of draining it. The read deadline is cleared again before the response begins, so only the read is bounded. A writer that neither carries a deadline nor unwraps to one reports `ErrNotSupported`, and the size bound stands alone.
- `baseWriter` walks the `Unwrap` chain down to the writer the server owns, because `MaxBytesReader` marks a connection by type-asserting its writer: handed a wrapper it cannot see through, it leaves the server draining a hostile body it has already refused.
- `HTTPServer` sets neither `ReadTimeout` nor `WriteTimeout`. Both are absolute deadlines over an exchange that outlives the request, and either would cut an SSE stream mid-answer; the body read carries its own deadline instead, armed and cleared per request in `readBody`.

## The route table

Each `route` is one registered pattern. An empty label means the route is not instrumented, which is how `/metrics` stays out of its own counters and a container healthcheck on `/healthz` stays out of the request series; `alwaysOn` exempts a route from the operator kill switch; `otherRouteLabel` is the single label every unmatched path folds into.

`/v1/admin/devshards/import` carries the templated `/v1/admin/devshards/{id}` label: the established series covers this path under the same name, and a label of its own would split the panel that reads it.

`handleDevshardChat` pins the race to one escrow, and refuses a pin the gateway no longer routes to as an unknown devshard (`ErrUnknownDevshard`, 404).

`routableModel` refuses an unroutable model before it can take a limiter slot or an input-token budget, and fails closed on an empty registry. A model the operator offers — named in `limits.model_access` or `limits.model_limits`, via `config.Limits.Offers` — answers the same `ModelUnavailableError` as an empty registry when nothing currently routes to it; a model nobody offers answers `UnsupportedModelError` instead.

## What the boundary hands the engine

`Sessions` adapts the live escrows to the engine's two outbound boundaries: the target a race dispatches through, and the poster that settles the nonces the race left unfinished. The `escrows` interface it reads has two lookups that differ on a retired escrow — see `rules.md`, "4. Routing and settlement read the escrow set asymmetrically".

- `Target` resolves one escrow per race, because an escrow can rotate out between the pick and the send. Its release keeps the escrow draining rather than closed until the race's vote is posted.
- `Send` hands the stream on unwrapped: the transport flushes each SSE line through an `http.Flusher` assertion on it, and a wrapper would leave a crowned winner's bytes in the server's buffer. `ProcessResponse` is applied for a reply that arrived beside an error too.
- `divergedState` reads a post state root the escrow cannot accept from either side of the wire: the host reports it as a diff it could not apply, the session as a hash that differs from the local root.
- `Poster` resolves the vote poster for one escrow and the params its race committed. A retired escrow still resolves: its committed nonces have no other settlement path.
- `timeoutPayload` rebuilds what the host was asked for. A verifier re-checks the payload field by field against the record the nonce committed, so every field but the prompt is read back from that record rather than carried alongside it. The record is read on its own with `Inference`, never through `SnapshotState`: a vote comes when an escrow is already struggling, and a full state copy per vote is what it can least afford. The status views read the balance and the version the same way.

## Streaming the reply

`maxBufferedResponseBytes` tracks the stream carry cap on purpose, and caps the SSE a race winner may send. Without a cap at all, a host that wins the race on its first token can then send unlimited valid SSE and take the process out; and because this buffer holds the same `prompt_token_ids` frames the carry cap was derived from, lowering one without the other would refuse the large-context replies that arithmetic was meant to admit.

`writeChatHeaders` is the one place every chat reply header is set, so a cache replay and a live race cannot answer the same request differently.

`statusForAssembled` answers 502 for the bodies the assembler substitutes for an answer it could not fold: the host is upstream, and neither body carries a status of its own. It also answers a failure the assembler *folded in*: a host can end a reply with an error object beside the content it had already produced, and a body carrying one is not an answer whatever the race concluded. The status comes from the same rule the pre-content path uses, so a host naming its own 400 is answered 400 and everything else 502. This is the one path where the whole body is known before the status is committed, which is why the check belongs here as well as in the classifier.

**The gateway writes the stream's terminator, and never forwards the host's.** `TrimSSEDone` drops a `[DONE]` out of every forwarded chunk and `terminateLocked` writes the gateway's own at the end. Relaying the host's would put it on the wire before the gateway knows the nonce finished, and an error event written afterwards is invisible: every OpenAI-compatible client stops reading at the terminator. Owning it means a failure after the host finished still reaches the client, ahead of the sentinel. A truncated answer (`engine.ErrAnswerTruncated`) is the one failure written without it: the stream ends on the error event, so a client that skips error events still never reads the partial answer as whole. A cache replay writes its stored body directly and keeps the terminator it stored.

In `race`, the second `X-Devshard-ID` write is the authoritative one for a reply still holding its headers. The error from `client.Close()` is returned rather than only logged: a tail the rewriter could not finish is a failure the status can no longer carry — the reply is already 200 — and returning it is what makes the cache refuse a body that stops mid-answer. `race` reports that failure alongside the outcome for the same reason: a stream commits 200 on its first byte, so the caller needs the error itself to tell a response worth replaying from one that is not.

`HTTPServer` gives every request a context derived from one it returns the cancel of (`BaseContext`), and `Shutdown` alone never calls it, so a request that ends within the shutdown's graceful share is never cut. Nothing else ends an SSE stream: the shutdown calls the cancel only once that share has passed with requests still open, so a stream that would never end cannot hold the shutdown for the whole grace period ([`docs/operations.md`](../docs/operations.md), "Shutdown"); a race whose client is cancelled this way detaches and drains like any other client departure.

## The request row carries two kinds of token count

`estimated_input_tokens` is the gateway's own count of the prompt — the request's one count, which the congestion windows and the nonce ledger read too — and `estimated_input_tokens_source` says where it came from: `tokenizer_exact`, `tokenizer_sampled`, or `bytes_estimate` for a model without a loaded tokenizer, a request the stats tokenizer left uncounted or that found the prompt count gate full, and every row written before the column existed ([`docs/request.md`](../docs/request.md), "The prompt token count"). The tokenizer counts the messages without the chat template, so even an exact count stays below the host's `prompt_tokens`. `prompt_tokens` and `winner_output_tokens` are what the host reported in its `usage` object. They are different kinds of number about the same request, so the row names them apart rather than serving an estimate beside a measurement under names that read as a pair. A host that reports no usage leaves the measured halves at zero; the estimate is never copied into them, or the estimate's own error would disappear from exactly the rows that would have shown it.

Both halves of `usage` latch together over an attempt, so a row can never pair one usage object's prompt with another's completion. Across the two ledgers one word means one thing: `estimated_input_tokens` is always the gateway's estimate, `input_tokens` is always the chain's count, and `prompt_tokens` is always what the host measured.

## Errors and statuses

- The gateway's own limiter is a quota the caller exceeded, which is 429; capacity and a busy escrow are the shard having no room, which is 503.
- `writeError` renders the JSON envelope; `http.Error` would send `text/plain` instead.
- `writeControlFailure` is not `writeErrorFor`: that one answers 502 for an unrecognised error, which is wrong for a store this process owns.
- `rateLimited` singles out the gateway limiter's own rejection, because the status, the `Retry-After` header and the counter that names which cap was hit all ask for it.
- `Retry-After` is rounded up: a zero would tell a client to retry immediately, the opposite of what a queue timeout means.
- `maxLoggedErrorBytes` bounds the body `failureRecorder` captures for an admin failure, because a `HostApplicationError` with no message renders its whole upstream payload. A body truncated at that cap no longer parses, so `failureRecorder.reason` falls back to the raw text rather than an empty field. The same truncation for a log line's own `error` field lives in the [`journal`](../journal/)'s own `loggedError` (`render_request.go`), with its own copy of the cap.
- `adminFailure` exists because `auditAdmin` records only the successful path. Its `failureRecorder` forwards `Flush` so a wrapped handler keeps streaming.

- **A snapshot too old to trust is its own refusal class.** `ChainStaleError` answers 503 with `Retry-After` of one poll interval, not the one-second default: the chain has by then been unreadable for at least `chain_snapshot_max_age_seconds`. `/v1/status` reports it as `chain_snapshot_stale`, so blocked is never returned without a reason. An unset timestamp is stale — the listener opens after the observer starts, so there is no phase to serve under before the first poll lands.
- **A fleet with no balance left retries on the escrow tick, not in a second.** A refusal carrying `types.ErrInsufficientBalance` answers 503 with `Retry-After` of `escrow.TickInterval`, the same cadence `ModelUnavailableError` takes: only a replacement escrow or a settling inference restores a balance, so the one-second default every other full shard takes would be a retry that cannot succeed. A shard that is merely busy keeps the short retry, because it passes on its own.
- **A model the operator offers with nothing routable is the gateway not being ready, not a bad request.** `ModelUnavailableError` answers 503 with `Retry-After` of `escrow.TickInterval`, the cadence at which a drained model is parked, replaced and republished — the same precedent as `ChainStaleError` taking the chain observer's poll interval. A model nobody offers answers 400 with `UnsupportedModelError` instead, listing the supported models, and carries no `Retry-After`.
- **A prompt past its model's context is the client's to shorten.** `ContextLengthError` answers 400 with OpenAI's `type: invalid_request_error` and `code: context_length_exceeded` (`detailForError`), the shape OpenAI-compatible clients branch on to trim a conversation; the message names the context length, `max_tokens`, the prompt's tokens and their total, and how much to cut from the messages or `max_tokens`. See [`docs/request.md`](../docs/request.md), "Step 6 in detail".
- **A race where every attempt was refused as overloaded is the shard having nowhere to send the request, not an upstream answering badly.** `engine.ErrHostsUnavailable` answers 503 with `Retry-After` of the live `HostCutoff.BaseMS`, rounded up to whole seconds, read from `s.config` the same way every other handler reads runtime configuration — the same base a refusing host is cut off for.

## What a finished request records

`finishRequest` hands the [`journal`](../journal/) the one question a finished request can no longer be asked: how much reached the client, and whether the terminator went with it. A delivery error is the difference between a reply the client read and one it is still waiting out its own timeout for. Input tokens ride along because every escalation deadline is computed from them.

The journal's `winnerOutputTokens` is what the client actually received, which with `duration_ms` — measured on the handler goroutine, not the line's own timestamp, which trails the request's end by however long the journal's queue held it — is what a measured output rate is computed from.

A request's prompt is counted at most once. `checkContext` reads the prompt for the model's pinned [`tokenizer`](../tokenizer/) (`PromptTokens.Prompt`) and tokenizes it only when its byte bound (`PromptText.MaximumTokens`) does not fit the context, judging by the count's `MinimumTokens`. After admission and the cache lookup, `countPrompt` reuses that count, else, with `DEVSHARD_STATS_TOKENIZER` on, tokenizes the prompt once with the piece cap (`PromptText.CountWithPieceCap`) when the prompt count gate has a free slot, else takes `filters.EstimatedPromptTokens` of the body; the limiter, the race and the ledgers read its `Tokens` ([`docs/request.md`](../docs/request.md), "The prompt token count").

`RaceLedger` is how the engine's single recording point reaches the ledger; nothing else writes a row.

## The views a response is built from

`filterOptions` is where the operator's configuration becomes the request-shaping rules. Forcing is expressed as its opposite there so that an `Options` built without it keeps forcing.

`modelTokenLimits` closes over the per-model override map only when an operator configured one, so a deployment without overrides allocates nothing on the request path.

`devshardView` and `rotationView` keep the storage rows out of the response: without them a column rename in the store silently changes the API, and the record's own field names reach the client as written.

These keys are fixed: an escrow is `id` in `/v1/status`, `/v1/admin/state` and the `/v1/admin/devshards` answers and requests, and `active_requests`, `latest` / `model_id`, `suspicious_hosts`, `loaded_runtimes`, `escrows_removed`, `nonces`, `max_concurrent_requests_per_10000_weight` in `capacity`, `active` on import and `model_id` on escrow creation. Import takes `source_path`, the path the gateway copies the escrow from. The `/v1/admin/devshards` listing goes through `devshardView` like `/v1/admin/state`, so an empty `settle_tx_hash` or `route_prefix` is left out. `/v1/status` reports `chain_phase`, the chain's epoch phase as the last snapshot read it, and a listing row carries `runtime` only while its session is resident; `admission_test.go` and `admin_test.go` read those two by name, and `fixed_keys_test.go` holds the rest in place. The admin settings keep their own keys: `config.Overrides` is also the stored form, so its keys stay as they are.

`devshardStatus.SessionVersion` is the protocol tag the session was created under and the one every settlement payload carries. An operator reading this endpoint has no other way to see which version an escrow is bound to, and a mismatch with the host it dispatches to is what makes a settlement unacceptable.

`capacityStatus` carries each weight under two names with one value (`current_weight` and `total_weight`, `baseline_weight` and `full_weight`, `scale_factor` and `limit_share`) — see `operations.md`, "Reading the gateway's state".

## Model filters

`/v1/admin/filters/{model...}` is the operator's per-model filter setting (`admin_filters.go`). The wildcard takes the slashes a model id carries, and the literal `/v1/admin/filters/presets` beats it, so no model can be named `presets`; the metric label is the template, so model ids never reach a label.

- **A PUT decodes strictly** (`decodeAdminBodyStrictly`, `strictjson.DecodeKnownFields`): an unknown key, at the top or inside `filters`, and anything after the first JSON value are a 400, and a body that names neither a preset nor a filter is a 400 rather than a stored no-op. Key names match case-insensitively and a repeated key keeps its last value, as the standard library decoder does; the stored rows are read back through the same decoder.
- **An entry is built from one `ModelFilters.Snapshot`**, so its stored `filters` and its `effective` profile always come from the same write. A row the gateway could not read at boot is listed as `source: unreadable` with its `problem` and is not applied. `warnings` names the settings limits that override the filters (`limitWarnings`).
- **The response cache key carries the empty-answer exemption** (`cacheKeyFor` takes the whole `filters.Result`): it is the one filter that changes classification without changing the body.
- **`effective` is `Profile.Knobs`**: the model's filters written as the same object a PUT carries, so a field rename inside `filters.Profile` never changes the answer.
- **The chain check reads the cached snapshot**: no chain call on the admin path. It runs on PUT only, unless `allow_off_chain` is set — and always for a setting whose composed profile `RequiresRegisteredModel`; reads and DELETE never need the chain.
- **`limits` in an entry is read-only**: the model's token limits from `/v1/admin/settings`, shown so everything about a model is in one answer.
- **`ModelFilters.Put` composes before it stores**, so a setting the filters refuse never reaches the database (`app/model_filters.go`).
- **The chat path reads `ModelFilters.Profiles()` once per request** and passes it to the filters in `filters.Options`; the resolved thinking-budget flag goes on to the engine with the request.

## The operator and recovery surface

`auditAdmin` records an operator action that changed state the gateway serves or settles from. It carries the action and its subject, never the body: an override payload can hold the admin key.

`badRequestUnlessOversized` keeps an ingest-cap rejection at 413 while every other decode failure is treated as the client's malformed body.

`DevshardStoragePath` is where one escrow's session keeps its SQLite files, and sessions and the delete route must derive it the same way.

`escrowInspection` is one escrow's state as an operator reads it while recovering a stuck devshard. Live and sealed inferences are counted apart because sealing drains records out of the live map, so the live tail alone reads as a whole history and is off by the escrow's entire lifetime. `unresolved` marks the inferences that have not produced a result yet — the ones holding a stuck escrow short of settlement.

`inspectable` resolves a handle for reading: a resident escrow answers from its own session, and an already-settled one is rehydrated from local storage, because "what did this escrow do" is a question asked after it stopped serving, not while.

## Request capture

`admit` passes a deterministic stride of what it sees rather than a random sample.

The sink's two failure counters mean different things. `refused` only ever grows: nothing deletes capture files, so once the directory reaches its cap the sink is off until an operator empties it, and this count is the only signal that has happened. `failed` is the other way capture goes dark — an unwritable or full directory — and is counted apart because it needs an operator, not a cleanup.

## The response cache

An entry is keyed on **everything that varies the answer**, and two of those are not obvious:

- **The logprob intent**, because the normalised body is not part of what varies: the force rules make one client's request byte-identical to another's, and the two are answered with differently stripped bodies.
- **The client's streaming and usage intents, separately from the body**, because the body asks every host to stream and to report usage. Two callers wanting different reply shapes would otherwise share one entry, and whichever arrived first would decide what the second one got.

The caller and the escrow scope are in the key as well, so a cached reply never crosses a caller or an escrow boundary.

**One entry is bounded by what a single reply may hold**, not by the cache's own ceiling: the recorder buffers into memory per in-flight request, so bounding it by the cache would let every concurrent request hold the whole cache on its own, outside every budget that reports held bytes. A reply past that bound is dropped as it streams rather than held to the end and refused by `put`. The recording also spends the shared response buffer budget (`BufferBudget`, `max_buffered_response_bytes`) for as long as the request runs, so recordings are counted with the folded replies in every report of held bytes; a recording the budget cannot hold stops the same way, the client still receives every byte, and what it held goes back when the request ends (`cacheRecorder.releaseBudget`).

**The recorder keeps the first status the handler chose**, so a write after the response has begun cannot turn a recorded failure into a 200. What the bytes cannot state themselves — a client that left, and a failure a committed 200 hid — is passed to `entry` as the unstorable reason.

**A reply that never finished its answer is served and never stored.** `put` asks `filters.CacheRefusal` of the bytes it is about to keep, outside the lock, and hands back what refused them — see [`filters/README.md`](../filters/README.md), "Finishing an answer" for what counts as finished. `chat` logs the one refusal that is news: a served reply whose choices never reached a terminal reason, which no other line names. The client still receives every byte that arrived; what stops is the replay, not the delivery.

`get` re-asks only the error verdict: the finish verdict cannot change under a stored entry, since `put` is the one way in and the stored bytes never move. That read runs the walk in its cheap form, which decodes no choices, so a hit never pays the store's price under the cache's lock.

Eviction drops entries in map order: with a per-caller key and an hour's TTL there is no access pattern a smarter policy would reward, and map order costs nothing.

**A hit is served while proof-of-compute blocks requests.** `chat` runs admission first and keeps its refusal; a refusal that is proof-of-compute alone lets the lookup run, a hit is served, and a miss gets the same refusal. Every other refusal is written before the lookup. See [`docs/rules.md`](../docs/rules.md), "No cached reply behind any refusal but proof-of-compute".

Every reply offered to the cache is counted in `devshard_gateway_cache_results_total{model,result}`: `stored`, or the refusal by name — the cache's own (`too_large` for a reply past the entry limit, `write_failed`, `race_failed` for a race that failed or a client that left, `no_escrow`) or the `filters` vocabulary (`empty_body`, `unfinished_answer`, `transient_failure`, `unreadable_event`, `unreplayable_status`). Hits and misses stay in `devshard_gateway_cache_hits_total` and `_misses_total`; this counter answers the other question, why a reply that ran a race is not there to hit.

## Read next

- [`docs/request.md`](../docs/request.md) — one request from the socket to the settled nonce.
- [`docs/operations.md`](../docs/operations.md) — every route and every knob.
