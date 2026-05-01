# Testenv stub engines — future design

> **Status:** design sketch for Phase 7+ work. Not yet implemented.
> **Scope:** stub `InferenceEngine` / `ValidationEngine` inside
> `devshardd-testenv`, configurable at runtime by a test orchestrator.
> **Testenv-only.** The REST control plane described here must never be
> compiled into, or even reachable from, production `devshardd`.
> See `devshard/docs/testenv.md` §Phase 7 for the short-form tracking
> entry and the container/wiring context.

---

## 1. Why this exists

Phase 7 in `testenv.md` currently sketches a tiny pair of stubs:

> `MockInferenceEngine`: deterministic completion, streams tokens via
> channel, latency configurable via env var for scenario tests.
> `MockValidationEngine`: returns `Valid` unless the request carries
> `X-Testenv-Inject-Fault: invalid` (used by protocol scenarios).

That is enough to boot a devshardd-testenv and pass the happy path. It
is **not** enough to exercise the real production shape, because in
production every host holds a pool of ML nodes and the `InferenceEngine`
dispatches inferences per-acquired-node through the `NodeManager`
(`devshard/mlnode/...`). Which means:

- A single host may serve concurrent inferences on different nodes.
- Failures are isolated per node (one flaky node must not taint the
  rest of the pool).
- Retries can land on a different node, so `ExcludedNodes` semantics
  must be preserved.

