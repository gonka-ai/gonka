# Timeout clock dual-mode — implementation quality review

**Related:** [timeout-clock-dual-mode-plan.md](timeout-clock-dual-mode-plan.md), [timeout-clock-dual-mode-audit.md](timeout-clock-dual-mode-audit.md)  
**Scope:** Track B (`devshard/`) against design goals: v6 wall legacy by default, stamp active when frozen, shadow + observability without flipping apply, B8 dual-run.

## Summary

| Severity | ID | Issue | Area |
|:---:|:---:|---|---|
| **Fixed** | 1 | Mismatch counter registers on default Prometheus registry; not exposed on gateway or host `/metrics` | B7, rollout |
| **Fixed** | 2 | E2E `header` subtest: mock-chain + gateway header, but `devshard-host` always legacy via env | B8 |
| **Fixed** | 3 | Background sweep + escrow hold use stamp `TimeoutRemaining`; legacy path not v6 wall | B5, ops |
| **Fixed** | 4 | Gateway shadow sampled on every `TimeoutDeadline` poll — timing noise, not stable divergence | B5, B7 |
| **Fixed** | 5 | Refusal wait hot path: `StoredAt` under shared header map lock per poll | B5, perf |
| **Fixed** | 6 | Gateway shadow vs verifier shadow use different stamp due semantics | B5, B6 |
| **Accepted** | 7 | Clock flag is read from the chain and is already escrow-bounded | B1, ops |
| **Fixed** | 8 | `EvalDue` legacy `wallPast` unused in production; contradicts v6 if wired blindly | B4 |
| **Fixed** | 9 | `clockMismatchOnce` only pruned for nonces in `nonceStates` | B5 |
| **Fixed** | 10 | Minor v6 parity: `VerifyPayload` no longer checks `StartedAt`; mempool confirm pick rule | B3 |
| **Fixed** | 11 | Debug endpoint mixes process-global mismatch with per-escrow mode | B7 |
| **Fixed** | 12 | Plan doc still `Planned`; B4/B5 wording drift; untracked `due.go` / `mismatch.go` break CI | docs, ship |

**What already matches the design (no row above):** legacy gateway deadline (v6 wall + `TimeoutBuffer`, issue 9 `confirmedAt` clear), legacy verifier integer `>=` with no buffer, header active path via `Decide`, apply votes-only, state root wall fields, bridge + mock-chain freeze, shadow outside `s.mu` with once-per nonce+kind, unit dual-clock gates.

---

## High

### 1 — Mismatch counter not on served `/metrics`

**Fixed.** `RegisterTimeoutClockMismatch` puts `devshard_timeout_clock_mismatch_total` on the gateway metrics registry and on `observability.Registry()`. `RecordClockMismatch` is still the only increment. Debug `mismatch_total` is that process sum.

**Problem.** `headertime.RecordClockMismatch` uses `prometheus.MustRegister` on the **default** registry (`chainoracle/blocks/headertime/mismatch.go`). Production handlers do not scrape it:

- Gateway: `DevshardMetrics` uses a dedicated registry (`cmd/devshardctl/metrics.go`).
- Host: `observability.MetricsHandler()` serves `observability.Registry()`.

Rollout step 4 in the plan (“watch `devshard_timeout_clock_mismatch_total` under legacy”) cannot be executed from normal scraping. Debug JSON on the gateway is the only structured cross-check for gateway mismatches; verifier mismatches are warn-only unless logs are aggregated.

**Proposed fix.**

1. Register `devshard_timeout_clock_mismatch_total` on **both** registries that back `/metrics` (gateway metrics registry + `observability.Registry()`), or export via a small shared helper both init paths call once.
2. Keep `RecordClockMismatch` as the single increment site; avoid double-counting if both processes are separate binaries (each process gets its own counter — correct).
3. Add tests: after a forced mismatch, scrape handler output (or `Gather()` on the registry) and assert the series appears with `{side,kind}`.
4. Optionally document that gateway debug `mismatch_total` is a process sum, not a substitute for Prometheus in production.

---

### 2 — E2E header subtest does not run header verifiers

**Fixed.** `sessionConfigFromEnv` copies `DEVSHARD_HEIGHTSYNC_TIMEOUT_CLOCK` onto the host session. Header e2e sets that env on every host to the same value the mock chain freezes on the escrow. Each host's `/debug/timeout-clock` is checked against the gateway session clock after bind. Unset stays legacy.

