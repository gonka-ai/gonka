# Onboarding Observability — Logs & API by Stage

What this branch adds so an operator can see what a node is doing during
onboarding. Onboarding state is **derived at the admin handler layer**
(read-only; nothing is written back to the broker / `NodeState`). This
document maps each onboarding stage to (a) the log lines the API node
emits and (b) what the admin API returns.

Log subsystems used: `types.Nodes`, `types.Participants`.

## Surfaces

| Surface | Purpose |
|---|---|
| `GET /admin/v1/nodes` | Each node carries an `onboarding` block (states, timing, messages). |
| `POST /admin/v1/nodes/:id/test` | One-shot MLnode validation (load → health → inference). |
| `api selfcheck` (CLI subcommand) | Offline broker PoC intended-state self-test against a mocked chain. |
| Process logs | Emitted by the API node; the status logger keeps "waiting for PoC" visible. |

## The `onboarding` block

```json
{
  "participant_state": "INACTIVE_WAITING | ACTIVE_PARTICIPATING",
  "mlnode_state":      "WAITING_FOR_POC | TESTING | TEST_FAILED",
  "timing": {
    "current_phase":          "Inference | PoCGenerate | PoCValidate | ...",
    "blocks_until_next_poc":   1988,
    "seconds_until_next_poc":  11928,
    "should_be_online":        false
  },
  "user_message": "…",
  "guidance":     "…"
}
```

`mlnode_state` is `TEST_FAILED` **only** when the MLnode validation test
failed — never from the broker's operational `FAILED` status (an inactive
node failing with "no epoch models" is normal onboarding, not a test
failure). `timing` is omitted until the chain phase tracker has synced.

## Lifecycle stages

### A. Process start — chain not yet synced

Logs (demoted to Info; they are normal startup states, not errors):
```
INFO Cannot populate node epoch data yet: phase tracker not initialized (normal during startup)   [Nodes]
INFO Cannot populate node epoch data yet: epoch state not synced (normal until the chain syncs)    [Nodes]
INFO RegisterNode. Epoch data not available yet; will populate after the chain syncs               [Nodes]
```
API: `onboarding` present, `timing` omitted; `mlnode_state=WAITING_FOR_POC`;
`user_message="MLnode not yet validated - it will be tested before the next PoC"`
(or `"Waiting for next PoC cycle (schedule syncing)"` once a test has passed).

### B. Registered, chain synced, participant inactive (the long wait)

Logs (status heartbeat at startup and every 5 minutes while inactive):
```
INFO MLnode not yet validated - it will be tested before the next PoC   [Nodes]
```
Once at least one node has passed a test, the heartbeat becomes:
```
INFO Waiting for next PoC cycle (starts in 2h 15m) - validated, safe to be offline until 10m before PoC   [Nodes]
```
If the participant is inactive and the node's model isn't in the active
epoch, this is logged at Info (not Error):
```
INFO Participant not yet active - model assignment pending (normal for new participants)   [Nodes]
```
API:
```json
{
  "participant_state": "INACTIVE_WAITING",
  "mlnode_state": "WAITING_FOR_POC",
  "timing": { "current_phase": "Inference", "seconds_until_next_poc": 11928, "should_be_online": false },
  "user_message": "Participant not yet active - model assignment will occur after joining active set",
  "guidance": "MLnode will be tested automatically when there is more than 1h until next PoC"
}
```

### C. Pre-PoC auto-test running

A test is auto-triggered on register / config-change when there is more
than 1h until PoC. On every synced new-block epoch update the block
dispatcher also re-checks broker-registered nodes, so nodes loaded from
config at startup, or nodes whose first test was skipped while the schedule
was unknown or too close to PoC, get retried once the window opens. Tests
can also be run manually via `POST /test`.

The safety check that protects assigned/preserved/PoC nodes is best-effort: it
is a point-in-time snapshot, and the test then loads + stops the MLnode outside
the broker for up to a few minutes, so a node assigned mid-test could be
stopped. This is accepted on purpose — tests only run on idle nodes with >1h
until PoC, and the broker's next-block reconciliation re-brings-up any node left
stopped. Closing the window fully would need an atomic reserve-if-idle in the
broker, which the design avoids (no broker state/commands for onboarding UX).

API: `mlnode_state=TESTING`,
`user_message="Testing MLnode configuration - model loading in progress"`.

### D. Test passed

Logs (auto-test):
```
INFO Auto-test passed for MLnode   node_id=… duration_ms=…   [Nodes]
```
API: `mlnode_state=WAITING_FOR_POC`; the reassuring wording is now shown:
`user_message="Waiting for next PoC cycle (starts in 2h 15m) - validated, safe to be offline until 10m before PoC"`.

### E. Test failed

Logs (auto-test failure is reported at the API node):
```
ERROR Auto-test failed for MLnode before PoC   node_id=… failing_model=… error=…   [Nodes]
```
API: `mlnode_state=TEST_FAILED`,
`user_message="MLnode test failed: model 'Qwen/Qwen2.5-7B-Instruct' could not be loaded"`.

### F. PoC approaching (within the online-alert lead, 10 min)

API: `timing.should_be_online=true`; the message switches to:
`user_message="PoC starting soon (in 8m) - MLnode must be online now"`
(or, if not yet validated, `"… - MLnode not yet validated, bring it online now"`).

### G. Participant joins the active set

Logs (logged once on the inactive → active transition, then the heartbeat
goes quiet):
```
INFO Participant is now in the active set and participating   [Participants]
```
API: `participant_state=ACTIVE_PARTICIPATING`,
`user_message="Participant is in active set and participating"`.

## `POST /admin/v1/nodes/:id/test`

Runs load → health → inference against the MLnode and returns the result
(no broker mutation). `200` with the result body (status may be `FAILED`),
`404` if the node id is unknown, `409` if a test is already running or the
node is assigned, preserved, locked, reconciling, or participating in PoC.

```json
{
  "node_id": "wiremock",
  "status": "SUCCESS",                         // or "FAILED"
  "failing_model": "…",                        // present on failure
  "error": "…",                                // present on failure
  "load_ms":  { "Qwen/Qwen2.5-7B-Instruct": 4 }, // per-model load time
  "health_ms": 7,                              // health-check time
  "resp_ms":   31,                             // inference-request time
  "duration_ms": 44
}
```

## `api selfcheck`

Steps the real broker through synthetic PoC phases against a mocked chain and
asserts its intended-state transitions at each stage (it does not exercise a
real MLnode worker), printing a per-stage report; exit 0 on PASS, 1 on FAIL.

```
selfcheck: PASS
  [PASS] node-registered
  [PASS] epoch-models-populated
  [PASS] hardware-diff-submitted
  [PASS] poc-generate
  [PASS] poc-validate
  [PASS] inference-resumed
```