Protocol scenarios that depend on this heterogeneity (per-node latency,
per-node fault rate, "this node returns invalid tokens, that node is
slow, this node 500s after N requests") cannot be expressed via
process-wide env vars. We need:

1. **Per-node profiles** inside the stub engines, keyed by the `node_id`
   handed out by the (mock) `NodeManager`.
2. **A control-plane API** that lets the test orchestrator install,
   mutate and tear down those profiles at runtime, without restarting
   `devshardd-testenv`.
3. A watertight guarantee that the control plane never reaches
   production — it's a lie-to-devshardd interface, and its presence in
   a real deployment would be an integrity incident.

---

## 2. Design goals

| Goal | Notes |
|------|-------|
| Per-node behavior matches production granularity | Stubs dispatch per `node_id` the same way the prod engine dispatches per ML-node endpoint. |
| Deterministic by default | Each node profile carries an explicit seed; runs are reproducible across CI. |
| Reconfigurable at runtime | Orchestrator changes profiles without redeploying. Supports `BeforeEach` resets. |
| Bounded fault-injection vocabulary | Small, curated set of knobs (latency dist, failure rate, verdict override, payload template). Closed set; new knobs require a PR. |
| Testenv-only by construction | File layout + build tag + dependency rule + log banner. Four independent guardrails. |
| Observable | Every mutation is logged; every request records `node_id`, `profile_version`, and injected decision for Grafana/logs. |
| Idempotent control API | Orchestrator can reapply the same config any number of times; safe for retries. |

---

## 3. Component layout

```text
devshard/testenv/
├── engine/
│   ├── inference_mock.go        // MockInferenceEngine (stub)
│   ├── validation_mock.go       // MockValidationEngine (stub)
│   ├── profile.go               // NodeProfile type; pure data, shared with control plane
│   ├── registry.go              // in-process profile registry (map[nodeID]NodeProfile + lock)
│   └── engine_test.go
├── engine/controlplane/
│   ├── server.go                // REST API bound to the registry
│   ├── handlers.go
│   ├── types.go                 // JSON DTOs (distinct from engine.NodeProfile)
│   └── server_test.go
└── orchestrator/                // optional helper package for test Go code
    ├── client.go                // thin Go client for the REST control plane
    └── scenario.go              // Declarative apply(path to YAML) helper
```

Packaging rules:

- `devshard/testenv/engine` — may be imported **only** by
  `devshard/testenv/cmd/devshardd-testenv`.
- `devshard/testenv/engine/controlplane` — may be imported **only** by
  `devshard/testenv/cmd/devshardd-testenv`. In particular, no other
  package under `devshard/testenv/...` imports it (the mock-chain and
  height-sync containers don't need a control plane).
- `devshard/testenv/orchestrator` — may be imported **only** by test
  code (file names end in `_test.go`) and by `devshard/testenv/scenarios/...`.
- A new CI dependency-check row in `testenv.md` §8.4 enforces these.

---

## 4. `NodeProfile` — the shape of a simulated node

```go
// engine/profile.go
package engine

type PayloadMode string

const (
    PayloadModeEcho       PayloadMode = "echo"        // echo the prompt; deterministic
    PayloadModeHash       PayloadMode = "hash"        // sha256(prompt); stable across runs
    PayloadModeFixed      PayloadMode = "fixed"       // a caller-supplied string
    PayloadModeTemplate   PayloadMode = "template"    // format string over request fields
)

type LatencyDist string

const (
    LatencyDistFixed   LatencyDist = "fixed"   // exact ms
    LatencyDistUniform LatencyDist = "uniform" // [min,max] ms
    LatencyDistNormal  LatencyDist = "normal"  // mean±stddev ms
)

type NodeProfile struct {
    NodeID  string // must match the node_id returned by the mock NodeManager
    Seed    int64  // deterministic token / jitter seed

    // Inference behavior.
    Payload         PayloadMode
    PayloadTemplate string  // used only when Payload == PayloadModeTemplate/Fixed
    TokensPerChunk  int     // how many tokens per streaming chunk (1 = token-by-token)
    Latency         Latency

    // Failure injection.
    FailureRate   float64   // [0,1] probability of APPLICATION_ERROR per request
    FailureAfterN int       // deterministic: fail the N-th request then reset
    Timeout       bool      // if true, block until ctx cancels (simulate TIMEOUT)

    // Validation behavior (consumed by MockValidationEngine only).
    ValidationVerdict string // "valid" | "invalid" | "mismatch"
    ValidationEveryN  int    // if > 0: flip verdict every N requests
}

type Latency struct {
    Dist        LatencyDist
    Min, Max    time.Duration // for Uniform
    Mean, Stddev time.Duration // for Normal
    Fixed       time.Duration // for Fixed
}
```

A missing profile means "use the process-wide default" — which itself
is a `NodeProfile` the orchestrator can overwrite via the control
plane. This gives tests a "blanket default + targeted overrides"
workflow.

### 4.1 Request lifecycle inside the stub engine

1. `MockInferenceEngine.Execute(ctx, req)` is called by devshardd after
   acquiring a node via `NodeManager`. `req` carries the `node_id`.
2. Stub looks up `registry.Get(node_id)` with a read lock; falls back
   to `registry.Default()`.
3. Per-request overrides from `X-Testenv-*` headers (see §7) apply on
   top of the profile.
4. Latency is slept (respecting ctx).
5. If `FailureAfterN` or `FailureRate` fires, the stub returns the
   configured error type. Otherwise it streams tokens according to the
   payload mode.
6. Every decision (profile version, seed used, jitter draw, fault
   fired) is recorded in a structured log line. The log schema is part
   of the stable contract consumed by Grafana Loki dashboards.

---

## 5. REST control plane

### 5.1 Binding

The control-plane HTTP server is mounted on a **separate port** from
devshardd's public HTTP (e.g. `9200`), bound to `127.0.0.1` or to the
docker network only (never to `0.0.0.0`). The orchestrator reaches it
via the testenv docker network.

Env gate: `TESTENV_CONTROL_PLANE=1` — not set, no server is started.
Absent env var is the safe default; prod images never source
`.env.testenv`.

On startup, the server prints a banner so any accidental appearance in
a real deployment is loud:

```text
*** TESTENV CONTROL PLANE ENABLED on :9200 — DO NOT RUN IN PRODUCTION ***
```

### 5.2 Endpoints

All endpoints are JSON; no auth (trusted docker network); versioned
prefix `/testenv/v1`.

| Method | Path | Purpose |
|--------|------|---------|
| `GET`    | `/testenv/v1/health`               | returns `{ok: true, build: "<git sha>"}` |
| `GET`    | `/testenv/v1/nodes`                | list every profiled `node_id` and its current profile version |
| `PUT`    | `/testenv/v1/nodes/{nodeID}`       | set / replace the profile for one node; idempotent |
| `PATCH`  | `/testenv/v1/nodes/{nodeID}`       | merge partial fields into the existing profile |
| `DELETE` | `/testenv/v1/nodes/{nodeID}`       | drop the override (revert to default) |
| `PUT`    | `/testenv/v1/default`              | replace the process-wide default profile |
| `POST`   | `/testenv/v1/apply`                | atomic batch: replace default + set a list of per-node profiles in one call |
| `POST`   | `/testenv/v1/reset`                | drop all overrides; restore the built-in default |
| `GET`    | `/testenv/v1/events?since=...`     | SSE stream of decisions (profile hits, fault fires) — orchestrator consumes for assertions |

Versioning: every profile write returns `{version: N, profile: {...}}`.
Clients observe `version` in log lines and `events` stream to prove
their `PUT` landed before they started driving traffic through
devshardd.

### 5.3 Request / response shape

```http
PUT /testenv/v1/nodes/node-7
Content-Type: application/json

{
  "seed": 42,
  "payload": { "mode": "echo", "tokens_per_chunk": 2 },
  "latency": { "dist": "normal", "mean_ms": 120, "stddev_ms": 30 },
  "failure_rate": 0.0,
  "validation_verdict": "valid"
}

→ 200 OK
{
  "node_id": "node-7",
  "version": 3,
  "profile": { ...as applied, with defaults filled in... }
}
```

Batch apply:

```http
POST /testenv/v1/apply
{
  "default": { "payload": {"mode":"echo"}, "latency": {"dist":"fixed","fixed_ms":50} },
  "nodes": {
    "node-0": { "failure_after_n": 3 },
    "node-7": { "validation_verdict": "invalid" }
  }
}

→ 200 OK { "version": 17 }
```

### 5.4 Fault-injection endpoint (scheduled; deferred)

A dedicated `POST /testenv/v1/faults` endpoint is deferred until a
protocol scenario actually needs it beyond per-node knobs. Most faults
fit cleanly into `NodeProfile`. When/if we need cross-node faults (e.g.
"this entire host rejects every request for the next 5 seconds"),
extend this endpoint with a narrow, closed set of fault types.

---

## 6. Orchestrator protocol

`devshard/testenv/orchestrator` provides a small Go client:

```go
c, err := orchestrator.NewClient("http://devshardd-testenv-0:9200")
if err != nil { ... }
defer c.Close()

// Scenario C2: one node returns invalid verdict on every 3rd request.
_, err = c.Apply(ctx, orchestrator.Config{
    Default: orchestrator.Profile{ Payload: orchestrator.Echo },
    Nodes: map[string]orchestrator.Profile{
        "node-3": { ValidationVerdict: "invalid", ValidationEveryN: 3 },
    },
})
```

Declarative YAML form for scenario tests (loaded by
`orchestrator.ApplyFile`):

```yaml
default:
  payload: { mode: echo, tokens_per_chunk: 1 }
  latency: { dist: fixed, fixed_ms: 25 }
nodes:
  node-0: { failure_after_n: 5 }
  node-3: { validation_verdict: invalid }
```

Scenario tests in `devshard/testenv/scenarios/` use a
`BeforeEach → c.Reset(); c.Apply(...)` pattern so each case starts from
a clean state. `Reset` is explicit — not a side effect of a test
framework — so test authors can't accidentally leak state between
cases.

---

## 7. Per-request overrides

Scenario tests frequently need "this particular request behaves
differently". The stub engine honors a bounded set of `X-Testenv-*`
request headers **without touching the registry**:

| Header | Effect |
|--------|--------|
| `X-Testenv-Latency-Ms: 250`              | overrides latency for this request only |
| `X-Testenv-Inject-Fault: invalid`        | validation engine returns `Invalid` |
| `X-Testenv-Inject-Fault: application`    | inference engine returns `APPLICATION_ERROR` |
| `X-Testenv-Inject-Fault: timeout`        | inference engine blocks until ctx cancels |
| `X-Testenv-Payload-Template: <string>`   | forces `PayloadModeTemplate` with this template |
| `X-Testenv-Seed: <int>`                  | overrides the seed derived from node profile |

Both paths coexist: profiles set the steady-state behavior; headers
inject one-shot exceptions. The request header takes precedence.

---

## 8. Isolation from production

Four independent guardrails:

1. **Package location.** All control-plane code lives under
   `devshard/testenv/...`, which is never in the prod devshardd import
   graph. CI dependency-check (extension of §8.4 in `testenv.md`) fails
   if `devshard/host` or `devshard/internal/devshard-dapi-glue`
   transitively import any file under `devshard/testenv/engine`.
2. **Build tag.** `devshard/testenv/engine/controlplane/server.go`
   starts with `//go:build testenv`. The `Makefile` target that builds
   prod `devshardd` does not set this tag; the target that builds
   `devshardd-testenv` does. Even if the import rule above is
   accidentally violated, a prod build strips the control-plane
   implementation to an empty file.
3. **Env gate.** The binary only starts the control-plane listener
   when `TESTENV_CONTROL_PLANE=1`. A misconfigured env file cannot turn
   it on in a prod image because that image doesn't carry the
   control-plane code in the first place (per guardrail #2).
4. **Binary name.** The testenv binary is `devshardd-testenv`.
   Operator-facing tools (`versiond`, deployment scripts) refuse to
   manage a binary with that suffix in prod environments.

Additional low-effort belt-and-suspenders:

- Startup banner (§5.1).
- Metrics series name prefixed with `testenv_` so the observability
  stack cannot silently ingest testenv metrics into prod dashboards.
- Log field `testenv=true` on every control-plane event.

---

## 9. Observability hooks

Every profile mutation and every fault decision is emitted as a
structured log line with stable field names:

```text
level=info msg="testenv profile applied" testenv=true node_id=node-7 version=3 payload=echo latency_dist=normal mean_ms=120 stddev_ms=30
level=info msg="testenv fault fired" testenv=true node_id=node-3 request_id=... kind=validation_invalid profile_version=3
```

Grafana Loki filter `{container=~"devshardd-testenv.*"} |= "testenv=true"`
surfaces the complete timeline of control-plane and engine activity,
which is often the fastest way to diagnose a flaky scenario.

---

## 10. Determinism guarantees

- `NodeProfile.Seed` is mandatory (default `0` means "derive from node
  id", itself deterministic).
- The stub never reads from wall-clock time for generation — only for
  latency simulation. A test-only `TESTENV_FREEZE_CLOCK=1` env flag is
  reserved for future use.
- `Reset` restores the built-in default profile byte-identically.
- CI pins `GOGC`, `GOMAXPROCS`, and scenario test seed; the
  orchestrator's `Apply` acknowledges with a version number so test
  ordering is explicit.

---

## 11. Phased delivery

Rather than landing the whole design at once, we split Phase 7 into
three landable sub-phases:

- **Phase 7a — static stubs** (matches the current testenv.md sketch):
  `MockInferenceEngine` / `MockValidationEngine` with a single
  process-wide default profile, configured via env vars. Enough for
  Phase 8 (`devshardd-testenv` boot) and the happy-path scenarios.
- **Phase 7b — per-node registry**: introduce `NodeProfile`,
  `engine/registry.go`, and the in-process map. No REST yet; profiles
  are seeded via YAML at process start. Unlocks scenarios that need
  heterogeneous nodes but fixed across a run.
- **Phase 7c — REST control plane + orchestrator client**: mount the
  `/testenv/v1/*` surface behind the build tag + env gate, add the Go
  client package, document the YAML scenario shape, extend §8.4 CI
  dependency checks to enforce the isolation guardrails.

Each sub-phase is shippable and adds no regression risk to prod
builds.

---

## 12. Scenario → control-plane mapping (illustrative)

| Scenario (testenv.md §8.3) | Orchestrator op                                                                 |
|----------------------------|--------------------------------------------------------------------------------|
| C1 happy path              | `Reset()`; `Apply(default: echo, latency fixed 25ms)`                           |
| C2 invalid verdict         | Set one node's `validation_verdict: invalid`                                    |
| C2' double-signing         | Out of scope for stub engines; handled in HostManager test double               |
| C3 delayed carry           | Set one node's `latency.dist=fixed, fixed_ms=BLOCK_INTERVAL*3`                  |
| C13 route fairness refusal | Set one node's `failure_after_n=1` so the next request 500s                     |
| C14 quiet session          | No engine change; orchestrator pauses inference traffic; uses `events` stream   |

When a scenario doesn't fit the stub's vocabulary, the answer is
either (a) add a narrowly-scoped knob to `NodeProfile` in a follow-up
PR, or (b) implement the fault at a higher layer (bridge mock, oracle
mock). We do **not** grow the control plane with open-ended hooks.

---

## 13. Non-goals

- **Not a production feature.** The control plane never appears in a
  prod build (§8). If you think you need this in prod, you actually
  want an admin API on the real node manager; design that separately.
- **Not a load-testing surface.** Deterministic stubs are not a
  substitute for real ML inference at load; performance work lives in
  a different rig.
- **No TLS, no auth.** The control plane is LAN-bound inside docker.
  The four isolation guardrails make auth redundant; adding it would
  add moving parts without raising the security floor.
- **No persistence across devshardd-testenv restarts.** Profiles live
  in memory. Scenarios that need persistence re-apply the YAML at
  startup via `orchestrator.ApplyFile` on container start.

---

## 14. Open questions (roadmap parking lot)

- Should validation failure injection cross-reference the inference
  request that produced the challenged result? Current sketch is
  independent; some scenarios need the coupling.
- Fairness of the failure RNG across goroutines: if two inferences
  pick the same node concurrently, do they share the `FailureAfterN`
  counter? Current answer: yes (counter is per node). Document in
  `registry.go` when implemented.
- Cross-host coordination. Some scenarios want "host A returns invalid
  on request X, host B returns valid on the same request". This is
  an orchestrator concern, not a stub concern — orchestrator makes two
  `Apply` calls, one per host.
- Audit log retention: how long does the `events` SSE buffer headers?
  Current default: keep last 1000 in memory; expose a bounded
  `/testenv/v1/events/history` endpoint later if needed.

---

## 15. References

- `devshard/docs/testenv.md` §Phase 7 — short-form tracking entry that
  points here.
- `devshard/engine.go` — the prod `InferenceEngine` / `ValidationEngine`
  interfaces the stubs implement.
- `devshard/mlnode/` — the real node-manager gRPC surface the stubs
  mimic at the protocol boundary.
- `devshard/testenv/mockdapi/` — where the in-process `NodeManager`
  stub lives; it is the source of `node_id`s the control plane keys on.
