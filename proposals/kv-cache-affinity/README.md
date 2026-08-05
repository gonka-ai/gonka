# Proposal: Session Affinity for KV-Cache Reuse

## Summary

Inference requests are routed to a pseudo-random node on every call. That is correct for fairness and validation, but it throws away the most valuable piece of serving state: the **KV / prefix cache**. A serving node (vLLM) keeps, per GPU, the attention key/value tensors for prefixes it has already processed. When a client's follow-up request in the same conversation lands on a *different* node, that node must recompute prefill for the whole shared prefix (system prompt + chat history) from cold — which, for multi-turn chat, is the dominant latency.

This proposal adds **bounded, opt-in session affinity**: a client that tags a conversation with the OpenAI-standard `prompt_cache_key` request field (fallback `user`) has its follow-up requests steered back to the same serving GPU, so the warm cache is reused. It is a pure scheduling hint — it does **not** change the nonce→host binding, the signed diff chain, payment, or validation — and it is bounded so no node can be pinned to a client indefinitely.

Non-goals: this does not trust, verify, or reward any node-reported cache hit; it does not add a global cache index or cross-node cache sharing; it does not change reward accounting.

## Problem

Routing is two hops, and the cache lives at the far end of the second one:

1. **Gateway → participant.** The devshard gateway (`devshardctl`) binds each inference nonce to a participant deterministically, `hostIdx = nonce % len(group)`, cycling round-robin over the escrow's slot set. This spread is deliberate: it keeps validation sampling representative and stops any single host from steering all of a client's traffic to itself.
2. **Participant → mlnode.** A participant runs a *pool* of mlnodes (GPUs). Its broker (`decentralized-api`, `broker.getLeastBusyNode`) hands each inference to the lowest-`LockCount` node for the model, with no session awareness.

The KV cache is **per-mlnode (per-GPU)**. So even a client that happened to return to the same participant can still be dispatched to a different GPU and miss the warm cache. To reuse the cache end-to-end, the same session must land on the same participant (hop 1) *and* the same mlnode within it (hop 2).

## Proposed Solution

Give each client session an opaque key (the OpenAI-standard `prompt_cache_key`, fallback `user`, read from the request body — no gonka-specific header, so standard OpenAI clients need no adaptation) and steer that session back to its last-used serving node at both hops, for a **bounded** window, then re-randomise.

**Hop 1 — gateway → participant (`devshardctl`).** A session's **primary** attempt carries its sticky participant as a *preference* into the session picker, which honors it only while doing so is free: the sticky participant can serve the request now, and the request has not waited past `pickerStaleThreshold` (200ms). Otherwise the picker takes the next compatible nonce as it always did. The preference reorders which queued request meets an upcoming nonce; it never holds a nonce back, so nothing is burned and nothing waits on a participant that is PoC-gated, throttled, or idle. Speculative/redundant **secondary** attempts keep their normal other-host routing, so latency racing and cross-host validation are unaffected.

**Hop 2 — participant → mlnode (`decentralized-api` broker).** `AcquireMLNodeRequest` gains an optional `session_id`. When set, `getLeastBusyNode` prefers the mlnode this session last used, if it is not in the skip set and is currently available for the model; otherwise it falls through to the existing least-busy selection. `lockAvailableNode` records which node the session landed on.

**Bound (both hops).** A session sticks to a node for at most `MaxRequests` requests or `TTL` wall-clock, whichever comes first; then the binding is evicted and the session re-randomises. Bindings are held in a fixed-cap map (`MaxEntries`) with a lazy expiry+eviction sweep, so an untrusted stream of distinct session keys cannot grow memory without bound.

## How it works (request flow)

A single request, from the client body to vLLM and back. Both routing decisions are the same shape: no session key (or expired/over-bound) → today's behaviour; sticky key within its bound → steer back to the warm node.

