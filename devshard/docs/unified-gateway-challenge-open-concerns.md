# Unified gateway challenge: open concerns

**Plan:** [unified-gateway-challenge-plan.md](./unified-gateway-challenge-plan.md)  
**Design:** [proposals/unified-gateway-challenge.md](./proposals/unified-gateway-challenge.md)  
**Status:** open. None of these is resolved in the plan. Each entry names the step it blocks.

Severity: **High** can strike an honest executor or make a kind unusable. **Medium** is a missing piece the plan needs. **Low** is a decision or alignment item.

Entries marked *(code-checked)* were confirmed in the tree. The rest are inferred from reading the plan against the code and need a check when their step starts.

---

## High

### C1. Honest spill can hash an ignorable event *(code-checked)*

**Blocks:** [Step 8](./unified-gateway-challenge-plan.md#step-8-case-1-empty-chunk)

In `transport/chat_gzip.go`, `scheduleSignedEvent` drops events the classifier calls ignorable, but its `default` branch commits them when `sent` is true (`already > 0 || folded`). An event larger than the coalesce cap is partly released before its class is known. If it later resolves to ignorable, the honest executor has hashed and signed it.

Rule 1 of the empty definition (empty-content `data:` event) would then strike an honest executor.

**Needed:** an exemption for the spill path (for example, only events fully inside the cap count), or a test showing the case cannot happen. Add `TestChatFrameSink_SpilledIgnorableEventIsNotStrike`.

### C2. "Bare `[DONE]`" is broader than "bare"

**Blocks:** [Step 8](./unified-gateway-challenge-plan.md#step-8-case-1-empty-chunk)

The plan defines it as `[DONE]` with no `finish_reason` or error before it in the span. An engine abort can end content with `[DONE]` and no `finish_reason`. The existing `done_without_finish` WARN in `logChatFrameCheck` shows this happens. Such a stream has content, so it is not empty, but the rule would strike it.

**Needed (decision):** narrow the rule to `[DONE]` with zero content tokens and no finish, or keep the broad rule and accept the risk. The narrow rule is safer. `done_without_finish` with content stays a WARN.

### C3. The MLNode tokenize endpoint is not in this tree *(code-checked)*

**Blocks:** [Step 12](./unified-gateway-challenge-plan.md#step-12-case-2-context-limit) branch A

A search for the tokenizer endpoint found nothing outside an unrelated admission-control approximation in `gateway.go`. Branch A assumes an endpoint that returns the same `prompt_tokens` the engine charged.

**Needed:** confirm the endpoint exists in the MLNode contract, what it returns for multimodal input and system-prompt injection, and that its count equals the engine's. Add a conformance test against a real or recorded engine response. Tolerance 0 is only sound once this holds.

### C4. Branch A may never reach a strike

**Blocks:** [Step 12](./unified-gateway-challenge-plan.md#step-12-case-2-context-limit) branch A

Hosts with old MLNodes vote `NONE` by design. If most validators have not upgraded, strike weight can never exceed `VoteThreshold`, so the kind is permanently inert.

**Needed:** a rollout rule: a deadline after which the gateway stops starting branch A, or a minimum fraction of capable hosts reported in host status before the gateway enables it. The capability probe result must be visible to the gateway, not only logged.

### C5. Reconnect after a pre-stream 400 re-executes the request

**Blocks:** [Step 12](./unified-gateway-challenge-plan.md#step-12-case-2-context-limit) branch B scope

The plan says the gateway reconnects instead of challenging a hidden 400. It does not say what reconnecting means for the record. Open questions:

- Does the reconnect reuse the same inference id and nonce, or open a new inference?
- Is the failed attempt charged, or refunded by timeout / error-miss?
- Can a retry loop be abused by an executor that always returns 400 (cost to the gateway)?
- Which executor receives the retry (same host, or another)?

**Needed:** read what `redundancy.go` does on a 400 context error today (it already parses "maximum context length") and write the intended behaviour with a bound on retries.

---

## Medium

### C6. Chain-side penalty for structural strikes is undefined

**Blocks:** [Step 2b](./unified-gateway-challenge-plan.md#step-2b-protocol-gate-and-settlement-enrichment)

The plan adds `invalid_structural` and per-kind counters to settlement. The inference-chain module is outside this repo, and no document says what it does with them: slashing, reputation, reward reduction, or only a metric. The existing `Invalid` still penalises, so an old chain is not worse off, but a new chain has nothing to apply.

**Needed:** a chain-side proposal, and an agreed order (chain learns the fields, then devshard enables the version).

### C7. Observed results have no consumer

**Blocks:** [Step 4](./unified-gateway-challenge-plan.md#step-4-gateway-collection-skeleton), [Step 10](./unified-gateway-challenge-plan.md#step-10-case-1-finish-vs-signed-count)

`OBSERVED` is a WARN log and a metric. Nothing acts on it. Without a consumer the grade has no effect on an executor that is consistently slightly off.

**Needed (decision):** either feed it into validation sampling for that executor, add an alert, or state that it is monitoring only. Also cap observe challenges per session so the RPC cost stays bounded.

### C8. Evidence retention under HA gateway failover

**Blocks:** [Step 6](./unified-gateway-challenge-plan.md#step-6-gateway-evidence-capture)

Step 5 makes the vote store durable (memory, SQLite, Postgres). Step 6 gives tail evidence, the complete-stream flag and span snapshots no store. If a gateway replica fails over between the stream and the challenge, in-memory evidence is lost and the challenge silently never fires.

**Needed:** persist evidence in the same backends as the vote store, with the same retention bound, or state that failover loses the challenge and test for that.

### C9. Hedged attempts must bind evidence to the winning attempt

**Blocks:** [Step 6](./unified-gateway-challenge-plan.md#step-6-gateway-evidence-capture)

`redundancy.go` races several hosts. Step 6 keeps evidence "on the attempt". The Finish that applies belongs to one executor, so evidence from a losing attempt must never be attached to it, and evidence from the winner must survive the race teardown.

**Needed:** key retained evidence by `(escrow, inference, executor slot)` and add `TestGateway_EvidenceBoundToWinningAttempt`.

### C10. `Validated` and `Challenged` records block strikes

**Blocks:** [Step 2](./unified-gateway-challenge-plan.md#step-2-applychallenge-skeleton)

`applyChallenge` accepts only `Finished`. ML validation never checks empty events or signed counts, so an executor that passes sampled validation (or whose record is already `Challenged`) cannot be struck for a signed-count lie afterwards.

**Needed (decision):** keep as designed and state it, or allow a strike from `Validated` / `Challenged` with a defined accounting rule (the pass unwind and the existing tallies interact).

### C11. Span cap is not tied to the sign schedule

**Blocks:** [Step 6](./unified-gateway-challenge-plan.md#step-6-gateway-evidence-capture)

`SignEvery` can be up to 4096 chunks, but `MaxSpanEvidenceBytes` defaults to 256 KiB. A long span is dropped as oversized and can never be challenged. The gateway chooses the schedule, so it can stay inside the cap, but nothing enforces that.

**Needed:** derive the cap from `Join` and `SignEvery`, or reject a schedule whose worst-case span exceeds the cap. Add a test.

### C12. Collection retries are not budgeted against sealing

**Blocks:** [Step 4](./unified-gateway-challenge-plan.md#step-4-gateway-collection-skeleton), [Step 6](./unified-gateway-challenge-plan.md#step-6-gateway-evidence-capture)

Retention is tied to the seal horizon, but a collection that retries on 429 / 503 can outlast the live window. A strike tx that arrives after the record is sealed is refused.

**Needed:** a collection deadline shorter than the seal horizon, with the outcome `incomplete` when it expires. Add `TestRunChallenge_DeadlineBeforeSeal`.

---

## Low

### C13. Step 7 mechanism is undecided

**Blocks:** [Step 7](./unified-gateway-challenge-plan.md#step-7-align-the-signed-token-count-with-completion-tokens)

The final signed count must equal `completion_tokens`. The plan leaves open whether the executor adds the unaccounted tokens to the final frame or the classifier counts per engine token. Intermediate frames stay event-based, so the Step 9 span rule depends on the choice being one shared function.

### C14. Step 14 fee primitive is undecided

**Blocks:** [Step 14](./unified-gateway-challenge-plan.md#step-14-case-3-gateway-paid-trigger)

No `ValidationFee` exists in the tree. A fee plus an "owed" marker needs either a record field (state-hash change, version bump) or a separate store. Decide before coding. The fee debit must not race escrow settlement.

### C15. Effective `max_tokens` when the user omits it

**Blocks:** [Step 12](./unified-gateway-challenge-plan.md#step-12-case-2-context-limit) both branches

`MaxTokens` and `Model` exist on the inference record (`types/domain.go`). If the gateway fills a default `max_tokens` when the user omits it, both branches must use the value the engine enforced, not the user's original.

### C16. Public-validation plan names the old votes

**Blocks:** none (documentation)

[public-validation-assignment-plan.md](./public-validation-assignment-plan.md) still describes the two chat-frame votes as "token count mismatch" and "signed prefix ≠ SSE artifact". They are now signed-count-vs-span, empty chunk and Finish-vs-signed-count. Update that document so both plans name the same kinds.

---

## Summary

| ID | Concern | Severity | Blocks |
| --- | --- | --- | --- |
| C1 | Honest spill can hash an ignorable event | High | Step 8 |
| C2 | Bare `[DONE]` rule too broad | High | Step 8 |
| C3 | MLNode tokenize endpoint not in tree | High | Step 12 A |
| C4 | Branch A may never reach a strike | High | Step 12 A |
| C5 | Reconnect semantics after pre-stream 400 | High | Step 12 B |
| C6 | Chain-side penalty undefined | Medium | Step 2b |
| C7 | Observed results have no consumer | Medium | Steps 4, 10 |
| C8 | Evidence retention under HA failover | Medium | Step 6 |
| C9 | Hedged attempts: bind to winner | Medium | Step 6 |
| C10 | Validated / Challenged block strikes | Medium | Step 2 |
| C11 | Span cap vs sign schedule | Medium | Step 6 |
| C12 | Retries vs seal horizon | Medium | Steps 4, 6 |
| C13 | Step 7 mechanism undecided | Low | Step 7 |
| C14 | Fee primitive undecided | Low | Step 14 |
| C15 | Effective `max_tokens` | Low | Step 12 |
| C16 | Public-validation plan wording | Low | none |
