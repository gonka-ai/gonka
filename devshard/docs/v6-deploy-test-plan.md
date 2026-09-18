# v6 deploy and test plan (delta from v5)

[v5-deploy-test-plan.md](./v5-deploy-test-plan.md) is the operator walkthrough for the 0.2.15 / v5 line — HA join overlay, router health and catalog admission, the gateway chain follower, host ping, warm cutover. All of it still applies. Do not edit it to describe v6.

This note is what changed for the **0.2.16 / v6** line: the two legs between the gateway and a devshard host are compressed. The response leg needs no flag and no decision. The request leg is behind one, and it is the only switch in this line that can break inference across a whole roster if it is turned on early.

Related: [gzip-in-transit.md](../../docs/proposal/gzip-in-transit.md) (design, measurements, compatibility matrix), [compressed-payloads.md](../../docs/proposal/compressed-payloads.md) (the payload-storage work this builds on), [v5-deploy-test-plan.md](./v5-deploy-test-plan.md).

---

## Scope: two legs, two different stories

| Leg | Ships as | Operator decision |
| --- | --- | --- |
| host → gateway (the SSE answer) | **on**, negotiated, no flag | none — check the proxies below once |
| gateway → host (the request body) | **off**, behind a flag | yes — it has a precondition on the whole roster |

Neither leg persists anything and neither changes host state. Rollback on either is a restart.

## Response compression (no flag)

A host answers gzip when the caller asks for it. Nothing had to change on the calling side: the transport client sets no `Accept-Encoding` of its own and leaves `DisableCompression` off, so Go already asks and already unwraps. An old host that does not compress simply answers as before — this is `Accept-Encoding` negotiation, not a switch, so mixed rosters are fine in both directions.

Measured on the answer shape hosts send today: **4.9x** on a 4096-token completion. What is on the wire changes; what the gateway parses does not.

What to check once, and only if you run a non-standard edge:

- **nginx** — the streaming location already sets `proxy_buffering off`, `proxy_request_buffering off` and `gzip off` (`proxy/entrypoint.sh`, `STREAMING_CONFIG`). `gzip off` stops nginx compressing on its own; it does not unwrap what a host compressed. Do **not** add `gunzip on`.
- **HAProxy** (`versiond-router/haproxy.cfg.template`) has no compression directives at all and forwards both directions untouched.
- Any proxy of your own must not decompress a **request** body. See the precondition below for why that specific one is fatal.

## Request compression (`DEVSHARD_GATEWAY_COMPRESS_REQUEST_BODIES`)

The gateway may gzip the request bodies it sends hosts. Requests have no `Accept-Encoding` equivalent — a sender cannot ask whether the far side understands gzip — so this one is **off by default**.

| | |
| --- | --- |
| Env | `DEVSHARD_GATEWAY_COMPRESS_REQUEST_BODIES` |
| Enable | `1` / `t` / `true` / `yes` / `on` (case-insensitive) |
| Default | **off**. Unset, empty, `0` / `f` / `false` / `no` / `off` leave bodies uncompressed. Any other value fails the runtime rather than reading as off. |
| What it changes | Bodies from 1 KiB up leave gzipped; the signature still covers the plaintext |
| Precondition | **Every host in the roster must already decompress.** Ship the read side first |
| Observability cost | `http.request_content_length` drops off the span for a compressed POST, because the header no longer describes the body once it is unwrapped. `/chat/completions` still reports the decoded size separately; the protocol routes report none |
| Failure if the precondition is unmet | The host verifies the compressed bytes, recovers a different address, and answers **403 sender-not-in-group** — which reads as a key or registration problem, not a compression one |

That last row is the one to remember. The signature covers the body with no canonicalisation, and ECDSA recovery does not fail on the wrong bytes — it returns somebody else. A host without the read side therefore does not say "I cannot read this"; it says "I do not know you".

---

## Operator checklist

### Response leg (all v6 deploys)

- [ ] No action. Confirm chat still streams token by token after the upgrade; a stalled first token is the symptom to escalate on.
- [ ] If you run your own edge, confirm it does not set `gunzip on` and does not buffer the `/chat/completions` response.

### Request leg (only when turning the flag on)

- [ ] Confirm **every** host in the roster runs v6 or later. A single host on the old build answers 403 sender-not-in-group for every request the gateway compresses to it.
- [ ] Turn it on for one gateway first and watch that host's 403 rate, not just the gateway's error rate.
- [ ] Confirm no proxy between gateway and hosts decompresses request bodies. Decompression must happen at the host, under its verifier.
- [ ] Rollback is clearing the variable and restarting. Nothing is persisted; hosts need no coordinated change.

---

## Automated tests

These run in the normal `go test` sweep; none needs the stand.

### The signature survives the encoding

```bash
cd devshard && go test ./server/ -run 'TestCompressedRequest|TestSigningTheCompressedBytes|TestMalformedGzip|TestTruncatedGzip|TestEmptyBodyClaimingGzip' -count=1
```

Proves a body signed as plaintext and sent compressed verifies through the real routes; that signing the encoding instead recovers a stranger; that a body claiming gzip and not being gzip answers 400 rather than a 5xx; and that the 10 MiB cap bounds the **decoded** body, asserted with a real bomb.

### The stream is not held back

```bash
cd devshard && go test ./transport/ -run TestServer_CompressedStreamingInferenceStaysFrameByFrame -count=1
```

Drives the real `HandleInference` with `Accept-Encoding: gzip` and pins two flush points: the receipt reaches the wire before the answer, and the answer before `[DONE]`. Silencing either writer's flush fails it.

### The decoded stream is bounded

```bash
cd devshard && go test ./transport/ -run TestParseSSE_Stream -count=1
```

Compression lets an untrusted executor buy a large decoded stream cheaply; `MaxSSEStreamBytes` (256 MiB) bounds it, and the pair of tests pins the boundary exactly.

### End to end

```bash
cd devshard && make e2e
```

The stub host mounts the same two middlewares as production, so the stand exercises the mounted path rather than routing around it.