**Problem.** `TestE2E_TimeoutProtocolEndToEnd` and `TestE2E_ExecutionTimeoutProtocolEndToEnd` set `MOCK_CHAIN_HEIGHTSYNC_TIMEOUT_CLOCK=true` on mock-chain. Gateway binds the escrow and gets `HeightsyncTimeoutClock=true`. E2E **hosts** use the `devshard-host` image and `sessionConfigFromEnv` (`cmd/devshard-host/main.go`), which never copies `heightsync_timeout_clock` — only timeouts and economics fields.

Result: **header gateway + legacy verifiers** in the same run. Votes can disagree (stamp due at gateway, wall not due at host), causing flakes or false greens. Citest is fine when hosts use `devshardd` + bridge bind.

**Proposed fix.**

1. Extend `sessionConfigFromEnv` (and `EscrowSessionFields`) with an env such as `DEVSHARD_HEIGHTSYNC_TIMEOUT_CLOCK` / reuse a single name aligned with mock-chain.
2. In `timeoutProtocolEnvOptions(header)`, when `header == true`, set that env on **host** containers the same way as mock-chain.
3. Assert host status or debug JSON includes `heightsync_timeout_clock == header` after bind (mirror gateway assertion in `e2e/timeout_test.go`).
4. Update B8 plan checkboxes: e2e dual-run is only valid for **both** clocks on gateway **and** verifiers.
5. Do not rely on process-only clock on gateway alone; escrow + env must agree (already the plan rule).

---

### 3 — Sweep and escrow hold still use stamp clock in legacy mode

**Fixed.** Sweep and hold call `protocolTimeoutDue`. Legacy ages the wall anchor plus `TimeoutBuffer` (and the sweep grace). Header ages the stamp.

**Problem.** `overdueTimeoutInferences` (`cmd/devshardctl/execution_timeout_sweep.go`) and `summarizeEscrowHoldInFlight` (`escrow_hold.go`) call `StateMachine.TimeoutRemaining`, which ages timeouts against the header **floor** and stamp window (`state/anchor.go`), not wall `StartedAt` / `ConfirmedAt`.

On **v6** (`devshard-0.2.x-v6`), sweep used wall stamps + `TimeoutBuffer` + grace. In legacy today:

- Pending refusal sweep uses `HandleTimeout(..., time.Time{})` → gateway wall refusal is immediately past, but selection used stamp floor “remaining”.
- Execution sweep can open before `ConfirmedAt + execution + buffer`.
- Feed/map errors make `TimeoutRemaining` fail → legacy orphans never swept (legacy incorrectly depends on header feed).

**Proposed fix.**

1. Add a small clock branch on `SessionConfig.HeightsyncTimeoutClock` (or inject a `timeoutSweepClock` interface):
   - **Legacy:** restore v6 `overdueTimeoutInferences(state, now, ...)` using `StartedAt` / `ConfirmedAt`, `user.TimeoutBuffer`, and existing grace constants; skip non-positive anchors.
   - **Header:** keep `TimeoutRemaining` / floor gate (current behaviour).
2. Apply the same split to `summarizeEscrowHoldInFlight` pending deadlines (v6 used `deadlineFromStamp(inference.StartedAt, ...)`).
3. Unit tests: legacy sweep selects nonce when wall past, not when stamp floor says not due; header sweep unchanged.
4. Document in plan: auxiliary gateway loops (sweep, hold) must respect active clock, not only `TimeoutDeadline` / verifier paths.

---

## Medium

### 4 — Gateway shadow is poll-frequency sensitive

**Fixed.** The shadow is sampled once the wall arm is due. Gaps within `TimeoutBuffer` do not count. The warn carries `mismatch_cause`, and a missing stamp does not increment the counter.

**Problem.** Legacy shadow runs on **every** `TimeoutDeadline` call. Stamp time at `StartedAtHeight` is usually a few seconds before `sendTime`, so whether `wallDue == stampDue` depends on poll timing (sleep loop, `inferenceConfirmed` doubles calls per refusal wait). Heavy traffic → more “disagreement” samples; light traffic → fewer. That is not a stable measure of production divergence. Missing stamp with no feed also counts as disagreement (both sides), flooding counters when legacy is meant to run without trusting the map for **active** due.

