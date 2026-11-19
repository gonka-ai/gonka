# Proposal: participant lists — hold the narrowing, add an unthrottled list

**Status:** Implemented
**Scope:** `devshard/cmd/gateway` — `scheduler`, `config`, `env`, `warmup`, `api`, package docs
**Related:** [routing.md](../../cmd/gateway/docs/routing.md) · [capacity.md](../../cmd/gateway/docs/capacity.md) · [rules.md](../../cmd/gateway/docs/rules.md) §1, §8 · [scheduler/README.md](../../cmd/gateway/scheduler/README.md)

Two changes land together because they are the two halves of one operator complaint: the narrowing does not hold, and the participants it names are still throttled and still lose nonces.

Every claim about current behaviour below was read in the code on `custom/gateway/v5` and is cited inline.

---

## Problem

An operator sets `participant_allowlist` expecting two things: that requests reach only the named participants, and that those participants are treated as the ones this gateway is here to serve. Neither holds today.

**The narrowing leaks.** Not through the dispatch ladder, which is correct, but around it — through the admin API, through the warmup prober, and through administrative fan-out.

**The named participants get no privilege at all.** They pass the same seven gates as anyone else, so a congested or ejected host of your own loses its nonce to a ghost burn exactly as a stranger's would.

**And the narrowing is expensive in a way its configuration does not suggest.** The protocol binds an executor to a nonce positionally (`hostIdx = nonce % groupSize`), and a declined binding does not consume the nonce — `PrepareInferenceFn` returns without advancing, so the next caller sees the same nonce bound to the same host ([user/session.go:1184-1199](../../user/session.go)). Reaching the next slot costs the nonce standing in front of it. With `mine` slots of a group of `size`, a hard narrowing therefore burns `size/mine - 1` nonces for every request served.

Each of those burns is a real `MsgStartInference` that debits the escrow `(60 + 64) × token_price`, settles as a finished inference, and credits the validator of the slot it landed on: `settleLiveRecordLocked` turns a still-Pending record into Finished with `HostStats[ExecutorSlot].Cost += ReservedCost` ([state/machine.go:1679-1692](../../state/machine.go)), and the chain pays it out as `validatorPayouts[addr] += hs.Cost` where `addr = escrow.Slots[hs.SlotId]` ([msg_server_settle_devshard_escrow.go:103](../../../inference-chain/x/inference/keeper/msg_server_settle_devshard_escrow.go)). A narrowing to your own hosts pays every excluded participant a toll for each step past them.

The slot layout is not a gateway setting. `GetSlotsFromSorted` samples slots weight-proportionally from the epoch group, seeded by `appHash + escrow id + model` ([calculations/slots.go:54](../../../inference-chain/x/inference/calculations/slots.go)), and `CreateEscrow` sends only an amount and a model ([escrow/commitments.go:78](../../cmd/gateway/escrow/commitments.go)) — a group composition cannot be requested. The burn rate of a hard narrowing is set by the operator's share of network weight.

**This bounds what the unthrottled list can achieve, and the bound is the reason to state it here.** Removing throttling and burns for named participants removes only the burns that land on *their* slots — `participant_window_full_no_send` and `participant_ejected_no_send`. The bulk, `participant_outside_allowlist`, lands on slots the list does not name and no privilege can reach. At two slots of sixteen, seven of the eight nonces per served request are outside-allowlist burns and stay.

---

## What is already correct, and stays untouched

The allowlist is the first rung of `firstBlock`, and nothing crosses it: `servingOverFullWindows` neutralises `blockWindowFull` alone, the forced overdraft runs only after `match` already returned `serve`, and `onlyThisHostIsLeft` is reached only from the `blockExcluded` arm ([scheduler/match.go:72-131](../../cmd/gateway/scheduler/match.go)).

The engine never chooses a participant. Both launch sites take the host off an `Assignment` produced by `Picker.Pick`, and no file under `engine/` reads `SlotParticipants()`, `ParticipantKeys()`, `HostParticipantKeyList()` or a registry host list. Escalation and retry re-enter the scheduler with the escrow pinned, so the full ladder applies again.

Neither is modified by this proposal.

