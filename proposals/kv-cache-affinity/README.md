# Session Affinity for KV-Cache Reuse

## Overview

Inference requests are routed to a pseudo-random serving node on every call. That is correct for fairness and validation sampling, but it discards the KV / prefix cache: when a client's follow-up request in the same conversation lands on a different GPU, that GPU recomputes prefill for the whole shared prefix. For multi-turn chat that recomputation is the dominant latency.

This proposal adds bounded, opt-in session affinity. A client tags a conversation with the OpenAI-standard `prompt_cache_key` request field (fallback `user`), and its follow-up requests are steered back to the same serving GPU. It is a scheduling preference, not a reservation, and it is bounded so no node can be pinned to one client indefinitely.

---

## Motivation

Routing is two hops, and the cache lives at the far end of the second one.

1. **Gateway to participant.** The devshard gateway binds each inference nonce to a participant as `nonce mod group_size`, cycling over the escrow's slot set. The spread keeps validation sampling representative and stops any host from steering all of a client's traffic to itself.
2. **Participant to mlnode.** A participant runs a pool of mlnodes; its broker hands each inference to the least-busy node for the model, with no session awareness.

The cache is per-GPU, so a client that returns to the same participant can still miss it. Reuse end to end requires landing on the same participant at hop 1 and the same mlnode at hop 2.

A shared prefix cache is also a timing side channel: a client co-located on a GPU can learn from its own reported cached-token count that some prefix is already resident, and recover another tenant's prompt piece by piece. Any design that concentrates one client's traffic on one GPU must also say which requests may share cached blocks.

---

## Goals

1. Reuse a warm prefix cache across the turns of one conversation, at both hops.
2. Require no client adaptation beyond a standard OpenAI request field.
3. Leave the nonce-to-host binding, the signed diff chain, payment, and validation untouched.
4. Bound how long any session may occupy one node.
5. Give cached blocks an explicit sharing scope.
6. Stay off until an operator turns it on, independently at the gateway and at each participant.

## Non-Goals

