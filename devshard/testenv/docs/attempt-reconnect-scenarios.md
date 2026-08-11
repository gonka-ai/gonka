# Same-nonce reconnect citest scenarios

End-to-end Docker Compose coverage for **attempt-reconnect** (gateway same-nonce
resume after a mid-stream host drop). Implementation plan:
[`../../docs/gateway-attempt-reconnect-plan.md`](../../docs/gateway-attempt-reconnect-plan.md)
(Step 7). Design overview:
[`../../docs/gateway-streaming-ha-overview.md`](../../docs/gateway-streaming-ha-overview.md).

Stack layout and general citest how-to: [`scenarios.md`](./scenarios.md).
Always-stream / aggregate suites (orthogonal): [`streaming-ha-scenarios.md`](./streaming-ha-scenarios.md).

---

## How to run

**Prerequisites:** Docker, Go toolchain, linux `devshardd` (built by the target).

```bash
cd devshard/testenv
make citest-attempt-reconnect
```

What that target does (order matters):

1. `make build-devshardd DEVSHARD_VERSION=v2` — protocol-gate negative path
2. `make citest-images` — compose images (mock-chain, mock-openai, gateway, …)
3. `TESTENV_CITEST=1 go test -tags=testenvci ./citest/ -run
   'TestAttemptReconnect_AdminEnables|TestAttemptReconnect_V2ProtocolSkipsSameNonce'`
4. `make build-devshardd DEVSHARD_VERSION=v5 DEVSHARD_BUILD_TAGS=testenvci` —
   enables the host primary-detach fault injector
   (`host/livestream_fault_testenvci.go`)
5. Run `TestAttemptReconnect_V5MidStreamDetachResumesSameNonce`
6. Rebuild `devshardd` as v2 again (leave the tree in the default citest version)

| Variable / tag | Purpose |
|----------------|---------|
| `TESTENV_CITEST=1` | Opt-in gate (`harness.SkipUnlessEnv`) |
| `-tags=testenvci` | Citest build tag on the test binary |
| `DEVSHARD_BUILD_TAGS=testenvci` | Compiles the detach injector into **devshardd** (release builds omit it) |
| `DEVSHARD_VERSION=v5` | Session / escrow protocol stamp required for same-nonce resume |

Run a single test after images + the matching `devshardd` exist:

```bash
# v2 leg (admin + protocol gate)
make build-devshardd DEVSHARD_VERSION=v2
TESTENV_CITEST=1 go test -tags=testenvci ./citest/ \
  -run 'TestAttemptReconnect_AdminEnables|TestAttemptReconnect_V2ProtocolSkipsSameNonce' \
  -count=1 -v -timeout 30m

# v5 resume leg (needs testenvci-tagged host binary)
make build-devshardd DEVSHARD_VERSION=v5 DEVSHARD_BUILD_TAGS=testenvci
TESTENV_CITEST=1 go test -tags=testenvci ./citest/ \
  -run TestAttemptReconnect_V5MidStreamDetachResumesSameNonce \
  -count=1 -v -timeout 30m
```

`make list-citest-targets` includes `citest-attempt-reconnect` for the CI matrix.

**Subnet note:** full-stack citests share `172.31.0.0/24`. Do not run two stacks on
one host at once; clean leftover `citest-*` compose projects/networks if a prior
run was killed mid-flight.

---

## Landed citest

**Sources:** `citest/attempt_reconnect_test.go`, helpers in
`citest/harness/reconnect.go` (`BootReconnectStack`,
`BootReconnectStackWithPrimaryDetach`, `PatchGatewayAttemptReconnect`,
`RequireGatewayReconnectResumed`, …).