---

## Part 1 — make the narrowing hold

### 1.1 The allowlist has no source but the override document

`PUT /v1/admin/settings` parses the body as a complete `Overrides` document ([api/admin.go:58](../../cmd/gateway/api/admin.go)), `Reconfigure` rebuilds the config from env plus that document ([app/operations.go:224-234](../../cmd/gateway/app/operations.go)), and `SaveOverrides` replaces the stored row whole ([store/overrides.go:29-42](../../cmd/gateway/store/overrides.go)). Replace semantics is deliberate — the audit line says "settings replaced".

`participant_allowlist` is the one narrowing signal with no env value and no default behind that document; `config/build.go:126` is its only writer in the tree. So any later PUT that does not resend the key — an operator changing `max_tokens_cap`, a deploy script, a UI posting only the fields it knows — drops it, and the scheduler takes the "no allowlist" branch everywhere: `refusedByAllowlist` returns nil, `outsideAllowlist` reads nil as "no block", and `reachableByAllowlist` admits every escrow. The failure is silent and fails open. A fresh or lost SQLite volume does the same, since `LoadOverrides` returns the zero value on `ErrNoRows`.

`config/allowlist_override_test.go:25` carries the comment "An override the operator did not send must not clear a list already in force", but its body never exercises that case; `Build` is stateless, so the asserted property does not exist.

**Decision: no env source. Both lists stay admin-API-only.** An env value behind the override was considered and rejected by the operator: these lists are operational state, changed while the gateway runs, and a second place to set them is a second place for them to disagree. The consequence is accepted rather than fixed — a settings document that omits a list still drops it, and a dropped narrowing still fails open.

**Change.** Because the hazard stands, make it observable at the moment that causes it: the `settings replaced` audit line in `api/admin.go` now carries both lists as they stand after the call. Under rules.md §8 this signal is fail-open on absence and fail-closed on presence, and with no config layer behind it that line is the only thing that distinguishes "nobody narrowed routing" from "somebody's PUT cleared the narrowing".

A standing narrator over config swaps was built first and removed. `journal/guard_test.go` states the rule it broke: lifecycle packages write through the journal, and only `api/admin.go` and `api/errors.go` may log directly, because they "write an operator's own action or a refused admin call, which belong to no lifecycle". Setting a routing list is an operator's own action, so the audit line is its sanctioned home — and putting the report in the scheduler's allowlist module, where the lists are read, is barred by the same guard unless it goes through the journal as a typed event, which is more machinery than one field is worth.

**Change.** Validate entry shape in `config/validate.go`, beyond the existing blank check: a bech32 `gonka1` prefix and a plausible length. A wrong-kind entry fails closed today — a short host label from a log or a Grafana `host=` tag matches nobody, `reachableByAllowlist` admits no escrow, and every request ends in `ErrAllowlistUnreachable` — which is loud but misdiagnosed as an outage. The correct source of entries is `GET /v1/admin/hosts`, which reports the full `participant_key`; say so in the operations doc.

**Not done, and now unfixable at this layer:** the property `config/allowlist_override_test.go:25` claims — "an override the operator did not send must not clear a list already in force" — cannot hold while the override document is the only source. The comment describes an intention the design has now explicitly declined.

### 1.2 The warmup prober sends a paid inference to whoever holds the slot

`dispatchProbe` discards the binding entirely — `func(user.HostBinding) (user.InferenceParams, bool, error)` — and accepts whatever slot the nonce landed on ([warmup/warmup.go:173](../../cmd/gateway/warmup/warmup.go)), then sends it with `SendOnly`. A fresh escrow is at nonce 1, so the probe always lands on slot 1 of the group, whoever that is. It is a real chat-completions inference charged to the escrow, it is on by default (`WarmNewEscrows: true`), and the prober is constructed from the config holder alone with no knowledge of the scheduler ([app/routing.go:58](../../cmd/gateway/app/routing.go)).

This is the one path on which a non-allowlisted participant receives an actual inference request.

