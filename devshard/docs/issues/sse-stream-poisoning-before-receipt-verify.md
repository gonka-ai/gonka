# SSE stream poisoning before receipt verification

**Status:** open · documented risk · no settlement break

**Affected components:** `devshard/transport/client.go` (SSE parser +
`StreamCallback`), `devshard/cmd/devshardctl/proxy.go` (caller-side
proxy), `devshard/user/user.go` (`ProcessResponse` / `StateSig`
verification), `devshard/transport/server.go` (host SSE emitter).

**Related:**

- [`docs/proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md`](../proposals/HEIGHT_SYNC_HEADERS_PROPOSAL.md)
  §"Asymmetric verification: responses signed, requests trusted, proof on
  demand (PoC v2.1)" — same deniability property for height-sync sections.
- [`docs/attacks.md`](../attacks.md) §"Executor refuses to work" — adjacent
  failure mode (no receipt at all).
- [`plans/height-sync-anchor-poc.md`](../../plans/height-sync-anchor-poc.md)
  §3.8 "Pathology: invalid signature" (planned addition).

---

## 1. Summary

`devshardctl` proxies host inference responses to the upstream caller as
**Server-Sent Events**. SSE `data:` lines are forwarded to the caller
*as they arrive*; the **`devshard_receipt` event with `StateSig`** is the
**last** event in the stream. The user-side `ProcessResponse` then
verifies `StateSig` against the expected host validator address.

This creates a window where the caller has already observed token bytes
that came from a host whose signature later fails to verify — i.e., the
host returned an invalid (or absent) `StateSig`, or the bytes were
otherwise tampered/MITM'd. The user cannot prove what bytes the host
actually transmitted (TLS is non-repudiable peer-to-peer but deniable to
third parties), so the failed inference is treated as **"no valid
response from host"** — settlement-safe but **not** safe for caller-side
content trust.

## 2. Concrete flow today

Streaming path:

```text
host (transport/server.go)
  └─ SSE: data: { devshard_meta }     │
     data: { tokens from engine }     │  flushed line-by-line via
     data: { tokens from engine }     │  StreamCallback in client.go
     ...                              │  ──────────────────────────►   caller
     data: { devshard_receipt: StateSig, StateHash, Nonce, ... }
     data: [DONE]
```

Verification path (after `[DONE]`):

```52:60:devshard/transport/client.go
	StreamCallback   func(nonce uint64, line string) // if set, receives raw SSE data lines during inference
```

```315:332:devshard/transport/client.go
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			sawDone = true
			if c.config.StreamCallback != nil {
				c.config.StreamCallback(nonce, line)
			}
			continue
		}

		// Try to parse as devshard protocol envelope.
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &envelope); err != nil {
			// Not JSON -- forward as-is.
			if c.config.StreamCallback != nil {
				c.config.StreamCallback(nonce, line)
			}
```

```203:223:devshard/user/user.go
	if resp.StateSig != nil {
		expectedAddr := s.group[hostIdx].ValidatorAddress
		sigContent := &types.StateSignatureContent{
			StateRoot: resp.StateHash,
			EscrowId:  s.escrowID,
			Nonce:     resp.Nonce,
		}
		sigData, err := proto.Marshal(sigContent)
		if err != nil {
			return fmt.Errorf("marshal state sig content: %w", err)
		}
		addr, err := s.verifier.RecoverAddress(sigData, resp.StateSig)
		if err != nil {
			return fmt.Errorf("%w: host %d: %v", types.ErrInvalidStateSig, hostIdx, err)
		}
		if addr != expectedAddr {
			if !s.sm.CheckWarmKey(addr, expectedAddr) {
				return fmt.Errorf("%w: host %d: expected %s, got %s",
					types.ErrInvalidStateSig, hostIdx, expectedAddr, addr)
```

By the time `processResponse` returns an error for `ErrInvalidStateSig`,
the caller has already seen all the token `data:` lines.

## 3. Threat model

| Adversary | Capability | Outcome today |
|-----------|------------|---------------|
| Malicious host returns invalid `StateSig` | Streams arbitrary tokens; signs garbage at the end | Caller saw tokens; host gets no settlement (no `MsgFinishInference`); session retries on next slot |
| Malicious host returns no `StateSig` (drops the receipt event) | Streams tokens, omits receipt | Same as above — `RefusalTimeout` path then takes over |
| Buggy host accidentally returns wrong signer (e.g. wrong key) | Streams tokens with `StateSig` that recovers to unexpected address | Settlement rejected; identical caller-side effect |
| MITM on the SSE channel | Injects tokens before terminator; replaces receipt | Same — user cannot prove which bytes came from host |