**Proposed fix.**

1. Observe shadow only when the **active** (wall) arm **transitions** to due, or at most once per `(nonce, kind)` while wall not yet due and clocks disagree (tighter than mismatch counter: compare at decision boundary).
2. Optionally require `|wall_deadline - stamp_deadline| > epsilon` before increment (plan “deadline within a small epsilon”).
3. Label or log `mismatch_cause`: `stamp_early`, `stamp_late`, `stamp_missing`, `both_due`.
4. Adjust unit tests to assert counter does not increment on benign double-poll when clocks agree.

---

### 5 — Performance: refusal wait amplifies header map reads

**Fixed.** `inferenceConfirmed` does not observe the shadow. The header map is read only when the wall arm is due, and that sample is remembered.

**Problem.** `sleepRefusal` calls `inferenceConfirmed` → `TimeoutDeadline` → `observeLegacyClockShadow` → `stampHeightTime` → `Source.StoredAt` (mutex). Every receipt wakes **all** refusal waiters; each waiter may call twice per iteration. Under load this is O(waiters) lock contention on one map per process.

**Proposed fix.**

1. Split “deadline for sleep/vote” from “legacy shadow observe”: e.g. `TimeoutDeadline` accepts `observeShadow bool`, or export `timeoutDeadlineForWait` without shadow.
2. Run shadow only from `WaitTimeoutDeadline` / `VoteTimeout` paths that matter for mismatch logging, not from `inferenceConfirmed`.
3. If shadow must stay on deadline: cache last `(height, hash) → ms` per session with short TTL or invalidate on header feed notify (already have `confirmWait` / height notify patterns).

---

### 6 — Inconsistent shadow semantics (gateway vs verifier)

**Fixed.** Gateway and verifier both classify the sample with `headertime.CompareLegacyShadow`. The stamp deadline uses the same slack as the active wall deadline, and `LegacyShadowEpsilon` is the only slack. A missing stamp is logged and does not increment. Active wall deadlines are unchanged.

**Problem.**

- Gateway legacy shadow: `now` vs `time.UnixMilli(stampMs).Add(legacyTimeoutDuration)` — no `Decide`, includes `TimeoutBuffer` in duration.
- Verifier legacy shadow: `headertime.Decide(..., interval, ..., CallerVerifier)` — AnswerWait semantics, no buffer on wall active path.

Same stamp/map row can yield “mismatch” on one side only, or opposite `stamp_due` in logs for the same nonce.

**Proposed fix.**

1. Pick one shadow definition in the plan (recommend: `Decide` with gateway interval + caller, **without** buffer on stamp side to mirror “when would header mode open”, **or** explicitly document gateway as “wall+buf vs stamp+buf” and verifier as “wall vs Decide”).
2. Extract `legacyStampDue(now, stampMs, missing, timeoutSeconds, caller, buffer time.Duration) bool` used by both gateway shadow and verifier shadow (active paths unchanged).
3. Align B7 log fields so comparisons across sides are interpretable.

---

### 7 — No detection of mixed clock deployment

**Accepted.** Reading `heightsync_timeout_clock` from the chain is the intended source. The flag is frozen on the escrow, and gateway and hosts take it from that escrow, so the clock is already escrow-bounded. No extra cross-party check, host status field, or cache-staleness work is required for this finding.

---

## Low

### 8 — `EvalDue` legacy helper vs production

**Fixed.** `EvalDue` and `wallPast` are removed. Acting deadlines go through `Due`: the deadline instant is due, a missing gateway anchor is unarmed, and a verifier wall of Unix 0 is the epoch. Header mode follows the stamp. The legacy sample is `CompareLegacyShadow`. `refusalDeadlineLocked` already calls `Due` and has no `eval.Active.Wall` branch.

**Problem.** B4 tests pin `wallPast` (strict `After`, zero wall fail-closed). Production legacy gateway/verifier do **not** call `EvalDue` for the active decision (v6 uses `!now.Before(deadline)` and integer `>=`). `refusalDeadlineLocked` still contains dead `eval.Active.Wall` branches in header-only callers.

**Proposed fix.**

1. Comment `EvalDue` as “header active + legacy **shadow** only; do not use for v6 active gateway deadline”.
2. Remove or guard dead `Active.Wall` branches in header-only functions if static analysis shows unreachable.
3. Keep B4 tests as specification for shadow math if shadow is unified (finding 6).

