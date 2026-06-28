# Pre-proposal: Resume disconnected requests when streaming tokens

We should be able to resume disconnected requests when streaming tokens.
Today neither vLLM nor devshard exposes mid-stream resume or token polling.
This note records the current gaps, what vLLM is adding elsewhere, and how
`devshardctl` already behaves on client disconnect — as context for a future
design.

## What does not exist today

| Mechanism | Status |
|-----------|--------|
| Resume vLLM stream from token offset | **No** — `/v1/chat/completions` is one HTTP request; on write failure the proxy closes the upstream body |
| Poll devshardd for "remaining tokens" | **No** — no such API |
| Poll host for partial stream by inference ID | **No** — `GET /v1/inference` returns state-machine metadata (`InferenceRecord`, sealed flag), not the token stream |

vLLM does not expose "continue this completion from chunk N." Devshard does
not buffer a pollable partial stream for clients.

### Implications

- A dropped client SSE connection cannot be reattached at an offset.
- Recovery today is **full replay** (same inference ID after host completion,
  via `completedResponses` / `CachedResponseBody`), **redundant new attempts**
  on another host, or **partial on-chain finish** if the executor captured
  chunks before upstream abort — not resume from chunk N.
- `metaDrainTimeout` in `devshardctl` is **not** client reconnect; it keeps
  the proxy→host connection alive briefly so protocol state (`devshard_meta`,
  `ProcessResponse`, `MsgFinishInference`) can advance even when the end user
  has gone away.

## Industry standards deep dive

Resume is often discussed as one feature, but the industry actually splits it
into **three layers**. Confusing them leads to wrong designs.

### Layer 1 — Transport: SSE over HTTP (dominant)

OpenAI, Anthropic, Google, and most gateways deliver tokens via **Server-Sent
Events** (`Content-Type: text/event-stream`) on a single HTTP response. That is
the de facto standard for chat-style LLM APIs.