**Decision: the prober is not gated, and this is not a leak.** The gate was built and then withdrawn. The probe teaches a host that the chain has already seated in this escrow's group, and that host will hold state for the escrow whether or not routing ever chooses it — the slot layout is the chain's, not this gateway's. Declining to teach it buys nothing and leaves a group member ignorant of an escrow it is on the hook for. What remains true is the narrower statement: the probe is the only non-request sender of a real inference, it is charged to the escrow, and an operator watching a non-allowlisted host will see it. That is documented in `routing.md` rather than filtered.

**Known limitation, not changed here.** `warmup.New` reads `WarmNewEscrows` once and returns nil when it is off, so that flag is structural and a runtime change does not reach it until restart.

### 1.3 Administrative fan-out stays as it is, and is documented

Host pings walk `HostDials()` of every live escrow ([hostping/targets.go:31](../../cmd/gateway/hostping/targets.go)); diff catch-up, finalize signature collection, the pending-diff flush on retirement and the timeout sweep all address the whole group. None carries a user prompt.

**Decision: no change.** Filtering probes by the allowlist would blind the gateway to hosts it may route to the moment the list changes, and reachability and clock drift are wanted for the whole group. **Change:** state explicitly in `routing.md` which paths ignore the list and why, so an operator seeing POSTs on a non-allowlisted host can tell this apart from a leak.

---

## Part 2 — unthrottled participants

### What it is

A second operator list naming participants whose quality-of-service gates this gateway declines to apply. When a nonce binds to one of them, a full congestion window and an outlier ejection no longer block it, the congestion tokens are taken as an overdraft rather than a fitted acquire, and the nonce is therefore not burned for either reason.

### What it is not

It does not make the cursor land on those participants more often — nothing can, the layout is the chain's. It does not override the safety gates. It does not make "never burn" total: two burns survive any privilege, and both must be named in the documentation rather than discovered in production.

- `ghostExclude` — every live waiter has already raced this host and excluded it, and another host can serve them ([match.go:174](../../cmd/gateway/scheduler/match.go)).
- `ghostAbandoned` — the nonce was committed as a serve and the caller vanished; the commit structurally precedes the delivery ([dispatcher_queue.go:239](../../cmd/gateway/scheduler/dispatcher_queue.go)).

### Naming

`unthrottled_participants`, not `priority_participants`. "Priority" would be read as routing preference, which is the one thing this list cannot deliver. The name also keeps distance from the chain's `ParticipantAllowList`, a governance gate on epoch weight computation ([chainvalidation.go:955](../../../inference-chain/x/inference/module/chainvalidation.go)) that shares the word and nothing else.

### Which gates it crosses

| # | Rung | Crossed | Why |
|---|---|---|---|
| 1 | Outside allowlist | yes | an unthrottled participant is admitted whether or not the allowlist names it |
| 2 | PoC required | **no** | the host owes the chain proof-of-compute and will not take work; the send is wasted |
| 3 | Congestion windows full | yes | protects the host from this gateway; a sanctioned bypass already exists — `Overdraft` |
| 4 | Cut-off | **no** | the host is already proven broken, and a send there spends exactly the nonce the privilege exists to save. `take` refuses it whatever the caller asks, "so the rule does not rest on the scheduler asking correctly" ([limits/participant.go:180-187](../../cmd/gateway/limits/participant.go)) |
| 5 | Ejected by the outlier detector | yes | already capped by `MaxEjectionFraction` and `MinAvailableHosts`, so honouring it can never empty the pool and skipping it cannot flood one |
| 6 | Escrow state diverged | **no** | a correctness valve; every further dispatch compounds the divergence |
| 7 | Excluded by this waiter | **no** | not a ceiling — the request itself refused this host after racing it |

The escrow-side gates — nonce ceiling, balance floor, retirement reserve — are untouched. They protect money and the chain's ability to settle, and rules.md §1 does not admit an exception.

### Edit points

