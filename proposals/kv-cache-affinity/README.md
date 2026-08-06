# Session Affinity for KV-Cache Reuse

## Overview

Inference requests are routed to a pseudo-random serving node on every call. That is correct for fairness and validation sampling, but it discards the most valuable piece of serving state: the KV / prefix cache. A serving node keeps, per GPU, the attention key/value tensors for prefixes it has already processed. When a client's follow-up request in the same conversation lands on a different node, that node recomputes prefill for the whole shared prefix — system prompt plus chat history — from cold. For multi-turn chat that recomputation is the dominant latency.

This proposal adds bounded, opt-in session affinity: a client that tags a conversation with the OpenAI-standard `prompt_cache_key` request field (fallback `user`) has its follow-up requests steered back to the same serving GPU, so the warm cache is reused. It is a scheduling preference, not a reservation, and it is bounded so no node can be pinned to one client indefinitely.

---

## Motivation

Routing is two hops, and the cache lives at the far end of the second one.

1. **Gateway to participant.** The devshard gateway binds each inference nonce to a participant deterministically as `nonce mod group_size`, cycling over the escrow's slot set. This spread is deliberate: it keeps validation sampling representative and stops any single host from steering all of a client's traffic to itself.
2. **Participant to mlnode.** A participant runs a pool of mlnodes. Its broker hands each inference to the least-busy node for the model, with no session awareness.

The KV cache is per-GPU. A client that happens to return to the same participant can still be dispatched to a different GPU and miss the warm cache. To reuse the cache end to end, a session must land on the same participant at hop 1 and the same mlnode at hop 2.

A second problem arrives with the first. A shared prefix cache is a timing side channel: a client co-located on a GPU can learn from its own reported cached-token count that some prefix is already resident, and recover another tenant's prompt piece by piece. Any design that deliberately concentrates one client's traffic on one GPU must also say which requests are allowed to share cached blocks.

---

## Goals

1. Reuse a warm prefix cache across the turns of one conversation, at both routing hops.
2. Require no client adaptation beyond a standard OpenAI request field.
3. Leave the nonce-to-host binding, the signed diff chain, payment, and validation untouched.
4. Bound how long any session may occupy one node, so routing re-randomises on its own.
5. Give cached blocks an explicit sharing scope, so concentrating traffic does not widen the existing timing side channel.
6. Stay off until an operator turns it on, independently at the gateway and at each participant.

---

## Non-Goals