The SSE spec defines `id:` fields on events and a `Last-Event-ID` request header
on automatic browser reconnect ([WHATWG SSE](https://html.spec.whatwg.org/multipage/server-sent-events.html)).
That mechanism only works when **the server buffers events** and replays from
the cursor. Major provider **Chat Completions** APIs generally do **not**
expose that for public clients — `EventSource` also cannot call them directly
because they use POST + auth headers, not GET.

References:

- [LLM output streaming architectures (Zylos, 2026)](https://zylos.ai/research/2026-03-28-llm-output-streaming-token-delivery-architectures)
- [Resume tokens and Last-Event-ID (Ably)](https://ably.com/blog/resume-tokens-last-event-id-llm-streaming-reconnection)
- [AI token streaming: SSE to durable sessions (WebSocket.org)](https://websocket.org/guides/use-cases/ai-streaming/)

### Layer 2 — Provider API: what each major API actually offers

| Provider / API | Mid-stream resume on standard chat API? | Typical recovery |
|----------------|----------------------------------------|------------------|
| OpenAI Chat Completions (`/v1/chat/completions`) | **No** | New request; partial assistant text in message history |
| OpenAI Responses (`/v1/responses`, background mode) | **Yes** (event replay) | `GET ?stream=true&starting_after={seq}` — see below |
| Anthropic Messages (`/v1/messages`, `stream=true`) | **No** | New request with partial output in context |
| Anthropic Managed Agent Sessions | **App-level** | `events.list()` history, then live stream + dedupe by event ID |
| vLLM `/v1/chat/completions` | **No** | Abort on disconnect; Realtime/streaming-input is separate |

**Industry default for Chat-Completions-shaped APIs:** client disconnect →
**abort the live stream** or **start a new request** (often with partial text
re-submitted). There is no universal “poll remaining tokens from the model”
endpoint.

Manual continuation (all providers, fallback): send a **new** inference with
the partial assistant response in the conversation and an instruction such as
“continue where you left off.” That re-runs the model; it is not offset
resume and may diverge from the original generation path.

### Layer 3 — Application / gateway: where production “resume” usually lives

Systems that need reliable reconnect build a **gateway** between client and
model:

1. Assign a monotonic ID to each streamed chunk (`id:` / `sequence_number` /
   custom cursor).
2. Buffer chunks server-side (Redis, DB, in-memory ring buffer).
3. On reconnect, replay from the client’s last acknowledged cursor.
4. Optionally keep the **upstream** model request alive while the client is
   gone (OpenAI `background=true`; devshard `metaDrainTimeout`).

Critical distinction (also noted in industry writeups):

> Model inference usually **cannot** cheaply resume from arbitrary token *N*
> on the GPU (KV cache is tied to the live request). Practical “resume” is
> almost always **event replay** (re-send buffered deltas) or **full
> regeneration** (new API call), not “reattach to the same model forward pass
> at token 847.”

Devshard today is closest to **gateway event replay at completion** (host
`completedResponses`, sealed disk lookup) plus **upstream drain for protocol
finish** — not provider-style per-chunk buffering during an in-flight stream.

### Three meanings of “resume”

| Meaning | Who implements it | Devshard today |
|---------|-------------------|----------------|
| **Event replay** — client receives chunks it missed | Gateway or provider (OpenAI Responses) | Partial — full replay after completion |
| **Generation continues server-side** while client is away | OpenAI `background=true`; devshard meta-drain | Yes — protocol/finish, not client UX |
| **Model continues from token N** (same KV session) | vLLM resumable / Realtime; rare in prod chat APIs | No |
| **New request with partial output in prompt** | Universal fallback | Possible, but new inference + cost |

## OpenAI `/v1/responses` — reference for resumable streaming

OpenAI’s **Responses API** is the main first-party design for **durable,
resumable streaming**. It is **not** the same as Chat Completions. Gonka/devshard
and vLLM today use the Chat Completions shape; this section is reference material
for a future devshard design.

Official guides:

- [Streaming API responses](https://developers.openai.com/api/docs/guides/streaming-responses)
- [Background mode](https://developers.openai.com/api/docs/guides/background)
- [WebSocket mode](https://developers.openai.com/api/docs/guides/websocket-mode)
- [Conversation state (`previous_response_id`)](https://developers.openai.com/api/docs/guides/conversation-state)

### Endpoints and roles

| Endpoint | Method | Role |
|----------|--------|------|
| `/v1/responses` | `POST` | Create a response (sync, background, and/or streaming) |
| `/v1/responses/{response_id}` | `GET` | Retrieve status/output; **resume stream** with query params |
| `/v1/responses/{response_id}/cancel` | `POST` | Cancel in-flight background response (idempotent) |
| `wss://…/v1/responses` | WebSocket | Long-lived socket; `response.create` events per turn |

Streaming uses **typed semantic events** (not `ChatCompletionChunk`), e.g.
`response.created`, `response.output_text.delta`, `response.completed`, `error`.
Each event carries a **`sequence_number`** cursor.

### Mode matrix (what supports resume)

| Create flags | Client disconnect | Resume after drop? |
|--------------|-------------------|-------------------|
| `stream=true` only (sync) | Generation typically **stops** when connection closes | **No** — must start a new `POST` |
| `background=true` | Server keeps running; poll `GET` for status | **Poll** final output, not mid-stream replay |
| `background=true` + `stream=true` + `store=true` | Server keeps generating **and** records events | **Yes** — `GET` stream resume (below) |
| WebSocket + `previous_response_id` | 60-minute connection limit; then reconnect | **Next-turn** continuation, not mid-token reattach |

**Limitations** (from OpenAI docs):

- Background streaming resume requires **`store=true`** (stateful / persisted
  response). Stateless / ZDR flows cannot use this replay path.
- Resume is only available if the original request was created with
  **`stream=true`**.
- Synchronous streams: cancel by **terminating the connection** — no
  `starting_after` on that live POST body.
- Background mode retains response data for polling/replay for a **limited
  window** (~10 minutes per background guide); not indefinite archival.

### Background + streaming: create and track cursor

```bash
# 1. Start — server returns immediately and streams events
curl https://api.openai.com/v1/responses \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -d '{
    "model": "gpt-5.5",
    "input": "Write a very long story.",
    "background": true,
    "stream": true
  }'

# Client records response.id and the last seen event.sequence_number
```

While consuming the stream, persist:

- `response_id` (e.g. `resp_123`)
- `cursor` = last processed `sequence_number` from each event

If the TCP/SSE connection drops, the **background job continues** on OpenAI’s
side (unlike normal Chat Completions `stream=true`).

### Resume streaming: `GET` with `starting_after`

```bash
curl -N "https://api.openai.com/v1/responses/resp_123?stream=true&starting_after=42" \
  -H "Authorization: Bearer $OPENAI_API_KEY"
```

Behavior:

- Replays **all events with `sequence_number` > 42**, then continues live if
  the response is still `in_progress`.
- If the response already `completed`, replay ends at the terminal event.
- This is **server-side event log replay**, not “model resumes generating from
  token offset 42.” The model work may have finished while the client was away.

SDK pattern (Python, from OpenAI background guide):

```python
stream = client.responses.create(
    model="gpt-5.5",
    input="Write a very long novel about otters in space.",
    background=True,
    stream=True,
)

cursor = None
for event in stream:
    handle(event)
    cursor = event.sequence_number

# After disconnect: GET resume (SDK stream helper evolving)
# client.responses.stream(resp.id, starting_after=cursor)
```

### Poll without streaming (background only)

```bash
curl https://api.openai.com/v1/responses/resp_123 \
  -H "Authorization: Bearer $OPENAI_API_KEY"
```

Poll while `status` is `queued` or `in_progress`; terminal states expose full
`output` / `output_text`. This is **status polling**, not token-by-token resume.

### Cancel background work

```bash
curl -X POST https://api.openai.com/v1/responses/resp_123/cancel \
  -H "Authorization: Bearer $OPENAI_API_KEY"
```

### WebSocket mode (multi-turn, not mid-stream token resume)

WebSocket mode (`wss://api.openai.com/v1/responses`) optimizes **long tool-call
chains**: each turn sends `response.create` with `previous_response_id` and
only **new** input items. On socket drop:

1. If `store=true` and response ID is valid → reconnect with
   `previous_response_id` + new items.
2. If `store=false` / ZDR / `previous_response_not_found` → new response with
   full input context (or compacted window from `/responses/compact`).

This continues **conversation state across turns**, not “pick up the same
`response.output_text.delta` mid-generation after SSE drop.” For that, use
**background + `starting_after`** above.

### Mapping OpenAI Responses patterns to devshard

| OpenAI Responses concept | Possible devshard analogue |
|--------------------------|---------------------------|
| `background=true` (server keeps working) | `metaDrainTimeout` + decoupled upstream ctx |
| `store=true` + event log | Per-inference chunk buffer + monotonic `sequence_number` |
| `GET ?starting_after=` | New client endpoint: replay buffered SSE from cursor |
| `response_id` | Inference ID + session nonce / sealed lookup key |
| Chat Completions (no resume) | Current vLLM `/v1/chat/completions` path |

A devshard “resume” feature aligned with industry practice would most likely
implement **gateway-level event replay** (Responses-style) or **full replay
from cache** (host `completedResponses` / sealed storage) — not vLLM KV reattach
on the existing chat-completions integration.

## vLLM: streaming input and Realtime API (different problem)

vLLM recently added **streaming input** and a **Realtime WebSocket API**
(`/v1/realtime`). See the Jan 2026 blog post:

[Streaming Requests & Realtime API in vLLM](https://vllm.ai/blog/2026-01-31-streaming-realtime)

That stack is aimed at realtime workloads (audio, incremental input), not at
reconnecting a dropped `/v1/chat/completions` SSE client:

- It uses a **sticky session** with the same internal `request_id`.
- The client sends **new input chunks** over time; the engine **reuses KV
  cache** for finalized tokens and continues from cumulative `prompt_token_ids`.
- It is exposed via engine `StreamingInput` and WebSocket (`/v1/realtime`), not
  as standard OpenAI chat-completions reconnect semantics.

Gonka/devshard today call vLLM through `/v1/chat/completions` with `stream:
true`. Adopting vLLM resumable/streaming-input would be a separate integration
(different API surface, session state, and model requirements), not a drop-in
fix for disconnected proxy clients.

For the standard chat-completions path, vLLM's intended behavior on client
disconnect is to **abort** generation when the HTTP connection closes (free GPU
/KV). There is no per-request token poll endpoint; aggregate metrics live at
`/metrics`.

## What devshardctl does today on disconnect

To follow protocol, `devshardctl` tries to **drain the host response queue**
even after the end user has disconnected:

- `runInference` uses `context.Background()` for the upstream path so client
  `r.Context()` cancellation does not immediately tear down the host
  connection.
- After disconnect (`cancelFlag`), `sendAndProcess` keeps reading host SSE
  until `metaDrainTimeout` (default 10s, overridable via
  `DEVSHARD_META_DRAIN_TIMEOUT_SECONDS`), so `ProcessResponse` can merge
  mempool txs including `MsgFinishInference`.
- If the host finishes execution during that drain, the executor caches the
  full canonical response body in `completedResponses` on the host. A later
  request with the same `StartInference` diff can receive `CachedResponseBody`
  and replay the **entire** response from scratch (not resume from offset).
- Sealed inference content can be persisted to storage and served via lookup
  after RAM eviction — again as a **full** cached response, not incremental
  token polling.

This drain protects **session and chain correctness**; it is the foundation on
which a future "respond later" UX could build (e.g. client reconnects and
receives a full replay), but it does not yet implement mid-stream resume.

## Open questions for a future proposal

1. Should devshard target **Responses-style event replay** (buffer deltas +
   `starting_after` cursor), **full replay** from host/disk cache (simpler), or
   **vLLM Realtime/streaming-input** (model-layer continuation)?
2. If the user disconnects before `metaDrainTimeout` completes, do we still
   want devshardd→vLLM to run to completion (avoid aborting upstream on proxy
   write failure) so more responses land in cache?
3. What client contract signals replay vs resume (`devshard_stream_reset` today
   only covers full replay on retry)? Should we adopt SSE `id:` fields or an
   explicit `devshard_sequence_number` aligned with OpenAI Responses events?
4. Where does the chunk buffer live (proxy only, host, devshardd) and for how
   long (`store=true` on OpenAI is ~10 minutes — what is devshard’s TTL)?