1. Trusting, verifying, or rewarding any node-reported cache hit.
2. A global cache index or cross-node cache sharing.
3. Any change to reward accounting or settlement.
4. Isolating clients that present no credential — see [Consensus and Security Analysis](#consensus-and-security-analysis).
5. Extending cache scoping to the validation path.

---

## Design

### Hop 1 — gateway to participant

A session's primary attempt carries its remembered participant into the gateway's request picker as a preference. The picker honours it only while doing so is free: the remembered participant can serve the request now, and the request has not waited past the picker's staleness threshold. Otherwise the picker takes the next compatible nonce, as it does today.

The preference reorders which queued request meets an upcoming nonce. It never holds a nonce back, so none is burned and no request waits on a participant that is proof-of-compute gated, throttled, or idle. Speculative secondary attempts keep their normal other-host routing.

### Hop 2 — participant to mlnode

The acquire request a participant's engine makes on its own broker gains an optional session identifier. When set, the broker prefers the mlnode this session last used, provided that node is not being skipped and is available for the model; otherwise it falls through to least-busy selection, and records where the session landed.

The preference is bounded by load as well as by count and time. A node stays available until it reaches its own concurrency limit, so availability alone would hold a session on a node one request short of saturation while a peer idles. The preference therefore applies only while the remembered node's in-flight count exceeds the least-busy candidate's by no more than a fixed margin, currently two. Past the margin the least-busy node serves and pays one cold prefill.

A session served away from its remembered node is rebound to the node that served it, restarting its request budget and lifetime there.

### Cache namespace

The serving node stamps each request with a cache namespace derived from its own escrow identifier and the session identifier, and the serving engine shares cached blocks only between requests carrying the same namespace.

The session identifier is not the client's own string. The gateway derives it as a keyed hash over the escrow identifier, the caller's credential, and the client's string, under a secret drawn at gateway start and never persisted. Three properties follow: the client's string never leaves the gateway; guessing another client's string does not reach that client's namespace, because the caller's own credential is folded in; and the identifier cannot be recomputed off-box.

The three parts are joined with a NUL separator. The join is unambiguous: escrow identifiers are chain-assigned and NUL-free, an HTTP header value cannot contain a NUL, and the only field that could is last.

The namespace stamp is applied whenever a request carries a session identifier, independently of whether that participant enabled mlnode stickiness. Stickiness changes a participant's own GPU scheduling and is its choice; narrowing which requests may share cached blocks is not, because the client it protects is not the one making the choice.

### Bounds

A session sticks to a node for at most a fixed number of requests or a fixed wall-clock lifetime, whichever comes first; then the binding is evicted and the session re-randomises. Bindings live in a fixed-capacity map with a lazy expiry-and-eviction sweep, so an untrusted stream of distinct session keys cannot grow memory without bound.

### Request flow

```text
request (body carries prompt_cache_key, or user as fallback)
 |
 +-- GATEWAY
 |     no key ---------------------> unchanged behaviour, nothing below happens
 |     key unseen or past bound ---> nonce mod group_size -> participant P
 |     key within bound -----------> prefer P for the primary attempt
 |                                   (P busy, or request gone stale -> next compatible nonce)
 |
 +-- session id = keyed hash of (escrow, caller credential, client string)
       |
       +-- PARTICIPANT P
             session id unseen ----> least-busy GPU -> node G
             session id sticky ----> prefer node G, unless it is past the load margin
                    |
                    +-- cache namespace = hash of (P's own escrow id, session id)
                          |
                          +-- first time on G ------> cold prefill, warms the cache
                          +-- same prefix again ----> prefix hit, prefill skipped
```

---

## User-Facing Semantics

A client opts in by sending `prompt_cache_key` on the requests of one conversation; `user` is honoured as a fallback. Both must be top-level fields, and both are type-checked and length-capped at the gateway boundary, so a malformed or oversized value is rejected rather than forwarded.

The key selects a cache namespace and a routing preference, and carries no guarantee: a request whose preferred node is busy is served elsewhere, and a conversation longer than the request bound re-randomises mid-way. Clients see this as latency variance, never as an error.

Restarting the gateway draws a new secret, invalidating every live binding and namespace. Correctness is unaffected; the cost is the discarded cache.

---

## Consensus and Security Analysis

**Nothing verified changes.** The nonce-to-host binding, the signed diff chain, and host catch-up are untouched. Hop 1 only reorders which queued request the gateway matches to an upcoming nonce — the same gateway-local scheduling the picker already performs. Hop 2 is internal load balancing on a participant's own GPUs, invisible to the chain. The namespace stamp is applied by the serving node after the payload is verified, is output-invariant, and never enters the committed prompt.

**The session identifier stays out of consensus** — not in the signed diff, not in the verified payload, not in the state root — but it reaches every participant that serves or verifies the request, inside the request body, and is covered by the transport request signature.

**Steering cannot dodge validation.** Sampling draws on the inference identifier and slot shares with the executor's own slots removed from the denominator, holding the expected validations per inference at the escrow's configured rate regardless of who executed.

**Concentration earns nothing.** A buyer pays its own funds for work performed, and a participant's weight comes from proof of compute, not inference volume. Steering one's own primaries yields nothing beyond the natural one-in-`group_size` share. The bounds are load-spread and cache-economics parameters; they carry no safety role.

**What the namespace does not isolate.** Four limits are inherent to the design.

Open access mode authenticates nobody, so two anonymous callers of one escrow sending the same client string share one namespace. The escrow component still separates them from every other escrow, but isolation between clients of one escrow requires authenticated access with a distinct credential per client.

Validation replays the original prompt without a session, so a sampled inference's prefix lands in the shared namespace on the validator's GPU. Isolation is thus probabilistic at the validation sampling rate. Namespacing that path is out of scope: it would put a serving-engine-version-dependent field on the consensus path for escrows that never opted in.

The gateway's own response cache sits above this boundary. Its key is the model, the client's response intent, and the normalized request body — from which `prompt_cache_key` has already been stripped — so two callers sending an identical prompt are served one cached completion regardless of session or credential. The namespace governs which requests share cached blocks on a GPU, not which share a completed answer at the gateway.

The session identifier is stable for the gateway process's life and identical for every participant that touches the request, so a participant can link one end-user's requests across models. Removing this needs a per-destination derivation, which the timeout path's broadcast currently blocks.

---

## Tunable Parameters

Set per gateway and per participant by the operator, not by governance: these affect one operator's own scheduling and memory, never chain state.

| Parameter | Default | Description |
|---|---|---|
| Gateway affinity enabled | off | Master switch. With it off no session identifier is derived, so neither hop-1 stickiness nor the namespace happens. |
| Gateway max requests | 32 | Consecutive requests before a binding is dropped. |
| Gateway binding lifetime | 120 s | Wall-clock lifetime of a binding. |
| Gateway map capacity | 50 000 | Binding-map size cap. |
| Participant stickiness enabled | off | Governs mlnode stickiness only; the namespace does not depend on it. Both the participant's engine and its broker read this flag. |
| Participant max requests | 64 | As above, at hop 2. |
| Participant binding lifetime | 600 s | As above, at hop 2. |
| Participant map capacity | 50 000 | As above, at hop 2. |

The hop-2 load margin is a constant, not an operator tunable; sizing it needs the same cache-residency measurement as the lifetimes.

**Sizing the request bound.** The warm-hit share inside a binding is `(N-1)/N`, so 32 buys 97% against 90% at 10. What limits raising it is key granularity: with `prompt_cache_key` a key is one conversation and a high bound only raises variance in per-host load, but with `user` one tenant is one key, and the bound is what stops a whole application from occupying one host. The default is chosen for that second case.

**Sizing the lifetime.** A binding is useful only while the serving engine still holds the blocks, which depends on cache capacity over token ingress on that GPU — not knowable in advance. The measurement that settles it: replay one prefix with a growing idle gap and find where the reported cached-token count falls to zero. **The shipped defaults are unmeasured round numbers.**

**Sizing the map.** Capacity must exceed the sessions live within one lifetime — roughly requests per second times lifetime divided by requests per session; at 100 rps, 120 s and 10 requests per session, about 1200. An entry costs roughly 275 bytes, so a full map is about 14 MB per escrow.

---

## Observability

An operator turning this on must be able to answer, without reading logs, whether stickiness is happening, whether the preference is yielded more often than honoured, and whether the binding map is nearing its cap. Hop 2 needs the same split separately, because hop 1 can work perfectly while hop 2 lands the session on a different GPU. Cache effectiveness itself is reported by the serving engine as a cached-token count per response.

Hop 2 reports two distinct yield outcomes: the node is gone or unavailable, or the node is healthy but past the load margin. The first indicates a fleet problem; the second is the margin operating as specified, and is the only signal available for sizing it.

---

## Future Considerations

* Replace hop-2 stickiness with a shared KV pool inside one participant, so a warm prefix stops being the property of the single GPU that computed it. Two conditions gate it: the cache namespace must be carried into the pool's own key, or isolation that now ends at one GPU is lost across every instance reading the pool; and the pool must stay inside a participant, since sharing one across participants is a trust boundary the network does not have.
* Derive the session identifier per destination participant, removing cross-participant linkability, once the timeout path no longer broadcasts one payload to many verifiers.
* Fold the session identifier into the gateway response cache key, closing the last sharing path above the namespace.
* Replace the lifetime and load-margin defaults with values derived from measured cache residency.