```text
request  (body: prompt_cache_key = "conv-42", fallback "user")
 |
 +--> GATEWAY (devshardctl):  key = affinityKeyFromDocument(document)   # read before prompt_cache_key is stripped from the body
 |     |
 |     +--> no key ------------------------------> feature inert: behaves exactly as today
 |     |
 |     +--> HOP 1  gateway -> participant
 |           |
 |           +--> key unseen / expired ----------> nonce % len(group) ------> participant P (random)
 |           +--> key sticky, within bound ------> prefer same participant P for the primary
 |                  (bound = MaxRequests | TTL; P busy or request aged 200ms -> next compatible nonce)
 |
 +--> session_id = HMAC(process secret, escrow | credential | key)   (rides the JSON wire to P; NOT in the signed payload)
       |
       +--> PARTICIPANT P (decentralized-api):  HOP 2  participant -> mlnode  (broker.getLeastBusyNode)
             |
             +--> session_id unseen ------------> least-busy GPU (lowest LockCount) ----> node G
             +--> session_id sticky ------------> prefer same GPU G (if free for model) -> node G
                    |
                    +--> ENGINE, just before the vLLM call:
                    |      body = withCacheSalt(body, escrow_id, session_id)   # cache_salt = sha256(escrow_id | session_id)
                    |
                    +--> vLLM prefix cache on GPU G, keyed by (prompt tokens + cache_salt)
                           |
                           +--> first time on G ---------> cold prefill, warms the cache
                           +--> repeat same prefix ------> PREFIX HIT: skip prefill of the shared prefix
                                  |
                                  +--> response streams back the SAME path, raw byte passthrough
                                        usage.prompt_tokens_details.cached_tokens ------> client
```

Why the second turn is fast — same conversation, same warm GPU:

```text
req #1  conv-42   -> P, GPU G  -> cold prefill of the 10k-token prefix   (cached_tokens = 0)
req #2  conv-42   -> P, GPU G  -> PREFIX HIT on that 10k prefix          (cached_tokens ~= 10k, low latency)
req #3  (no key)  -> nonce%len -> random participant / GPU               (unchanged behaviour)
```

