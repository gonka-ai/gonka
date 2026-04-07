# Issue: Fast-path unreachable hosts and invalid responses in subnetctl `sendAndProcess`

## Summary

Change the testenv `subnetctl` proxy (and align production `subnetctl` if applicable) so that **clear network / host-unreachable errors** do not sit behind the full **`RefusalTimeout` + buffer** sleep before a second attempt or before entering the **refusal-timeout protocol** phase. Use **short retries** first; if the host remains unreachable, **advance to refusal-timeout handling immediately** so the rest of the group can participate (timeout votes / protocol continuation). Reserve the **long refusal wait** for the case where a **TCP connection is established** but the **protocol response is missing or invalid** after bounded retries.

## Background

Today, `sendAndProcess` treats `SendOnly` returning `(nil, err)` (no parsed `HostResponse`) as a **soft** outcome: it returns `(false, 0, nil)` and `runInference` then **sleeps until** `RefusalTimeout + timeoutBuffer` before calling `sendAndProcess` again. That makes unreachable executors feel “stuck” for ~65s+ even though the failure mode is known immediately at the transport layer.

See: `subnet/docs/proposals/PROTOCOL_TESTING_PROPOSAL.md` §2.1–2.3, and implementation in `subnet/testenv/cmd/subnetctl/proxy.go` (`sendAndProcess`, `runInference`).

At the Go HTTP client layer we can often distinguish:

- **Dial / unreachable** (`net.OpError`, `ECONNREFUSED`, `EHOSTUNREACH`, DNS failure, etc.).
- **Connected** but **HTTP non-200** or **body / SSE parse** failure.
- **Partial SSE** with optional partial `HostResponse` (see `subnet/transport/client.go`).

Semantic “host processed inference” still requires a **valid protocol response**; TCP success alone is not enough.

## Problem

1. **Unreachable host** is penalized with the **same long delay** as “maybe the host will accept soon,” which hurts UX and slows tests.
2. **Invalid responses** do not get a **tight retry loop** before escalating to refusal-timeout protocol.
3. The product intent (as stated by maintainers): after unreachable is confirmed, **do not block on the full refusal window** before moving into the phase where **other validators** can engage with **refusal / timeout** behavior (rather than waiting blindly).

## Proposed behavior

### A. Error classification (transport → proxy)

Extend `sendAndProcess` (or `HTTPClient.Send` error wrapping) so the proxy can branch on:

| Class | Typical signals | Initial handling |
|-------|-------------------|------------------|
| **Unreachable** | Dial errors, connection refused, no route, definitive “host down” | Short backoff retries (see B). If still unreachable after threshold → **enter refusal-timeout protocol path without waiting `RefusalTimeout`**. |
| **Connected, invalid / incomplete protocol** | HTTP error, unmarshal failure, missing receipt/meta, unexpected envelope | **Immediate retry** (see B). After **retry threshold** → **enter refusal-timeout protocol path** without waiting the full `RefusalTimeout` first. |
| **Connected, still in progress** | Valid partial state but inference not finished (no finish tx yet) | Existing timing: **wait `RefusalTimeout` (+ buffer)** only when appropriate (execution vs refusal), per current `confirmedAt` logic. |

Exact classification should use `errors.Is` / `errors.As` on `*net.OpError`, `syscall` errno, and `context.DeadlineExceeded` where relevant. Document which errors map to which class.

### B. Retry policy (before long sleep)

1. **Unreachable:**  
   - Retry `sendAndProcess` (or `SendOnly`) with **short delays** (e.g. 1s, 2s, 4s — configurable, capped).  
   - If all retries remain unreachable → **do not** sleep the full `RefusalTimeout` just for that; go straight to the **second phase** of refusal handling (timeout votes / `handleTimeout` with `TIMEOUT_REASON_REFUSED` or equivalent), matching current end state but **faster**.

2. **Invalid response (after connection):**  
   - **Immediate** retry (minimal or zero backoff), **N attempts** (configurable threshold).  
   - If threshold exceeded → same as above: **enter refusal-timeout protocol path** without waiting the full `RefusalTimeout` first.

3. **Connected but no valid completion yet:**  
   - Keep **existing** semantics: **wait `RefusalTimeout` (+ buffer)** when `confirmedAt == 0` and we have reason to believe the host is alive but refusing / not completing, as today.

### C. “Other hosts” / protocol alignment

Clarify in implementation notes (and code comments) how “other hosts try to proxy” maps to existing mechanics:

- Timeout **vote collection** across non-executor validators (`CollectTimeoutVotes`, `handleTimeout`).
- Any **gossip / catch-up** behavior already triggered by timeout txs.

If literal HTTP proxying through peers is **not** implemented, the issue title in UX terms should still resolve to **advancing refusal-timeout protocol** (votes + pending timeout diff) **without** the full pre-phase sleep when unreachable is definitive.

## Non-goals (for this issue)

- Proving “request was processed” without a valid `HostResponse` (still impossible).
- Changing **execution-timeout** paths (`confirmedAt > 0`) in the first iteration unless the same classification clearly applies there too (optional follow-up).

## Suggested configuration (constants or env)

- `SUBNETCTL_SEND_QUICK_RETRY_MAX` — max attempts for unreachable / invalid-response fast path.  
- `SUBNETCTL_SEND_QUICK_RETRY_BASE` — base delay for unreachable backoff.  
- Optional: feature flag to preserve legacy “always wait RefusalTimeout on soft send” for rollback.

## Acceptance criteria

- [ ] Unreachable executor (e.g. connection refused): no **full** `RefusalTimeout` sleep **before** escalation when retries are exhausted; refusal-timeout protocol (votes / user-visible error category) is reached **materially sooner** than today.  
- [ ] Connected but **invalid** HTTP / parse / protocol envelope: **immediate** retries up to threshold, then same escalation without the long pre-wait.  
- [ ] **Established connection**, valid partial progress, refusal case: still **waits** `RefusalTimeout` (+ buffer) where the protocol requires it (no premature timeout).  
- [ ] Unit or integration tests: simulate unreachable (`killableClient` / injected dial error) and invalid response; assert **elapsed time** or **call count** bounds vs baseline.  
- [ ] Document error classification table in code or `subnet/docs/proposals/PROTOCOL_TESTING_PROPOSAL.md` follow-up section.

## Related code

- `subnet/testenv/cmd/subnetctl/proxy.go` — `runInference`, `sendAndProcess`, `sleepUntil` / refusal branch.  
- `subnet/cmd/subnetctl/proxy.go` — production parity if behavior should match.  
- `subnet/transport/client.go` — `HTTPClient.Send`, `doPostRaw`, SSE parse errors.  
- `subnet/user/user.go` — `SendOnly`, session semantics.

## References

- `subnet/docs/proposals/PROTOCOL_TESTING_PROPOSAL.md` §2.1–2.3 (subnetctl soft failures, long waits, network vs semantic completion).