---

### 9 — `clockMismatchOnce` retention

**Fixed.** `dropSealedOutcomesLocked` also walks `clockMismatchOnce`. A sample whose inference was sealed past the retention window is dropped even when that nonce never had an outcome. A sample inside the window, and one that was never sealed, stays.

**Problem.** Prune in `dropSealedOutcomesLocked` only deletes keys for nonces present in `nonceStates`. A mismatch recorded for a nonce never tracked in `nonceStates` (e.g. sweep-only path) leaks until session teardown.

**Proposed fix.**

1. On `claimClockMismatchLocked`, if nonce is sealed/closed, skip claim or prune immediately.
2. Or cap map size / TTL for entries whose inference left live map (mirror `forgetInference` on host).

---

### 10 — Minor v6 parity gaps

**Fixed.** The verifier refusal path declines a payload whose `StartedAt` differs from the record. `QueuedConfirmStart` still keeps the earliest `ProposedAt`; that choice is documented as signature-stable in [async-header-time-timeout.md](../../async-header-time-timeout.md).

**Problem.**

- `VerifyPayload` no longer compares `p.StartedAt` to `rec.StartedAt` (v6 did).
- Mempool `QueuedConfirmStart`: earliest **proposed** wins vs v6 earliest `confirmed_at`.

Unlikely to affect timeout clock directly; worth noting for “exact v6” claims.

**Proposed fix.**

1. Restore `StartedAt` check on verifier refusal path only if product still wants binding (separate small PR).
2. Document mempool change in async-header-time docs if intentional for signature stability.

---

### 11 — Debug endpoint scope

**Fixed.** Each escrow entry carries `last_mismatch` only when the process's latest sample names that escrow. The top-level sample stays process-wide and already includes `escrow`.

**Problem.** `GET /v1/debug/timeout-clock`: `mismatch_total` and `last_mismatch` are process-global; per-escrow entries carry mode. Multi-escrow gateway can show header mode on escrow A and last mismatch from escrow B.

**Proposed fix.**

1. Filter last sample by escrow when building per-escrow entries, or label `last_mismatch.escrow` prominently in top-level JSON (already present — document).
2. Optionally per-escrow counters (higher cardinality — only if needed).

---

### 12 — Documentation and ship blockers

**Fixed.** The plan status is Done, with the seal and settlement-drain caveat. B4 and B5 describe `Due`: legacy refusal ages `sendTime` plus `TimeoutBuffer`, a zero `sendTime` falls back to `StartedAt`, and the deadline instant is due. The header-time sources (`due.go`, `protocol_due.go`, `legacy_shadow.go`, `mismatch.go`, and their tests) ship with Track B. `due_test.go` is gone with `EvalDue`.

**Problem.**

- Plan header still says `Status: Planned` while table marks B7/B8 Done.
- B4 text: legacy wall “strictly after” vs production `deadlineNotReached` / zero `sendTime` behaviour.
- B5 text: “refusal opens on `started_at`” vs production primary anchor `sendTime` (+ record when restart).
- CI: `due.go`, `mismatch.go`, tests untracked → undefined symbols on PR branch.

**Proposed fix.**

1. Set plan status to **In progress** or **Done** with explicit caveats (findings 1–3).
2. Align B4/B5 wording with v6 `sendTime` refusal and buffer.
3. **Commit** `chainoracle/blocks/headertime/due.go`, `due_test.go`, `mismatch.go`, `mismatch_test.go` with Track B.
4. Refresh audit doc with “open” rows for findings 1–3 until fixed.

---

## Recommended fix order

1. **Ship:** add missing `headertime` files (12) — fixed.
2. **Metrics:** registry wiring (1) — fixed.
3. **E2E:** host clock env (2) — fixed.
4. **Legacy ops:** sweep + hold (3) — fixed.
5. **Shadow quality:** sampling (4), refusal-wait reads (5), and semantics (6) are fixed.
6. **Hardening:** map pruning (9) and the doc pass (12) are fixed. Issue 7 is accepted. `EvalDue` (8) is fixed.

---

## Out of scope for this review

- Track A chain branch merge timing.
- Dispute / rollback when map disagrees with carried time (warn-only today).
- Changing AnswerWait or vote-only apply.
- Flipping `heightsync_timeout_clock` on live escrows mid-session.