`cache_salt` is what scopes reuse: the salt is `sha256(escrow_id | session_id)`, and `session_id` is itself `HMAC(process secret, escrow_id | caller credential | client string)` derived at the gateway, so KV blocks never collide across escrows, across API keys, or with a guessed client string. What the salt cannot give is isolation between two anonymous callers of the same escrow on a model whose `access_mode` is `open`: there is no credential to fold in, so the same client string is the same client as far as the gateway can tell. Isolation between clients of one escrow therefore requires `access_mode: api_key` with a distinct key per client — see [Consensus & Security Analysis](#consensus--security-analysis).

## Consensus & Security Analysis

**Fits consensus — nothing verified changes.** `nonce % len(group)`, the signed diff chain, and host catch-up are untouched; hosts verify the exact same nonce sequence they do today. Hop 1 only reorders *which queued request* the gateway matches to an upcoming nonce — the same class of gateway-local scheduling the picker already performs with its exclude set — and is implemented by reusing that machinery, so no new dispatch or verification path is introduced. Hop 2 is internal load-balancing on a participant's own GPUs, invisible to the chain. The `session_id` stays out of consensus: it is not in the signed diff, not in the payload `VerifyPayload` checks, and not in the state root. It is sent — to every participant that serves or verifies the request, inside the JSON body, which `transport.SignRequest` signs as a whole, so it is covered by the transport request signature even though nothing on the chain sees it.

**No self-dealing / validation-evasion (hop 1).** Steering the primary cannot dodge validation at all, with or without the bound: `ShouldValidate` (`devshard/state/validation.go`) draws on `deterministicHash(seed, inferenceID)` and folds the executor in only as `totalSlots - executorSlotCount` in the denominator, which holds the expected number of validations per inference at exactly the escrow's rate. A pinned session's inferences are therefore sampled like any other, by validators chosen the same way. Nor is concentration worth anything: the buyer pays its own funds for work actually performed, and a participant's weight comes from PoC, not from inference volume. Only the primary attempt is steered; secondaries still cross-check other hosts. The `MaxRequests` / `TTL` bounds are therefore load-spread and cache-economics parameters, not safety ones — see [Parameters](#parameters). And routing never affects payment — a host is paid only for the work it actually performs on funds the buyer supplied — so concentrating one's own bounded primaries earns nothing on top of the ~1/`groupSize` natural self-routing that already exists. No cache hit is trusted or rewarded.

**No new attack surface (hop 2).** These are one participant's own GPUs; steering a session among them cannot self-deal or dodge validation. The bound here exists purely so load can rebalance and a departed/rebalanced node is not chased.

**DoS / memory.** `prompt_cache_key` and `user` are both type-checked and length-capped at the gateway boundary (`PromptCacheKeyMaxLen` / `UserMaxLen`, 512 bytes) before extraction, so a non-string or oversized value gets a 400 instead of being forwarded. Whichever string survives is then collapsed by `deriveSessionToken` into a fixed 64-character HMAC-SHA256 digest, so every entry in both binding maps has the same width regardless of what the client sent — the 512-byte cap is a courtesy to the caller, not what actually bounds memory. Both maps are additionally size-capped (`MaxEntries`) with an eviction sweep, and creating a binding costs the client a real, funded inference, so growth is cost-bounded on top of the fixed width.

**What the salt does not isolate.** A model served with `access_mode: "open"` authenticates nobody, so two anonymous callers of one escrow sending the same `user` value are indistinguishable to the gateway and share one namespace — by construction, not by oversight. The escrow component still separates them from every other escrow, and the process secret still keeps the wire token from being recomputed off-box, but within-escrow isolation is only achievable under `access_mode: "api_key"` with one key per client. Anything stronger would need a per-caller identity the gateway does not have.

**Validation replays leave the namespace.** The salt scopes the executor's GPU only. When an inference is sampled for validation, the validator replays the same prompt with no session and therefore no salt, so the victim's prefix lands in the shared, unsalted namespace on the validator's GPU, where any co-located client can probe it with the same `cached_tokens` oracle this feature exists to close. Isolation is therefore probabilistic, not absolute: a given inference is exposed with the probability that it is sampled for validation. The alternative — salting the validation path — would put a vLLM-version-dependent field on the consensus path for escrows that never opted into affinity, which is the worse trade.

**One session token, every participant that touches the request.** The token is a stable function of (process secret, escrow, credential, client string); the `MaxRequests`/`TTL` bounds rotate the routing *binding*, not the identifier, which lives as long as the gateway process. The primary, every speculative secondary, and every timeout verifier receive the same value, so a participant can link two requests of one end-user — across models, and even when it only ever saw a losing speculative attempt. Folding the destination participant's key into the derivation would remove the cross-participant part of this; it is not done today because the timeout path broadcasts one payload to many verifiers.

**`prompt_cache_key` counts only at the top level.** The key is lifted from the parsed document before the `extra_body` envelope is unwrapped, so a client that passes `prompt_cache_key` inside `extra_body` gets no affinity and no salt — it silently falls back to `user`, or to nothing.

**Gateway restart rotates the secret.** `sessionTokenSecret` is drawn once per process from `crypto/rand` and never persisted (`gateway.go`), so restarting `devshardctl` invalidates every live hop-1 binding and every executor `cache_salt` derived from it — the next request under a given client key hashes to a new session id and starts in a cold cache namespace. This does not weaken anything (a rotated secret cannot leak the one it replaced), but it is a warm-cache reset from the caller's point of view, and an operator restarting the gateway should expect the next round of requests to run cold. A participant's own hop-2 bindings are unaffected by a gateway restart and age out on their own TTL/MaxRequests bound.

## Implementation Status

- **Hop 1 (gateway, `devshardctl`): implemented.** `affinity.go` (bounded tracker, `affinityKeyFromDocument`, `deriveSessionToken`). `prompt_cache_key`/`user` are read from the parsed request document into `chatRequest.AffinityKey` inside `ChatRequestPipeline.Normalize` (`request_filters.go`), before the `PreValidation` stage strips `prompt_cache_key` off the wire body; `proxy.go` then derives the HMAC session token (`deriveSessionToken`) into `user.InferenceParams.AffinityKey`. Primary steering happens in `redundancy.go` (`preparePrimaryWithAffinity`). OFF by default; enable via `DEVSHARD_AFFINITY_ENABLED`. Unit-tested (bounded stickiness, TTL, rotation eviction, map cap, disabled/empty no-ops).
- **Hop 2 (participant, `decentralized-api` broker): implemented, server side.** `session_id` on `AcquireMLNodeRequest`; broker preference + recording in `broker.go`; bounded tracker in `broker/session_affinity.go`; forwarded by the nodemanager gRPC server. Backward compatible — old clients send no `session_id` and get today's least-busy behaviour. Unit-tested.
- **cache_salt (cache isolation) — implemented.** The devshardd engine injects vLLM's per-request `cache_salt = sha256(escrow id | session id)` **host-side, immediately before the vLLM call** (`withCacheSalt`), NOT in the gateway's signed-prompt pipeline. The escrow comes from the participant's own state, never from the wire, so a gateway cannot merge two escrows' namespaces. The salt is applied whenever a request carries a non-empty session id, independently of `DAPI_MLNODE_AFFINITY_ENABLED` — that flag only gates mlnode *stickiness*, because stickiness is what changes the participant's own GPU scheduling, while cache isolation is not something a participant is offered a choice to opt out of (`devshard/cmd/devshardd/inference/engine.go:106-122`). `cache_salt` enters vLLM's KV-block hash, so it narrows reuse to same-salt requests on the executor's GPU; it does not by itself close any wider timing side-channel. The validator deliberately replays the same prompt with no session and therefore no salt (`validator.go`), so validation traffic stays in the shared, unsalted namespace on the validator's GPU — putting a vLLM-version-dependent field on the validation path for escrows that never opted into affinity was judged not worth it, and the cost of that choice is a warmer, but not isolated, cache on the validator side. It is output-invariant (cache namespacing only), so the committed/signed prompt and executor↔validator validation are unaffected (`VerifyPayload` ignores it). Different escrows, and different sessions within one escrow, get different salts and share no KV blocks. Unit-tested.
- **Session-id propagation — implemented.** The affinity key flows gateway → host → broker/engine: `SendOnly` sets it on `host.InferencePayload.SessionID`, carried over the JSON wire (`transport.PayloadJSON`) into `devshard.ExecuteRequest.SessionID`, threaded through the devshardd engine (`executeMLRequest` / `doWithLockedNode`) into `Client.Acquire` → `AcquireMLNodeRequest.session_id` (feeds hop-2 mlnode stickiness) and into the `cache_salt` injection. Every path that re-sends the same prompt carries the same field, so a retry never falls back into the shared namespace: `host.go` sets it on both execution jobs it builds (`signReceipt` and `challengeReceiptLocked`), and the gateway's timeout path builds its host payload through one `timeoutPayload` constructor rather than two literals that could drift apart again. It is not part of what consensus verifies (`VerifyPayload` ignores it, and it never enters a diff or the state root), though it does ride inside the HTTP body that `transport.SignRequest` signs. With no client key, nothing happens on either hop — no session id, no stickiness, no salt, exactly today's behaviour. With a client key, the executor salt always applies; stickiness at hop 1 and hop 2 are each gated by their own flag (`DEVSHARD_AFFINITY_ENABLED`, `DAPI_MLNODE_AFFINITY_ENABLED`).

## Parameters

Gateway (hop 1), env:

| Var | Default | Meaning |
|---|---|---|
| `DEVSHARD_AFFINITY_ENABLED` | off | without it the gateway never derives a session id at all, so neither hop-1 stickiness nor the executor's `cache_salt` happens for any request on this devshard |
| `DEVSHARD_AFFINITY_MAX_REQUESTS` | 32 | consecutive requests before re-randomise; the warm-hit share inside a binding is `(N-1)/N`, so 32 buys 97% against 90% at 10 |
| `DEVSHARD_AFFINITY_TTL_MS` | 120000 | wall-clock lifetime of a binding; **not measured** — see the sizing note below |
| `DEVSHARD_AFFINITY_MAX_ENTRIES` | 50000 | binding-map size cap |

Participant broker (hop 2), env:

| Var | Default | Meaning |
|---|---|---|
| `DAPI_MLNODE_AFFINITY_ENABLED` | off | mlnode-stickiness switch; the executor's `cache_salt` injection does not depend on it (see cache_salt above) |
| `DAPI_MLNODE_AFFINITY_MAX_REQUESTS` | 64 | consecutive requests before re-randomise |
| `DAPI_MLNODE_AFFINITY_TTL_MS` | 600000 | wall-clock lifetime of a binding; **not measured**, and capped in practice by hop 1's own bounds |
| `DAPI_MLNODE_AFFINITY_MAX_ENTRIES` | 50000 | binding-map size cap |

**Sizing these.** `MaxRequests` trades warm-hit share against how long one key monopolises a host. With `prompt_cache_key` the key is one conversation, so keys are many, the first landing is still `nonce % len(group)`, and a high bound only raises variance — not any host's expected share. With `user` as the key, one tenant is one key, and the bound is what stops a whole application from sitting on one host; 32 is chosen for that case, not for the first. Hop 2's larger bound is mostly unreachable: once hop 1 re-randomises, the session moves to a different participant with its own map, and the old binding ages out.

`TTL` should equal how long vLLM actually keeps the blocks, which depends on cache capacity `(GPU memory x gpu-memory-utilization - weights) / bytes-per-token` (halved by `--kv-cache-dtype fp8`) divided by the token ingress rate on that GPU — none of which is knowable from this repo. Measure it directly instead: replay one prefix with a growing idle gap and find where `usage.prompt_tokens_details.cached_tokens` falls to zero. Above that point the binding routes to a cold cache and only delays re-balancing; below it, reuse is discarded. The current values are round numbers, not measurements.

`MaxEntries` must exceed the sessions live within one TTL, roughly `requests_per_second x TTL / requests_per_session`; at 100 rps, 120s and 10 requests per session that is ~1200. The cap costs about 275 bytes per entry, so a full map is ~14 MB per escrow runtime and multiplies by the number of escrows a gateway serves.

`DAPI_MLNODE_AFFINITY_ENABLED` is read independently by two processes on the participant side: devshardd (`cmd/devshardd/config.go`, gates whether the engine forwards a session id to the broker for stickiness at all) and the `decentralized-api` broker (`broker/session_affinity.go`, gates whether it honors a session id it receives). Both must have the flag set for hop-2 stickiness to actually happen; setting it on only one side leaves hop 2 at today's least-busy behaviour.

## Observability

`devshard_gateway_affinity_decision_total{devshard_id,decision}` (`devshard/cmd/devshardctl/metrics.go`) answers whether hop-1 stickiness is doing anything at all: `decision` is `hit` when the primary landed on the sticky participant, `yielded` when a sticky preference existed but the picker served someone else (the sticky participant was busy or the request aged past `pickerStaleThreshold`), or `miss` when no sticky preference existed for that request. `devshard_gateway_affinity_bindings{devshard_id}` is a gauge of the current hop-1 binding-map size, for watching `MaxEntries` pressure. `decentralized_api_mlnode_affinity_decision_total{decision,model}` (`decentralized-api/observability/metrics_prometheus.go`) is the hop-2 equivalent — same `hit`/`yielded`/`miss` decisions, answering whether a participant's own broker is actually landing sessions back on the same GPU.

## Files

- `devshard/cmd/devshardctl/affinity.go` (new) — gateway session tracker, `affinityKeyFromDocument`, `deriveSessionToken`.
- `devshard/cmd/devshardctl/{proxy.go,redundancy.go,gateway.go}` — session-token derivation, primary steering, runtime wiring (session secret, affinity tracker).
- `devshard/cmd/devshardctl/{request_filters.go,request_filters_parameters.go,request_filters_config.go}` — lift `prompt_cache_key`/`user` into `chatRequest.AffinityKey` before PreValidation strips the wire field.
- `devshard/cmd/devshardctl/session_picker.go` — sticky-participant preference in nonce dispatch (`pickerRequest.stickyParticipant`, `activeSticky`).
- `devshard/cmd/devshardctl/metrics.go` — hop-1 affinity-decision counter and binding-count gauge.
- `devshard/user/session.go` — `InferenceParams.AffinityKey`.
- `devshard/host/host.go` — session id carried into the execution job on both the normal receipt path and the receipt-challenge path.
- `devshard/types.go`, `devshard/transport/types.go` — `SessionID` on the wire (`ExecuteRequest`, `PayloadJSON`).
- `devshard/cmd/devshardd/inference/engine.go` — `cache_salt` injection (`withCacheSalt`), applied independently of the participant's own affinity flag.
- `devshard/cmd/devshardd/config.go` — participant-side `DAPI_MLNODE_AFFINITY_ENABLED` flag.
- `common/nodemanager/nodemanager.proto` — `AcquireMLNodeRequest.session_id`.
- `decentralized-api/broker/session_affinity.go` (new) — broker mlnode tracker.
- `decentralized-api/broker/{broker.go,commands.go,node_lock.go}` — mlnode preference.
- `decentralized-api/nodemanager/server.go` — forward `session_id` to broker.
- `decentralized-api/observability/metrics_prometheus.go` — hop-2 affinity-decision counter.