1. This proposal does not trust, verify, or reward any node-reported cache hit.
2. This proposal does not add a global cache index or cross-node cache sharing.
3. This proposal does not change reward accounting or settlement.
4. This proposal does not isolate clients that present no credential; see [Consensus and Security Analysis](#consensus-and-security-analysis).
5. This proposal does not extend cache scoping to the validation path.

---

## High-Level Design

Each client session gets an opaque key, taken from the request body so that standard OpenAI clients need no gonka-specific header. That key steers the session back to its last-used serving node at both hops for a bounded window, then the binding is dropped and routing re-randomises.

### Hop 1 — gateway to participant

A session's primary attempt carries its remembered participant into the gateway's request picker as a preference. The picker honours it only while doing so costs nothing: the remembered participant can serve the request now, and the request has not been waiting past the picker's staleness threshold. Otherwise the picker takes the next compatible nonce, exactly as it does today.

The preference reorders which queued request meets an upcoming nonce. It never holds a nonce back, so no nonce is burned and no request waits on a participant that is proof-of-compute gated, throttled, or idle. Speculative secondary attempts keep their normal other-host routing, so latency racing and cross-host verification are unaffected.

### Hop 2 — participant to mlnode

The acquire request a participant's engine makes on its own broker gains an optional session identifier. When set, the broker prefers the mlnode this session last used, provided that node is not being skipped and is currently available for the model; otherwise it falls through to the existing least-busy selection, and records where the session actually landed.

### Cache namespace

Steering alone would concentrate a client's traffic without saying who may reuse its cached blocks. The serving node therefore stamps each request with a cache namespace derived from its own escrow identifier and the session identifier, and the serving engine shares cached blocks only between requests carrying the same namespace.

The session identifier is not the client's own string. The gateway derives it as a keyed hash over the escrow identifier, the credential the caller presented, and the client's string, under a secret drawn at gateway start and never persisted. Three properties follow. The client's string never leaves the gateway. Guessing another client's string does not reach that client's namespace, because the caller's own credential is folded in. And the identifier cannot be recomputed off-box, because the secret is not derivable from anything on the wire.

The namespace stamp is applied whenever a request carries a session identifier, independently of whether that participant enabled mlnode stickiness. Stickiness changes a participant's own GPU scheduling and is therefore its choice; narrowing which requests may share cached blocks is not something a participant is offered a choice to waive, because the client whose data it protects is not the one making the choice.

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
             session id sticky ----> prefer the same node G, if free for the model
                    |
                    +-- cache namespace = hash of (P's own escrow id, session id)
                          |
                          +-- first time on G ------> cold prefill, warms the cache
                          +-- same prefix again ----> prefix hit, prefill skipped
```

---

## User-Facing Semantics

A client opts in by sending `prompt_cache_key` on the requests of one conversation; `user` is honoured as a fallback for clients that send only that. The field must be a top-level request field. Both are type-checked and length-capped at the gateway boundary, so a malformed or oversized value is rejected rather than forwarded.

The key selects a cache namespace and a routing preference. It carries no guarantee: a request whose preferred node is busy is served elsewhere, and a conversation longer than the request bound re-randomises mid-way. Clients see this only as latency variance, never as an error.

Restarting the gateway draws a new secret, so every live binding and every namespace derived from it is invalidated and the next round of requests runs cold. This costs a warm cache, not correctness.

---

## Consensus and Security Analysis

**Nothing verified changes.** The nonce-to-host binding, the signed diff chain, and host catch-up are untouched; hosts verify the same nonce sequence they do today. Hop 1 only reorders which queued request the gateway matches to an upcoming nonce — the same class of gateway-local scheduling the picker already performs. Hop 2 is internal load balancing on a participant's own GPUs, invisible to the chain. The cache namespace stamp is applied by the serving node after the payload is verified, is output-invariant, and never enters the committed prompt.

**The session identifier stays out of consensus** — not in the signed diff, not in the verified payload, not in the state root — but it is sent to every participant that serves or verifies the request, inside the request body, and is therefore covered by the transport request signature.

**Steering cannot dodge validation.** Validation sampling draws on the inference identifier and slot shares, with the executor's own slots removed from the denominator, which holds the expected number of validations per inference at the escrow's configured rate no matter who executed it. A steered session's inferences are sampled like any other, by validators chosen the same way.

**Concentration is not worth anything.** A buyer pays its own funds for work actually performed, and a participant's weight comes from proof of compute, not from inference volume. Steering one's own primaries earns nothing beyond the natural one-in-`group_size` share. No cache hit is trusted or rewarded. The bounds are therefore load-spread and cache-economics parameters, not safety ones.

**What the namespace does not isolate.** Three limits are inherent to the design rather than oversights.

A model served in open access mode authenticates nobody, so two anonymous callers of one escrow sending the same client string are indistinguishable and share one namespace. The escrow component still separates them from every other escrow, but isolation between clients of one escrow requires authenticated access with a distinct credential per client.

Validation replays the original prompt without a session, and therefore without a namespace, so a sampled inference's prefix lands in the shared namespace on the validator's GPU, where a co-located client can probe it. Isolation is thus probabilistic: an inference is exposed with the probability that it is sampled for validation. Extending namespacing to the validation path would place a serving-engine-version-dependent field on the consensus path for escrows that never opted in, which is the worse trade.

The session identifier is stable for the life of the gateway process and identical for every participant that touches the request — primary, speculative secondary, and timeout verifier alike. A participant can therefore link two requests of one end-user, across models, even when it only ever saw a losing speculative attempt. Folding the destination participant into the derivation would remove this; it is deferred because the timeout path broadcasts one payload to many verifiers.

---

## Tunable Parameters

Set per gateway and per participant by the operator, not by governance: these affect one operator's own scheduling and memory, never chain state.

| Parameter | Default | Description |
|---|---|---|
| Gateway affinity enabled | off | Master switch. With it off the gateway derives no session identifier, so neither hop-1 stickiness nor the cache namespace happens for any request. |
| Gateway max requests | 32 | Consecutive requests before a binding is dropped and the session re-randomises. |
| Gateway binding lifetime | 120 s | Wall-clock lifetime of a binding. |
| Gateway map capacity | 50 000 | Binding-map size cap. |
| Participant stickiness enabled | off | Governs mlnode stickiness only; the cache namespace does not depend on it. Both the participant's engine and its broker read this flag, and both must have it set for hop-2 stickiness to happen. |
| Participant max requests | 64 | As above, at hop 2. |
| Participant binding lifetime | 600 s | As above, at hop 2. |
| Participant map capacity | 50 000 | As above, at hop 2. |

**Sizing the request bound.** The warm-hit share inside a binding is `(N-1)/N`, so 32 buys 97% against 90% at 10. The limit on raising it is key granularity: with `prompt_cache_key` a key is one conversation, keys are many, the first landing is still pseudo-random, and a high bound only raises variance in per-host load. With `user` as the key, one tenant is one key, and the bound is what stops a whole application from occupying one host. The default is chosen for that second case. The hop-2 bound is mostly unreachable in practice, because once hop 1 re-randomises the session moves to a different participant with its own bindings.

**Sizing the lifetime.** A binding is useful only while the serving engine still holds the blocks, which depends on cache capacity — GPU memory times its utilisation share, less model weights, divided by bytes per token, itself halved by an 8-bit KV cache — over the token ingress rate on that GPU. None of that is knowable in advance. Measure it instead: replay one prefix with a growing idle gap and find where the reported cached-token count falls to zero. Above that point a binding routes to a cold cache and only delays re-balancing; below it, reuse is discarded. **The current lifetimes are round numbers, not measurements.**

**Sizing the map.** Capacity must exceed the sessions live within one lifetime, roughly requests per second times lifetime divided by requests per session; at 100 rps, 120 s and 10 requests per session that is about 1200. An entry costs roughly 275 bytes, so a full map is about 14 MB per escrow and multiplies by the number of escrows a gateway serves.

---

## Observability

An operator who turns this on must be able to answer three questions without reading logs: whether stickiness is happening at all, whether the picker is yielding the preference more often than honouring it, and whether the binding map is approaching its cap. The second hop needs the same hit-versus-yield split separately, because hop 1 can be working perfectly while hop 2 lands the session on a different GPU and the cache stays cold either way. Cache effectiveness itself is reported by the serving engine as a cached-token count in each response and needs nothing new.

---

## Future Considerations

* Replace hop-2 stickiness with a shared KV pool inside one participant. A cache layer such as LMCache moves KV blocks out of GPU memory into a tiered store — host memory, local disk, a remote backend — and lets several serving instances read one pool, so a warm prefix stops being the property of the single GPU that computed it. Within one participant that is its own hardware and its own choice, and it would make hop-2 preference unnecessary: any of that participant's mlnodes could serve the warm prefix, leaving hop 1 as the only steering the network needs. Two conditions gate it. The cache namespace this proposal introduces must be carried into the pool's own key, or isolation that currently ends at one GPU is lost across every instance reading the pool. And the pool must stay inside a participant: sharing one across participants would mean one operator serving another's cached blocks, a trust boundary the network does not have and this proposal does not propose to create.
* Derive the session identifier per destination participant, removing cross-participant linkability, once the timeout path no longer broadcasts one payload to many verifiers.
* Extend namespacing to validation replays if the serving-engine field becomes version-stable across the network.
* Honour the Moonshot-native `cache_key` field as a third source, for clients that send it instead of `prompt_cache_key`.
* Replace the measured lifetime defaults with values derived from observed cache residency once the network has been measured under load.