1. `scheduler/gates.go`, `fleetGates` — build an `unthrottled(participant) bool` predicate from the settings snapshot, read per drain like the rest of the ladder.
2. `scheduler/match.go`, `firstBlock` — for an unthrottled participant, return `blockNone` at rungs 1, 3 and 5; rungs 2, 4, 6, 7 are asked unchanged.
3. `scheduler/dispatcher_queue.go`, the acquire in `drain` — take the slot as an overdraft unconditionally for an unthrottled participant, rather than only when the forced send is due.
4. ~~`scheduler/dispatcher.go`, `admit` — skip the `refuseSlot` fold for an unthrottled participant.~~ **Not needed, and left alone.** The fold records why the limiter refused a slot, and an unthrottled participant's slot is taken as an overdraft, which only a cut-off refuses. Folding a cut-off is correct: it is a true block that this list does not waive. There is no window-full refusal left to freeze the participant on.
5. `scheduler/allowlist.go`, `reachableByAllowlist` — an escrow whose group holds an unthrottled participant is reachable even when the allowlist names none of its slots.
6. `config` — `Scheduler.UnthrottledParticipants []string` and the `unthrottled_participants` override key, with the same shape validation as the allowlist. No env value: like the allowlist, it is set through `PUT /v1/admin/settings` and nowhere else.

`escrow_pick.go`'s burn forecast reads the same `blocks` ladder, so it follows without its own change — which is the reason the ladder has one definition.

### Four traps, each already verifiable in the code

**Do not change what `Admits` and `Available` answer.** `limits.Capacity` reads `ParticipantLimiter.Available` to size `EscrowWeight`, which sizes `effectiveConcurrencyLimit` on the gateway-wide door. A participant that always reports available would inflate the door for every model it serves. The privilege belongs in what the scheduler does with the answer, never in the answer.

**The overdraft is unbounded by design, and the backstop is elsewhere.** `Overdraft` takes the tokens whether or not they fit, so an unthrottled participant's in-flight total climbs without a ceiling of its own. Total concurrency is still bounded before the scheduler by `GatewayLimiter.AcquireForModel`, which is per model and refuses with 429. That is the honest reading of "regardless of the ceiling": the participant's own window is bypassed, the gateway's door is not.

**Backpressure changes shape.** `sweepExhausted` walks the same ladder, so a participant that never blocks means waiters are never swept and never answered `ErrHostsBusy`. That is correct only if the participant really does serve them; if it is slow rather than full, requests queue instead of being refused. The engine's own deadlines remain the bound.

**One drain's memo is keyed by participant alone** ([routing.md, "The drain"](../../cmd/gateway/docs/routing.md)). The new predicate keys on the participant, so the memo stays valid. A future per-model or per-request variant of either list would have to widen that key.

---

## Alternatives considered

**Drop the narrowing; keep only the unthrottled list.** Nonces landing on unnamed participants would be served rather than burned, taking the burn rate from `size/mine - 1` to zero and ending the toll paid to excluded validators. Rejected for now because it gives up the guarantee that a prompt never reaches a host outside the list. It becomes the right answer the moment the list is a preference about economics rather than a boundary about privacy — the decision is the operator's, and the measurement below is what should settle it.

**Keep the narrowing and attack the burn rate through rotation.** Slot layout is deterministic per escrow id, so holding more escrows than needed and steering traffic into the ones that happen to hold more of your slots would lower the burn rate — roughly halving it at a twelve-percent weight share, by the binomial spread rather than by measurement. Rejected for now: it locks capital and multiplies on-chain operations, and the measurement below should come first.

---

## Verification

`go build ./... && go vet ./... && golangci-lint run && go test -race ./...` for the tree.

Behaviour, as tests written red first:

- A settings document naming only the allowlist waives nothing.
- A list entry that is a host label rather than an address is refused when the settings document is built.
- An unthrottled participant is served over a full congestion window, and over an ejection, without a burn.
- An unthrottled participant is still burned for PoC-required, cut-off and state-diverged, and its escrow is still refused by the nonce ceiling and the balance floor.
- One refused acquire does not freeze an unthrottled participant for the rest of the drain.
- An escrow holding only an unthrottled participant is reachable when the allowlist names none of its slots.

Operationally, before choosing between this proposal and its first alternative: read `devshard_gateway_ghost_nonces_burned_total{reason}` over a representative period and compare the `participant_outside_allowlist` share against `size/mine - 1`. The metric already exists and needs no change.