| Test | Plan scenario | What we validate |
|------|---------------|------------------|
| `TestAttemptReconnect_AdminEnables` | Settings plumbing | `POST /v1/admin/settings` with `attempt_reconnect_*` applies in-process |
| `TestAttemptReconnect_V2ProtocolSkipsSameNonce` | Mid-stream drop, **v4/v2**, reconnect on | `PartialStream` ML fault → no silent same-nonce resume; clean failure |
| `TestAttemptReconnect_V5MidStreamDetachResumesSameNonce` | Mid-stream drop, **v5**, reconnect on | Primary detach after N LiveStream writes → one continuous completion, `winner_reconnect_resumed` in gateway logs, exactly one finished inference |

Admin knobs (in-memory until Step 8 DB columns):

```json
{
  "redundancy": {
    "attempt_reconnect_enabled": true,
    "attempt_reconnect_budget_ms": 2000,
    "attempt_reconnect_max_tries": 2
  }
}
```

v5 fault injection: compose env
`DEVSHARD_TEST_DETACH_PRIMARY_AFTER_WRITES` on every versiond service (patched by
`PatchComposeDetachPrimaryAfterWrites`). Only active in a `testenvci`-tagged
`devshardd`.

---

## Plan scenarios → coverage

Numbering matches [gateway-attempt-reconnect-plan.md](../../docs/gateway-attempt-reconnect-plan.md)
Step 7.

| # | Scenario | Citest | Notes |
|---|----------|--------|-------|
| 1 | Mid-stream drop, v5, reconnect on — one completion, one finish | ✅ `TestAttemptReconnect_V5MidStreamDetachResumesSameNonce` | Happy path via primary detach |
| 1a | Resume after drain+forget (payload-store tier) | — | Unit / host LiveStream tests stronger today |
| 1b | Cursor inside RAM window | — | `host/livestream_test.go` |
| 1c | Cursor older than window → spool or clean escalate | — | `host/livestream_test.go` |
| 2 | Mid-stream drop, ≤v4, reconnect on — no same-nonce | ✅ `TestAttemptReconnect_V2ProtocolSkipsSameNonce` | Behavior only; `skipped_protocol` counter waits on Step 8 |
| 3 | Host permanently down after drop, v5 — escalate after budget | — | Follow-up citest (host-kill harness) |
| 4 | Streaming + non-streaming, fixed seed — content/usage agree | — | Follow-up citest |
| 5 | Helpers (`PartialStream`, detach injector, admin patch) | ✅ | Used by landed tests |
| 6 | R4 winner continuity e2e | — | Unit: `redundancy_reconnect_test.go` |
| 7 | Second drop after resume — no second ladder | — | Unit: `TestRunInference_V5SecondDropAfterResumeDoesNotRerunLadder` |
| 8 | Reconnect while client already gone | — | Follow-up citest |

### Cross-instance HA (plan Step 10 — deferred)

| ID | Scenario | Status |
|----|----------|--------|
| HA1–HA4 | Sticky `devshardd` reboot / failover mid-stream; ML job survives; another replica reattaches | Deferred until mock-ML split (`ak/devshard-observability-e2e`) + MLNode keep-alive ([issue #1466](https://github.com/gonka-ai/gonka/issues/1466) §4). Topology citest (leases / rolling update) already exists under [`scenarios.md`](./scenarios.md). |

---

## Unit companions (not citest)

| Area | Package / files |
|------|-----------------|
| Ladder / winner continuity / second drop | `cmd/devshardctl/redundancy_reconnect_test.go` |
| LiveStream spool / cursor / trim | `host/livestream_test.go`, `host/spool.go` |
| Shared scratch spool | `spool/` — [`../../docs/spool-shared-library.md`](../../docs/spool-shared-library.md) |

---

## Related

| Doc | Role |
|-----|------|
| [`../../docs/gateway-attempt-reconnect-plan.md`](../../docs/gateway-attempt-reconnect-plan.md) | Implementation checklist |
| [`streaming-ha-scenarios.md`](./streaming-ha-scenarios.md) | Force-upstream + pointer here |
| [`scenarios.md`](./scenarios.md) | Core stack citest index |
| `make citest-force-upstream-streaming` | Always-stream client shape / aggregate spill |
