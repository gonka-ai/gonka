# Host / MLNode ping observability — implementation and test plan

**Proposal:** [proposals/gateway-host-ping-observability.md](./proposals/gateway-host-ping-observability.md)
**Related:** dapi mlnode metrics federation ([PR #1469](https://github.com/gonka-ai/gonka/pull/1469)); `spool` promotion precedent ([spool-shared-library-plan.md](./spool-shared-library-plan.md) §D1, Phase 4)
**Status:** plan — Step 0 implemented (metrics restore + tests)

Two probers (gateway → `devshardd`, dapi → mlnode) need the same probe primitive: one cheap HTTP request per target per interval, yielding reachability, RTT, and clock divergence. This plan lands that primitive **once** in `common/probe`, then wires the two callers in dependency order so each step is shippable and observable on its own.

---

## D1 — Where the library lives: `common/probe`

**Decision: a new top-level `common/probe` package.**

The [spool plan](./spool-shared-library-plan.md) deliberately kept its substrate in `devshard/spool` and filed `common/spool` as an optional Phase 4, on the grounds that both consumers already lived in one module and promotion would force every change through three modules for no third-party caller. **That argument inverts here**, and the difference is mechanical rather than aesthetic:

| | spool | probe |
|---|---|---|
| Consumers | `devshard/cmd/devshardctl`, `devshard/host` | gateway (`devshard`), dapi (`decentralized-api`) |
| Module boundary crossed | no | **yes** |
| Verdict | stay local | `common/` from day one |

Both consumer modules already declare `common` and replace it with the local checkout (`devshard/go.mod`, `decentralized-api/go.mod`: `common => ../common`), so no new dependency edge is created — only a new package inside an edge that both modules already have.

**Why not `common/observability/probe`?** Acceptable, and a reasonable reviewer preference. `common/probe` is chosen because the package is an HTTP probe primitive whose output happens to be used for observability, and a top-level package keeps its import graph obviously stdlib-only. Nothing in the plan changes if the reviewer moves it.

### The split: library computes, callers publish

`common/probe` **must not touch Prometheus.** This is not stylistic — the two callers have incompatible metric plumbing:

| | Gateway | Dapi |
|---|---|---|
| Registry | custom `prometheus.NewRegistry()` (`devshard/cmd/devshardctl/metrics.go`) | **default** registry via `prometheus.MustRegister` (`decentralized-api/observability/metrics_prometheus.go`) |
| Metric prefix | `devshard_gateway_host_ping_*` | `dapi_mlnode_ping_*` |
| Identity label | `host` + `participant_key` (see Decisions) | `node_id` (`Node.Id`) |
| `/metrics` route | exists (`gateway.go`) | **removed in [#1482](https://github.com/gonka-ai/gonka/pull/1482)** — restore in Step 0 |

So the library emits a plain `Result` and each caller owns instruments, label names, and deletion. This also keeps `github.com/prometheus/client_golang` **indirect** in `common/go.mod` (it is indirect today); a registry-aware library would promote it to a direct dependency of the module every service imports, for no benefit.

| Shared (`common/probe`) | Stays with the caller |
|---|---|
| Probe execution, `httptrace` warm/cold detection | Metric registration + `DeleteLabelValues` cleanup |
| Timestamp parsing (ping headers/JSON, `Date` fallback) | Label naming (`host` vs `node_id`) |
| NTP four-timestamp math, monotonic/wall discipline | Target discovery (escrow refcount vs broker inventory) |
| Capability cache + demote rules | Config/env names, kill switch |
| Scheduler: ticker, single-flight, worker cap, jitter | Ownership of the goroutine lifecycle |
| Server-side ping `http.Handler` (Go targets) | Framework mounting (Echo wrap, middleware bypass) |

---

## Proposed API (`common/probe`)

Stdlib only: `context`, `errors`, `fmt`, `net/http`, `net/http/httptrace`, `strconv`, `sync`, `sync/atomic`, `time`.

```go
type Kind uint8 // KindNone, KindPing, KindDate, KindHealth

type Target struct {
    Key         string // metric label value: dial host (gateway) or node_id (dapi)
    PingURL     string // "" disables ping discovery for this target
    FallbackURL string // cheap liveness; required
}

type Result struct {
    Key        string
    Up         bool
    Kind       Kind          // how this probe was performed
    RTT        time.Duration // delay (server processing subtracted when KindPing)
    ConnReused bool          // false => cold dial, exclude from warm RTT series
    At         time.Time     // prober wall clock, for the freshness gauge

    Divergence       time.Duration
    HasDivergence    bool // false => emit no divergence sample (never 0)
    DivergenceSource Kind // KindPing (sub-ms) or KindDate (~1s)

    Err error
}

// Sink is the caller's metric adapter. Observe is called once per target per
// tick; Forget is called when a target leaves the set.
type Sink interface {
    Observe(Result)
    Forget(key string)
}

type TargetSource interface { Targets() []Target } // snapshotted once per tick

type Config struct {
    Interval        time.Duration // must be < transport IdleConnTimeout
    Timeout         time.Duration // per target, must be << Interval
    Concurrency     int
    Jitter          time.Duration
    CapabilityTTL   time.Duration // re-probe ping after this long on fallback
    Transport       http.RoundTripper // caller supplies a probe-only transport
    Clock           func() time.Time  // injectable for tests
}

func New(cfg Config) (*Prober, error)
func (p *Prober) ProbeOnce(ctx context.Context, t Target) Result
func (p *Prober) Invalidate(key string) // version change observed => re-discover ping

func NewScheduler(p *Prober, src TargetSource, sink Sink, obs SchedObserver) *Scheduler
func (s *Scheduler) Run(ctx context.Context) // returns on ctx cancel

// SchedObserver reports the job's own health (see proposal §5).
type SchedObserver interface {
    TickStarted()
    TickSkipped()
    TargetCount(int)
}

// Handler serves the ping contract for Go targets: 204 + X-Server-Recv-Ns /
// X-Server-Send-Ns, unix nanoseconds, no allocation beyond the header writes.
func Handler(now func() time.Time) http.Handler
```

Invariants the API enforces rather than documents:

- `Config.Interval >= Config.Timeout * 2` and `Interval > 0` → `New` returns an error otherwise. Prevents the tick-overrun regime by construction.
- `HasDivergence == false` whenever no timestamp parsed; callers cannot accidentally publish a zero.
- `Prober` holds no metric state, so a caller that forgets `Forget` leaks only its own series — a bug the caller's own test can catch.

---

## Decisions (resolved)

| # | Question | Decision |
|---|---|---|
| D1 | Dapi `/metrics` exposure | **Public behind the nginx `metrics_zone`**, same pattern as [`/v1/mlnodes/metrics` in #1469](https://github.com/gonka-ai/gonka/pull/1469). Also restore the ML-port `9100` scrape path that Prometheus already targets. |
| D2 | Fleet-wide RTT histogram | **In phase 1** (Step 2): `_rtt_seconds` histogram with **no** `host` / `participant_key` label, alongside the per-target gauges. |
| D3 | Host ↔ participant join | **Publish `participant_key`**: Info/gauge mapping series `{host, participant_key}` so dashboards do not need an external join. Probe gauges stay keyed by dial `host` (one probe per target); the mapping series carries the participant dimension. |
| D4 | `devshardd` ping path | **Child mounts `GET /ping`** (root, like child `/healthz`). **On the wire through versiond the URL is versioned:** `{host}{RoutePrefix}/ping` (e.g. `http://host:8080/devshard/v2/ping`). Never bare `/ping` on the versiond listen address — see below. |

### Path model: versiond sits in front of every child

`devshardd` does **not** listen on the public host port. `versiond` does (`versioned/cmd/versiond/main.go`). Its proxy (`versioned/internal/proxy/proxy.go`) treats the first path segment as the **protocol/binary version name**, strips it, and forwards the remainder to that child:

| Client URL | What answers |
|---|---|
| `GET /healthz` | **versiond supervisor** status (mux-owned) — *not* a child |
| `GET /{version}/healthz` or `/devshard/{version}/healthz` | child `devshardd` `/healthz` |
| `GET /metrics` | versionless obs → **primary** child `/metrics` |
| `GET /{version}/ping` or `/devshard/{version}/ping` | child `/ping` (this plan) |
| `GET /ping` (no version) | **404 / not a child** — not in `isVersionlessObsPath` today |

So the open question was a false dichotomy. `/v1/ping` is the wrong name: `v1` would look like a versiond version slot, not an API namespace. The child path is `/ping`; the probe URL always carries the version the gateway already knows from the escrow’s `RoutePrefix` (`/devshard/<version>`), the same prefix used for inference (`transport.Client`).

**Implications for Surface A:**

- Phase 1 fallback is `{host}{RoutePrefix}/healthz` (child), **not** `{host}/healthz` (supervisor). Using bare `/healthz` would measure versiond, not the binary that serves the escrow — and would never return ping timestamps after Step 3.
- `PingURL` / `FallbackURL` are built from the dial target **plus** a `RoutePrefix` stored with the target (from the escrow/runtime that put the host in the set). If several versions keep the same dial host alive, pick any live prefix for that host (or the newest); one probe still represents path RTT to that host’s serving child. Do **not** add `/ping` to versionless fan-out/primary unless a later change explicitly wants “primary child only” semantics (different signal).
- Nginx / join-proxy allowlists must permit `/{version}/ping` and `/devshard/{version}/ping` (or the host’s sticky-router equivalent), not a bare `/ping` location alone.

---

## Implementation steps

Ordered so that each step is independently shippable. **Step 0 is a regression fix and lands first** — without it, Surface B (and every existing `decentralized_api_*` gauge) has nowhere to be scraped.

### Step 0 — Resurrect dapi `/metrics` (regression from `b53fd8fcd`)

**Blocks everything on Surface B. Do this before probe work.**

[PR #1469](https://github.com/gonka-ai/gonka/pull/1469) shipped public mlnode **federation** (`GET /v1/mlnodes/metrics`) and that route is still bound today. What `b53fd8fcd` ([PR #1482](https://github.com/gonka-ai/gonka/pull/1482)) accidentally removed is dapi's **own** Prometheus exposition that coexisted with #1469 on the ML server (port `9100`). Restore that contract **exactly as it was at the #1469 merge (`ee21535`)**, then add the decided public `metrics_zone` front door.

**Behaviour at #1469 / pre-`b53fd8fcd` (source of truth — restore this):**

```go
// decentralized-api/internal/server/mlnode/server.go  (ee21535)
e.GET("/metrics", echo.WrapHandler(
    observability.MergedMetricsHandler(devshardobservability.Registry())))
e.GET("/sd/devshardd", s.getDevshardSDTargets)
// …
s.e.Server.ConnState = devshardobservability.ConnState("ml")
```

| Piece | Exact restore | Notes |
|---|---|---|
| `GET /metrics` on ML server (`DAPI_API__ML_SERVER_PORT=9100`) | yes | Serves default registry via `promhttp`. At #1469 this was `MergedMetricsHandler(devshardobservability.Registry())` so in-process `devshard_*` series appeared too. |
| `GET /sd/devshardd` HTTP SD | yes | Same JSON target-group shape: `versiond:8080` + `__metrics_path__=/{version}/metrics`. Scrape job in `deploy/join/observability/prometheus.yml` still points here. |
| `ConnState("ml")` on ML server `Start` | yes | Same helper; re-import only if dapi still can (or move the tiny helper later — do not invent a different hook). |
| Public `/v1/mlnodes/metrics` | **already present** | Do **not** re-implement #1469. Leave federation alone. |

**Adaptation for post-extraction (required for "exactly as #1469" to compile today):** dapi no longer hosts an in-process `devshardobservability.Registry()`. Use:

```go
e.GET("/metrics", echo.WrapHandler(observability.MergedMetricsHandler(/* no extras */)))
// equivalent: observability.MetricsHandler()
```

That is the same handler family and the same default-registry contents `#1469`-era scrapers expected for `decentralized_api_*`; only the now-absent in-process `devshard_*` merge argument is dropped. Do **not** re-add a `devshard` module dependency solely to merge an empty registry.

**Public exposure (Decision D1 — new relative to #1469-era internal scrape):**

- Add nginx locations for dapi's own metrics behind the existing `metrics_zone` (same limit/CORS pattern as `/v1/mlnodes/metrics` in #1469): e.g. `location = /metrics` (and `/api/metrics` if the dual-prefix convention applies) → `proxy_pass` to the ML/`api` backend path that serves the restored handler.
- Keep `api:9100/metrics` working for the existing Prometheus job — public zone is additive, not a replacement.
- Do **not** fold ping series into `/v1/mlnodes/metrics` federation output (unchanged from the proposal).

**Tests (regression guard that #1482 lacked):**

- Router-level: `mlnode.NewServer` serves `GET /metrics` with at least one `decentralized_api_*` series after a recorded operation (or after `initPrometheusMetrics`).
- Router-level: `GET /sd/devshardd` returns the versiond target-group shape for a fixture `ConfigManager`.
- Optional compose/doc check: `api:9100/metrics` still matches `deploy/join/observability/prometheus.yml`.

**Exit:** `curl api:9100/metrics` lists `decentralized_api_*`; Prometheus `decentralized-api` job is no longer a dead target; `/sd/devshardd` returns targets again; public path behind `metrics_zone` scrapes the same exposition.

### Step 1 — `common/probe` with no callers

**Files:** `common/probe/{probe.go,capability.go,scheduler.go,handler.go,parse.go}` + tests.

- Probe execution with `httptrace.ClientTrace.GotConn` → `ConnReused`.
- Timestamp parsing: ping headers → ping JSON → `Date` header → none. Unit is unix ns and the name says so; `Date` is RFC 1123 via `http.ParseTime`.
- Divergence: four-timestamp form when recv/send present, midpoint otherwise. Elapsed always from a single `time.Now()` via `time.Since`.
- Capability cache: demote **only** on 404/405; never on timeout, connection error, or 5xx. Re-discover on `Invalidate` or TTL.
- Scheduler: ticker, snapshot targets, bounded workers, per-target jitter, single-flight skip → `TickSkipped()`.
- `Handler` for Go targets.
- **CI wiring (required, see Test plan §7):** add `./probe/...` to `scripts/validate-edge-api.sh`. No CI job runs `go test ./...` inside `common/`; that script's explicit allowlist (`./queryapi/...`, `./observability/...`, `./chain/...`) is the only path by which `common` tests execute. A new package silently runs nowhere until it is listed.

**Exit:** `cd common && go test ./probe/... -race` green; no `client_golang` import; no caller.

### Step 2 — Gateway target set + metrics, fallback tier only

Proposal rollout phase 1. **No `devshardd` change**, because `GET /healthz` is already `"ok"` and middleware-bypassed, and `Date` gives ~1 s divergence for free.

**Files:** `devshard/cmd/devshardctl/hostping.go` (new), `metrics.go`, wiring in `gateway.go`, escrow lifecycle call sites.

- `hostPingTargets`: dial-target-keyed map `target → {refcount, lastUsed, participantKeys, routePrefix}` under its own mutex (not `g.mu` — the hot path is one write on first dispatch per escrow/host). `routePrefix` is the escrow/runtime’s `/devshard/<version>` (Decision D4); probe URLs are `{dial}{routePrefix}/healthz` then `{dial}{routePrefix}/ping`.
- Increment on **first successful inference dispatch** to an escrow's host; decrement in *every* teardown path: `deactivateDevshardByIDWithReason`, `deactivateAndSettleDevshardByID`, `retireRuntime`, and startup-skipped escrows. Dedupe participant keys → one probe per dial target.
- Slow reconcile (minutes) that **logs a discrepancy** rather than silently repairing, so refcount asymmetry is discoverable.
- `DevshardMetrics` gains the proposal series (`_up`, `_rtt_seconds` **gauge**, `_clock_divergence_seconds{source}`, `_last_probe_timestamp_seconds`, `_probe_kind{kind}`, `_targets`, `_ticks_total`/`_ticks_skipped_total`) plus:
  - **Decision D2:** fleet-wide `_rtt_seconds` **histogram** (no `host` / `participant_key` label) observed on every warm sample in phase 1.
  - **Decision D3:** mapping series e.g. `_participant_info{host,participant_key}=1` (or gauge) updated whenever the target set changes; deleted with the host/participant on teardown. Dashboards join via this series, not via an external table.
  - `DeleteHostPingMetrics(host)` (and participant mapping cleanup) on the gateway registry, modelled on `devshard/observability/metrics_lifecycle.go:DeleteEscrowMetrics`.
- Probe-only `http.Transport` (do not share the inference pool from `devshard/transport/client.go`; probes must not evict inference idle conns).
- Config: `DEVSHARD_GATEWAY_HOST_PING_INTERVAL` / `_TIMEOUT` / `_CONCURRENCY` / `_DISABLED`.

**Exit:** after one inference, host appears with `up=1`, warm RTT gauge + histogram sample, `probe_kind="date"`, advancing freshness, and a `participant_info` series for that host; after settle, every series (including mapping + histogram label-free samples still accumulate historically, but gauges/mapping are gone). Kill switch removes the goroutine.

### Step 3 — `devshardd` ping endpoint + gateway upgrade to sub-ms

Proposal rollout phase 2. **Child path `/ping`; wire path versioned (Decision D4).**

- Mount `probe.Handler` at `GET /ping` on the **child** in `devshard/cmd/devshardd/server.go`; add `/ping` to `isLifecycleBypassPath` (`lifecycle.go`) and to the OTel skip list (`devshard/observability/middleware.go`). Do **not** put `/ping` on versiond’s mux next to supervisor `/healthz`, and do **not** add it to `isVersionlessObsPath` (that would pin/fan-out and hide per-version reachability).
- Gateway: `PingURL = dial + routePrefix + "/ping"` (e.g. `…/devshard/v2/ping`). versiond strips the version segment and forwards `/ping` to the child. Drive `Prober.Invalidate` when the host’s active `RoutePrefix` / approved versions change so an upgraded slot is rediscovered without waiting on `CapabilityTTL`.
- **Proxy allowlist:** ensure join/nginx paths that already forward `/devshard/{version}/…` (or `/{version}/…`) also allow `…/ping`. A bare `location = /ping` is wrong — clients never hit that through versiond.

**Exit:** probe to `{host}/devshard/<ver>/ping` returns 204 + recv/send headers; bare `{host}/ping` does not falsely look like success; mixed fleet shows `probe_kind="ping"` on upgraded children and `"date"` on old ones.

### Step 4 — Dapi ping job (requires Step 0)

Proposal rollout phase 3a. **Depends on Step 0** — ping gauges register on the default registry and are scraped from the restored `/metrics` (ML port + public `metrics_zone`).

- Register the `dapi_mlnode_ping_*` instruments on the default registry with `prometheus.MustRegister`, matching the existing style (include the phase-1 fleet-wide RTT histogram, same as gateway Decision D2).
- `TargetSource` from `nodeBroker.GetNodes()` reusing the **exact** base URL construction of `mlNodeMetricsTargets` (`PoCUrl()` + `url.JoinPath`, **not** `PoCUrlWithVersion`), so ping and federation describe the same dial path.
- Background goroutine started from `main.go` next to `MLNodeBackgroundManager`; kill switch `DAPI_API__MLNODE_PING_DISABLED` mirroring `DAPI_API__MLNODE_METRICS_DISABLED`.
- Fallback URL: `{PoCUrl}/readyz` (root-mounted). **Never** `/health` or `/livez`.

**Exit:** `GET /metrics` (Step 0) lists every broker node’s ping series; nodes removed from inventory lose their series.

### Step 5 — mlnode ping endpoint

Proposal rollout phase 3b.

- `GET /api/v1/ping` under `API_PREFIX` returning recv/send unix ns with no GPU or manager work.
- Add the path to the nginx template if mlnode is ever fronted.
- Optional and **separately reviewed**: split `/livez` from `/health`. They share one handler and a 5 s cache today, so splitting them changes k8s probe behaviour and is not a drive-by change.

**Exit:** dapi reports `probe_kind="ping"`; mlnode ping p99 stays flat while `/health` load is unchanged.

### Step 6 — Dashboards, alerts, soak

- Panels per surface: up, warm RTT, divergence by `source`, target count, tick rate.
- Alerts: staleness (`time() - last_probe_timestamp > 3×interval`), `rate(ticks_total) == 0`, sustained `ticks_skipped`, `|divergence| > 2s` for `source="date"` and a tighter bound for `source="ping"`.
- 24 h soak on a real fleet: target-count flat (no refcount leak), no probe-attributable load on hosts, no quarantine transitions correlated with probe failures.

---

## Test plan

### 1. `common/probe` unit tests (Step 1)

`httptest.Server` for happy paths; a scripted `http.RoundTripper` for everything timing- or failure-related. `Config.Clock` is injectable so wall-clock scenarios are deterministic.

| Case | Assertion |
|---|---|
| Ping recv/send present | `Kind=KindPing`; `RTT` equals injected delay minus injected server processing; divergence matches the four-timestamp fixture |
| Known offset, symmetric delay | server ahead 5 s ⇒ `Divergence ≈ +5s` within a tight tolerance |
| Known offset, **asymmetric** delay | divergence error bounded by half the asymmetry — pins the documented limitation instead of pretending it is exact |
| `Date` only | `Kind=KindDate`, `DivergenceSource=KindDate`; tolerance accounts for whole-second truncation |
| No parseable timestamp | `Up=true`, `HasDivergence=false` (explicitly **not** `Divergence==0`) |
| **Wall clock steps backwards mid-probe** | `RTT > 0` and unchanged; divergence unaffected. Guards the monotonic/wall split — the one bug that silently poisons every derived series |
| ms value in an ns field | rejected, no divergence sample, error recorded — the ms/ns mix-up presents as a plausible multi-hour skew otherwise |
| 404 / 405 on ping | capability demoted to date/health |
| Timeout / connection refused / 500 on ping | `Up=false`, capability **not** demoted |
| N ticks after a demote | exactly **one** ping attempt (TTL respected); proves no per-tick flapping |
| `Invalidate` after demote | next tick attempts ping again |
| Cold then warm probe | first `ConnReused=false`, subsequent `true` |
| Slow tick vs interval | `TickSkipped()` fires; no overlapping wave; worker count never exceeds `Concurrency` (counting semaphore observer) |
| Target list mutated mid-tick | wave size unchanged (snapshot semantics) |
| `Interval < 2×Timeout` | `New` returns an error |
| `-race` across the suite | no data race on the capability cache |

`Handler`: `testing.B` with `-benchmem` asserting a fixed low allocation bound; `recv <= send`; `204` with both headers; values parse as ns.

### 2. Gateway unit tests (Step 2)

- **Refcount exhaustiveness:** table-driven over all four teardown paths — each must reach refcount 0 and call `Forget`. A path added later without a decrement fails here.
- **Dedupe:** several participant keys on one dial target ⇒ one target, one probe; mapping series lists every `participant_key`.
- **Cleanup completeness:** gather the gateway registry after `Forget` and assert **no** series for that host remains, including `last_probe_timestamp` and `participant_info`. Written as "no metric name with this label value" rather than a per-series list, so a newly added gauge that misses cleanup fails automatically.
- **Fleet histogram:** warm samples increment the label-free `_rtt_seconds` histogram; cold samples do not.
- **Kill switch:** disabled ⇒ no goroutine, no probes, no series.
- **Hard invariant:** inject a limiter/quarantine double and assert **zero** calls on it across a full tick where every probe fails. This is the guard for the correlated-failure hazard: a prober-local fault must never quarantine the fleet.
- **Startup-skipped escrows** never enter the set.

### 3. `devshardd` tests (Step 3)

- `/ping` returns 204 + headers; `/healthz` unchanged.
- Bypass: probing `/ping` does not move lifecycle inflight gauges and produces no trace span.
- Drain: `/ping` behaviour during drain is deliberate and asserted (it should keep answering — reachability is not readiness).

### 4. Dapi tests (Steps 0 + 4)

**Step 0 (regression — land with the restore):**

- **`/metrics` route exists** on the ML server and exposes `decentralized_api_*` — HTTP-level test against `mlnode.NewServer`'s router. This is the guard that would have caught [#1482](https://github.com/gonka-ai/gonka/pull/1482).
- **`/sd/devshardd`** returns the #1469-era target-group shape for a fixture config.
- Optional: public nginx `metrics_zone` location renders / proxies `/metrics` (compose config check).

**Step 4:**

- Same `/metrics` exposition includes `dapi_mlnode_ping_up` after the ping job runs.
- Target construction equals `mlNodeMetricsTargets`' base URL for the same broker fixture (asserted against the federation helper, not a hardcoded string, so the two cannot drift).
- Node removed from broker inventory ⇒ series forgotten.
- Kill switch honoured.

### 5. testenv citest (`citest-host-ping`)

New target in `devshard/testenv/Makefile`. It is picked up by CI automatically: `list-citest-targets` greps `^citest-*` and feeds the workflow matrix (`.github/workflows/devshard-testenv.yml`), so no workflow edit is needed.

Scenarios, using the existing stub host (`devshard/cmd/devshard-host`, already serving `GET /health`) as the programmable target:

| ID | Scenario | Assertion |
|---|---|---|
| P1 | Escrow opened but **unused** | host absent from `_targets` and from all series |
| P2 | One inference, then probes | `up=1`, warm RTT gauge + histogram sample, freshness advancing, `participant_info{host,participant_key}` present |
| P3 | Settle / retire | all gauges + mapping for that host gone; `_targets` back to baseline |
| P4 | Stub answers `/ping` with a **skewed** timestamp (offset injected in the stub, not the container clock) | `_clock_divergence_seconds{source="ping"}` ≈ injected offset |
| P5 | Stub 404s `/ping` (old image) | `probe_kind="date"`, divergence present at ~1 s resolution, exactly one ping retry per TTL |
| P6 | Stub stops answering | `up=0`, capability retained, no quarantine transition on the gateway |
| P7 | Kill switch flipped on a running gateway | probes stop, freshness goes stale, inference unaffected |

P4 injects skew in the stub response rather than the container clock on purpose: CI containers share the host clock, so a test that needs a genuinely wrong clock is either unreliable or requires `faketime`. Skewing the payload tests exactly the code under test.

### 6. Soak (Step 6)

24 h, real fleet: `_targets` flat; `ticks_skipped_total` zero; no host-side latency change attributable to probes; zero quarantine transitions correlated with probe dips; alert rules fire on a deliberately stopped prober.

### 7. Where these run in CI

| Tests | Job | Action needed |
|---|---|---|
| `common/probe` | *(none today)* | **Add `./probe/...` to `scripts/validate-edge-api.sh`** — the only place `common` tests run, via an explicit allowlist |
| Gateway, `devshardd` | Build and Test Devshard (`go test ./...` in `./devshard`) | none |
| Dapi | Build and Test API Wrapper (`go test ./...` in `./decentralized-api`) | none |
| `citest-host-ping` | devshard testenv matrix | none (auto-discovered) |
| mlnode ping | mlnode Python suite | add handler test |

---

## Risks

| Risk | Mitigation |
|---|---|
| Refcount leak grows the target set silently | reconcile logs discrepancies; alert on `_targets` drift; exhaustiveness test over teardown paths |
| Probe transport competes with inference | separate transport; `MaxIdleConnsPerHost` small; interval below idle timeout |
| Someone wires probe results into routing later | hard invariant documented in the proposal **and** asserted by a test double |
| Version omitted from probe URL ⇒ hits wrong process or 404 | Decision D4: always `{RoutePrefix}/ping`; never bare `/ping` or versiond `/healthz` |
| `/ping` missing from host proxy allowlist ⇒ permanent coarse tier | Step 3 allowlist; P5 covers the 404 path so fallback stays correct |
| Divergence read as a measurement | alert thresholds differ per `source`; `date` bias ≤ 1 s from truncation is documented |
| `common/probe` grows caller-specific policy | registry-free API; `Sink`/`TargetSource` are the only extension points |

## Open questions

None currently. (Exposure, histogram, `participant_key`, and versioned ping path are decided above.)