Common property: **the caller may have consumed bytes that the protocol
will later refuse to settle on.** None of these adversaries gain payment;
all of them can briefly poison caller output.

## 4. What the protocol already guarantees

- **No settlement on failure.** `ErrInvalidStateSig` causes
  `SendInference` / `ProcessResponse` to return error. The user does
  **not** pipeline `MsgFinishInference` for this nonce in the next diff,
  so the host is never paid for the failed call.
- **State stays consistent.** Local diff for this nonce was applied by
  `PrepareInference`, but no response-derived state (`StateHash`, warm
  keys, mempool from host) is committed because `processResponse`
  short-circuits at the verify failure. The next host that serves the
  session sees the same pre-response state.
- **Detection is local + cheap.** `RecoverAddress` runs entirely on the
  user side; failure is a fast classification, not a quorum operation.
- **Reputation / liveness.** Repeated invalid receipts feed host-health
  metrics (`HostStats.missed++` once the timeout path engages via
  `attacks.md §"Executor refuses to work"`).

## 5. What is NOT guaranteed (the actual risk)

- **Token bytes already on the wire to the caller.** Anything in the SSE
  stream up to `[DONE]` has been flushed downstream. Mitigation requires
  one of the modes in §6.
- **Caller-side billing alignment.** If the caller layer (devshardctl
  customer SDK / browser) charges or commits to the user based on
  observed tokens rather than on a verified completion event, it can
  over-charge or expose unverifiable content.
- **Cryptographic blame.** An invalid signature is **unprovable to a
  third party**. The user cannot present "host X sent me garbage" as
  evidence; TLS is deniable to anyone other than the two endpoints.
  So this risk **does not** translate into a slashable offense.

## 6. Mitigation options

### A. Document + accept (recommended default)

State explicitly that:

- Streaming output is **best-effort UX**.
- Protocol settlement and host payment depend on the verified receipt.
- Callers requiring strong content trust MUST opt in to one of (B–D).

This matches the existing devshard threat model and the height-sync
asymmetric-verification design — see references in §0.

### B. Buffered proxy mode (opt-in)

`devshardctl` exposes a config flag (e.g. `--receipt-first` or
`Session.BufferedStream = true`) that:

1. Buffers SSE `data:` lines in memory (capped by a size budget).
2. On `[DONE]`, parses `devshard_receipt`, verifies `StateSig`.
3. **Only then** flushes accumulated tokens to the caller (or emits a
   single failure event).

Cost: kills TTFT; bounded memory per in-flight inference.

### C. Sentinel-then-trailer mode (recommended for high-trust callers)

Always stream tokens (preserve TTFT) but emit a final **trailer event**
to the caller:

```text
data: { "devshard_outcome": "verified" }     // or "rejected:invalid_state_sig"
```

This is generated by `devshardctl` after `ProcessResponse` returns.
Callers that care about authenticity gate downstream actions on the
trailer; callers that don't can ignore it. Cost: one extra event per
inference; trivial.

### D. Bind tokens to the state root (future)

If the host's `StateRoot` ever commits to the served tokens (e.g. via a
prompt+completion Merkle root in `StateSignatureContent`), then a
verified `StateSig` proves the token stream. This is a protocol change,
not a `devshardctl` change, and lives in the cPoC / completion-commit
roadmap.

## 7. Open decisions

- Whether `devshardctl` ships a default trailer event (option C) in the
  next milestone or stays at option A.
- Whether `Session.BufferedStream` (option B) is exposed in the v1
  caller config or kept as a debug-only flag for testenv.
- Whether the trailer's payload schema is normalized so SDKs can rely on
  it programmatically.

## 8. Acceptance / verification

- **Settlement-safe today.** Existing unit tests in
  `devshard/user/user_test.go` cover `ErrInvalidStateSig` short-circuit;
  `devshard/transport/client_test.go` covers SSE parsing and
  `StreamCallback`.
- **Test gap (to add when option C or B lands):**
  - `TestSession_InvalidStateSig_DoesNotEmitFinishInference` (settlement).
  - `TestProxy_StreamTrailer_RejectedOnInvalidStateSig` (option C).
  - `TestProxy_BufferedStream_HoldsTokensUntilReceiptVerify` (option B).

## 9. Why we are filing this rather than fixing inline

The trade-off between TTFT and content authenticity is **caller-policy**,
not protocol. The settlement layer is already correct (no payment for
invalid receipts), and the deniability property of TLS means no
mitigation in `devshardctl` can produce slashable evidence. This issue
tracks the residual caller-UX concern and the option matrix so the
choice can be made deliberately in the next milestone instead of being
re-discovered ad hoc.
