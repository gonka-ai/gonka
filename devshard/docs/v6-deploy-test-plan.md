# v6 deploy and test plan (delta from v5)

[v5-deploy-test-plan.md](./v5-deploy-test-plan.md) is the operator walkthrough for the 0.2.15 / v5 line — HA join overlay, router health and catalog admission, the gateway chain follower, host ping, warm cutover. All of it still applies. Do not edit it to describe v6.

This note is what changed for the **0.2.16 / v6** line: the host→gateway answer is gzip when the caller asks. The gateway→host HTTP request stays the signed plaintext. `DEVSHARD_GATEWAY_COMPRESS_REQUEST_BODIES` used to gzip that request; the gateway no longer reads it.

Related: [gzip-in-transit.md](../../docs/proposal/gzip-in-transit.md) (design, measurements, compatibility matrix), [compressed-payloads.md](../../docs/proposal/compressed-payloads.md) (the payload-storage work this builds on), [v5-deploy-test-plan.md](./v5-deploy-test-plan.md).

---

## Scope: two legs, two different stories

| Leg | Ships as | Operator decision |
| --- | --- | --- |
| host → gateway (the SSE answer) | **on**, negotiated, no flag | none — check the proxies below once |
| gateway → host (the HTTP request body) | plaintext, the signed bytes | none — there is no switch |

Neither leg persists anything and neither changes host state. Response rollback is a restart onto a build that does not compress.

## Response compression (no flag)

A host answers gzip when the caller asks for it. Nothing had to change on the calling side: the transport client sets no `Accept-Encoding` of its own and leaves `DisableCompression` off, so Go already asks and already unwraps. An old host that does not compress simply answers as before — this is `Accept-Encoding` negotiation, not a switch, so mixed rosters are fine in both directions.

Measured on the answer shape hosts send today: **4.9x** on a 4096-token completion. What is on the wire changes; what the gateway parses does not.

What to check once, and only if you run a non-standard edge:

- **nginx** — the streaming location already sets `proxy_buffering off`, `proxy_request_buffering off` and `gzip off` (`proxy/entrypoint.sh`, `STREAMING_CONFIG`). `gzip off` stops nginx compressing on its own; it does not unwrap what a host compressed. Do **not** add `gunzip on`.
- **HAProxy** (`versiond-router/haproxy.cfg.template`) has no compression directives at all and forwards both directions untouched.
- Any proxy of your own must not decompress a **request** body before the host. A host still unwraps `Content-Encoding: gzip` before it checks the signature. Decompressing earlier changes the bytes the signature covers.

## Request bodies stay plaintext

`DEVSHARD_GATEWAY_COMPRESS_REQUEST_BODIES` used to gzip HTTP request bodies the gateway sends a host. That path is retired. The gateway does not read the variable, and the POST body on the wire is the signed plaintext.

A host still accepts a gzipped request: it unwraps `Content-Encoding: gzip` before authentication, and the signature is over that plaintext. The gateway simply never sets the header. Peer RPC is a separate path and always sends Connect gzip; this variable never controlled it.

---

## Operator checklist

### Response leg (all v6 deploys)

- [ ] No action. Confirm chat still streams token by token after the upgrade; a stalled first token is the symptom to escalate on.
- [ ] If you run your own edge, confirm it does not set `gunzip on` and does not buffer the `/chat/completions` response.

### Request leg

- [ ] No action. The gateway does not gzip HTTP request bodies. `DEVSHARD_GATEWAY_COMPRESS_REQUEST_BODIES` is not read.
- [ ] If you run your own edge, confirm it does not decompress a request body before the host. The host unwraps gzip itself, under its verifier.

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
