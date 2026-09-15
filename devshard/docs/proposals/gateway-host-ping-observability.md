# Proposal: Host / MLNode ping observability (RTT + clock divergence)

**Status:** In progress — Steps 0–4 implemented in tree; Steps 5–6 deferred (see below)  
**Related:** Gateway host health; dapi mlnode metrics federation ([PR #1469](https://github.com/gonka-ai/gonka/pull/1469))  
**Shared library:** `common/probe` (registry-free; callers own Prometheus)  
**Scope:**

1. **Gateway → `devshardd`** — periodic pings to distinct hosts backing opened, *used* escrows.
2. **Dapi → mlnode** — same probe semantics against the participant’s ML nodes (broker inventory), complementary to `/v1/mlnodes/metrics` federation.

Prometheus: per-target RTT, up, probe kind, probe freshness, and estimated clock divergence — coarse (~1 s) from the standard HTTP `Date` header today, sub-millisecond once a ping endpoint ships.

Claims about current behaviour were checked against the code and are cited inline. **Implemented in tree:** `common/probe`, gateway host-ping job (Surface A), `devshardd` `/clock`, dapi ML-port `/metrics` restore, and dapi mlnode ping job (Surface B against `/readyz` until Step 5). **Deferred:** mlnode `/api/v1/clock` (Step 5) and dashboards/alerts/soak (Step 6) — see [Deferred work](#deferred-work-steps-56).

---

## Problem

Operators lack a continuous, low-cost signal for:

1. **Reachability / RTT** of targets that matter in production paths.
2. **Clock divergence** between the probing process and those targets (timeouts, settle windows, cross-host timestamps).

Two probe surfaces:

| Prober | Targets | Why this set |
|---|---|---|
| Gateway | Distinct `devshardd` hosts for **opened escrows this gateway has used** (≥1 inference) | Idle registered hosts should not be probed |
| Dapi | Every ML node in the participant’s **broker inventory** (same source as [PR #1469](https://github.com/gonka-ai/gonka/pull/1469) federation) | Operator cares about the whole local fleet, not only “recently used” |

Inference / quarantine / PerfTracker remain the control plane; these pings are **observability only** (enforced as a hard invariant below).

Existing timing signals do not cover this: `PerfTracker` / `RequestSample` record receipt and first-token times, but only for hosts that received real inference, only under load, and with application work folded in. There is no idle-path reachability baseline and no clock comparison anywhere in the codebase — the only clock logic is the 30 s signed-request drift limit in `devshard/transport/auth.go`, which rejects requests rather than measuring drift.

---

## Shared probe contract

### Endpoints (version-aware)

| Tier | Endpoint | Server time source | Divergence resolution |
|---|---|---|---|
| Preferred | New lightweight **clock** (`GET /clock` on `devshardd`, `GET /api/v1/clock` on mlnode) | Explicit `t_recv` / `t_send` unix ns | sub-millisecond |
| Fallback A | Existing cheap **liveness** + standard HTTP **`Date`** response header | `Date` (RFC 7231, whole seconds) | ~1 s — enough for gross skew |
| Fallback B | Cheap liveness, no usable `Date` | none | RTT / up only |

**`Date` is free.** Both Go `net/http` and uvicorn emit `Date` on every response, generated at write time — the same position as `t_send`. Gross clock skew (the ops-relevant failure: minutes/hours) is therefore observable on *today's* images with **zero server-side work**. The new ping endpoint only buys resolution, so it must not gate shipping the job.

Because the two tiers differ in precision by ~3 orders of magnitude, keep them distinguishable: label divergence series with `source="ping|date"` (2 values, negligible cardinality) so alert thresholds are not applied to whole-second data.

Capability cache per target: on first ping success → sticky `ping`; on definitive miss (404 / 405) → sticky `date`/`health` and **never** alternate per tick. Invalidate on:

- **Observed version change** — preferred over blind TTL. The gateway already polls peer versions (`VersionsCache`, `devshard/cmd/devshardctl/versions_cache.go`), so an upgraded host regains divergence immediately instead of after a TTL.
- **TTL** (5–15 min) as the fallback when no version signal exists (dapi → mlnode).

⚠️ **Proxy allowlist.** A 404 can come from an intermediary rather than the target: gonka fronts services with nginx (`proxy/nginx.unified.conf.template`) and only routes explicitly configured paths. If a new ping path is not added there, capability detection sticks to fallback **permanently and silently**. Adding the route to the proxy config is part of shipping the endpoint, not a follow-up.

### Time-divergence estimate

Baseline (single server timestamp, or `Date`):

- `t_probe` — prober wall clock when the request is **sent**
- `rtt` — elapsed until response headers (or tiny body) fully received
- `t_srv` — timestamp carried by the target
- `t_est = t_probe + rtt / 2` — estimated target time at response generation
- `divergence = t_srv - t_est`

Positive ⇒ target clock ahead of the midpoint estimate; negative ⇒ behind.

**Preferred: two server timestamps.** A single timestamp folds *server handler time* into the estimate, because `t_srv` is taken just before write (end of processing) while `t_est` assumes the midpoint. Carrying both receive and send times removes that bias for two extra `time.Now()` calls, using the standard NTP four-timestamp form:

```
offset = ((t_recv - t_probe) + (t_send - t_done)) / 2
delay  = (t_done - t_probe) - (t_send - t_recv)
```

where `t_done` is the prober's wall clock at response receipt (NTP's T1–T4 with `t_probe`, `t_recv`, `t_send`, `t_done`). `offset` carries the same sign convention as the baseline `divergence` — positive means the target is ahead — so both tiers feed **one** metric, distinguished only by the `source` label. `delay` is a better RTT than wall-to-wall elapsed time because it subtracts server processing. Residual error is one-way path **asymmetry**, which no single-round-trip method can remove.

**Clock hygiene (implementation-critical).** `rtt` / `delay` must come from the **monotonic** clock and `t_probe` / `t_done` from the **wall** clock. In Go, capture `start := time.Now()` once and derive elapsed via `time.Since(start)` (monotonic) while reading `start` for wall time — computing elapsed by subtracting two independent `time.Now()` wall reads lets an NTP step corrupt the RTT sample, which then corrupts divergence through `rtt/2`.

A response that is reachable but carries no parseable timestamp is `up=1` with **no** divergence sample (do not emit 0).

### Ping endpoint shape (both `devshardd` and mlnode)

Keep the handler **O(1) and allocation-light**:

- No GPU / manager / escrow work.
- **Normative wire format:** `204` + headers `X-Server-Recv-Ns` / `X-Server-Send-Ns` (see [Appendix: MLNode handoff](#appendix-mlnode-handoff--how-dapi-connects-what-to-add); same contract for `devshardd`). Prober may also accept JSON `{"recv_unix_ns":…,"send_unix_ns":…}` as a parse fallback — do not ship JSON *instead of* the headers.
- **Unit is unix nanoseconds, always** — the field/header name carries the unit so a prober can never mis-scale by 10⁶. (Do not leave "ns or ms" to the implementer; a silent ms/ns mix-up shows up as a plausible-looking multi-hour divergence.)
- Timestamps taken at handler entry and immediately before write; no auth, no tracing middleware — same bypass list that already excludes `/healthz` and `/metrics` (`devshard/cmd/devshardd/lifecycle.go:isLifecycleBypassPath`, `devshard/observability/middleware.go`).

### Existing endpoints: what is actually cheap today

**`devshardd`** — `GET /healthz` returns the literal string `"ok"` and is already excluded from lifecycle middleware (`devshard/cmd/devshardd/server.go`). It is a genuinely O(1) probe, so **Surface A needs no server change** to get useful RTT (and ~1 s divergence via `Date`).

**mlnode** — the fallback story is worse than "use the light path":

| Path | Reality |
|---|---|
| `/health`, `/livez` | **Same handler** (stacked decorators, `mlnode/packages/api/src/api/health.py`): NVML device enumeration + all three manager health checks. Wrapped in a **5 s response cache**. |
| `/readyz` | No GPU work, but calls `inference_manager.is_healthy()` (vLLM runner liveness) **when `service_state == INFERENCE`** — i.e. exactly on the nodes that matter. |

Two consequences the naive design gets wrong:

1. **The 5 s cache does not save you.** At a 10–30 s probe interval every probe lands after TTL expiry, so *each* probe pays full NVML enumeration. A cache only helps when probes are faster than the TTL, which is the opposite of the intended interval. Probing `/health` on a schedule is therefore strictly worse than probing nothing.
2. **`/readyz` measures runner health, not path latency** on inference nodes, so its RTT is not comparable across service states.

⇒ For Surface B, either ship the ping endpoint or accept that fallback RTT is a *composite* signal (documented as such, and not compared against Surface A numbers). Health routes are mounted at the **root** (`/livez`, `/readyz`), while API routers use `API_PREFIX = "/api/v1"` — so ping lives at `/api/v1/clock` and the fallback does **not**.

---

## Performance-oriented design

Naive “every X seconds, for each host, try new endpoint then health” is wrong for cost and for metric quality. Optimizations below are part of the contract.

### 1. Target set maintenance (gateway)

- **Dial-target-keyed map** with escrow refcount (or generation): `target → {refcount, last_used, capability}`. Keyed by dial target, not participant key — see Surface A for why that distinction is load-bearing.
- **In-flight add** on first inference dispatch to that escrow’s host (hot path: one map write under mutex / shard).
- **Lazy drop**: decrement on escrow close / retire; remove target when refcount hits 0, and delete its metric series in the same critical section. Prefer this over full periodic reconcile.
- Refcount asymmetry leaks silently, so make the decrement paths exhaustive (deactivate, settle, retire, and startup-skipped escrows) and let the slow reconcile (every few minutes — **not** every tick) exist purely as a backstop that logs when it finds a discrepancy. A leak here is invisible except as a slowly growing `…_ping_targets`, which is itself worth alerting on.

Dapi: build target list from the same broker enumeration as `MLNodeMetricsHandler` (`GetNodes()` → `PoCUrl()` + `Node.Id`). No refcount needed; the list changes when nodes are added/removed, so delete series for nodes that leave the inventory.

### 2. Scheduler

- Interval **X** (config; default on the order of 10–30s).
- **Single-flight ticks**: if the previous fan-out is still running, **skip** (do not coalesce or queue) and increment `…_ping_ticks_skipped_total`. "Skip or coalesce" is not a design — coalescing needs a queue and can still pile up, and a silent skip hides degradation, so the choice is skip **plus** a counter to alert on.
- **Bounded concurrency** (e.g. 8–32 workers); per-target timeout ≪ interval (e.g. 1–2s). With timeout ≤ 2s and ≥8 workers, a tick can only exceed a 15s interval above ~60 targets, which is where the skip counter starts earning its keep.
- Optional **small jitter** per target inside the tick to avoid synchronized load on shared infra.
- Snapshot the target list once at tick start; mutations during the tick do not resize the in-flight wave.

### 3. HTTP client

- One shared `http.Client` with **keep-alive**; never construct a client per probe. `devshard/transport/client.go` already pools per-base-URL transports (`MaxIdleConnsPerHost: 4`, `IdleConnTimeout: 120s`) — reuse that shape, but a **separate** transport for probes so probe connections cannot evict inference connections from the idle pool.
- **Constrain interval < `IdleConnTimeout`.** This is a correctness requirement, not tuning: if the interval exceeds the idle timeout, every probe re-dials (plus TLS) and the metric reports handshake cost, not RTT. A 10–30s interval against a 120s idle timeout keeps connections warm.
- Even so, the **first** probe per target (and any after a pool eviction) includes dial. Do not let that outlier land in the same series as warm RTT: use `httptrace.ClientTrace` (`GotConn.Reused`) and either drop the sample or record it under a separate `…_ping_cold_rtt_seconds`. Otherwise every restart shows a fleet-wide latency spike that is pure connection setup.
- Tiny response read budget (e.g. 4 KiB, `io.LimitReader`); discard body after timestamp parse.

### 4. Capability sticky + cheap fallback

- Cache `probe_kind ∈ {clock, date, health}` per target; re-discover only on observed version change, TTL expiry, or sustained ping failures that look like “endpoint gone”.
- Distinguish **“endpoint absent”** (404/405 ⇒ demote capability) from **“target down”** (timeout, connection refused ⇒ `up=0`, keep capability). Demoting on a timeout would let one outage permanently downgrade a healthy host to coarse divergence until the next TTL.
- Fallback must not call heavy mlnode `/health`; otherwise RTT measures handler cost (NVML + managers) rather than path latency.

### 5. Metrics cardinality / cost

Prefer **last-value gauges** for the “ping map” (aligned with `mlnode_up{node_id}` in PR #1469), not high-cardinality histograms per host:

| Metric | Type | Notes |
|---|---|---|
| `…_ping_up{…}` | gauge 0/1 | Last probe success |
| `…_ping_rtt_seconds{…}` | gauge | Last warm RTT (both probe kinds) |
| `…_clock_divergence_seconds{…,source="ping\|date"}` | gauge | Last divergence; **omit** series entirely when no timestamp is available |
| `…_ping_probe_kind{…,kind="clock\|date\|health"}` | gauge 1 info-style | Sticky capability |
| `…_ping_targets` | gauge | Size of in-memory set |

Optional low-cardinality histogram of RTT **without** host label (fleet distribution only). Avoid `histogram × host` unless scrape budget is proven.

#### Self-observability is mandatory here

Last-value gauges have a failure mode that must be closed before this ships: **a dead prober is indistinguishable from a healthy fleet.** If the scheduler goroutine panics, deadlocks, or its ticker is starved, every `…_ping_up` stays pinned at its last `1` and every RTT gauge freezes at a good value. The dashboard goes green precisely when the signal is gone. Absence-based alerting does not help either, since the series persist for the process lifetime.

Required companions:

| Metric | Type | Purpose |
|---|---|---|
| `…_ping_last_probe_timestamp_seconds{…}` | gauge (unix ts) | Freshness per target; alert on `time() - metric > k·interval` |
| `…_ping_ticks_total` | counter | Liveness of the scheduler itself; alert on `rate() == 0` |
| `…_ping_ticks_skipped_total` | counter | Single-flight skips (see §2); quantifies overload |

The two counters also make the job's own cost auditable without adding per-probe cardinality.

#### Registry and cleanup

- Gateway metrics live on a **custom registry**, not the default one (`DevshardMetrics`, `prometheus.NewRegistry()` in `devshard/cmd/devshardctl/metrics.go`) — register ping instruments there, not via `promauto`.
- On target removal: **DeleteLabelValues** so stale hosts disappear — same “absence” spirit as 404 nodes omitted from mlnode federation. Note the gateway has **no such cleanup helper today**; `devshard/observability/metrics_lifecycle.go:DeleteEscrowMetrics` is the precedent to copy. Every per-host gauge above (including the freshness gauge) must be deleted together, or a retired host leaves a permanently stale `up=1`.

### 6. Interaction with PR #1469 federation

- Ping metrics live on the **prober’s own** `/metrics` — the gateway already serves one (`gateway.go`); dapi’s was removed in [PR #1482](https://github.com/gonka-ai/gonka/pull/1482) and must be restored (see Surface B).
- Do **not** fold ping RTT into the public `/v1/mlnodes/metrics` merge by default (that path is pull-through, cached ~10s with single-flight, nginx rate-limited, and already fans out to exporters). Operators scrape dapi/gateway metrics directly.
- Reuse broker target listing and kill-switch lessons from PR #1469; the ping job is a **background poller**, not request-triggered, which decouples probe load from scrape QPS. That also means the federation handler's cache and nginx rate limit protect *it* but not the ping job — the ping job's own rate limiting is the fixed interval, so the interval is the only thing standing between a config typo and a probe flood.

### 7. Cost budget (rule of thumb)

Steady-state probe rate is `targets / interval`. **Concurrency does not multiply it** — the worker cap bounds instantaneous parallelism (and therefore peak sockets), not throughput:

```
rate         = targets / interval          # sustained load on the fleet
peak_sockets = min(targets, concurrency)   # burst footprint per tick
```

Example: 50 hosts / 15s ≈ **3.3 probes/s**, each &lt;1 KB with a &lt;2s timeout, ≤32 concurrent — negligible against inference QPS on a dedicated client. Per-target load is what an operator actually feels: `1/interval` ≈ 0.07 req/s per host.

---

## Surface A — Gateway → `devshardd`

### Target set

Opened escrows this gateway instance has used (≥1 inference). Distinct dial targets only.

Nothing like this exists today, and the *keying* is the design decision to get right first. Every existing per-host structure in the gateway is keyed by **participant key** (`host:N`), not by dial target:

| Structure | Key | File |
|---|---|---|
| `CapacityState` (weights, `escrowMembership`) | participant key | `capacity_state.go` |
| `PerfTracker.hosts` (receipt / first-token rings) | participant key | `hostperf.go` |
| `ParticipantRequestLimiter.participants` (quarantine) | participant key | gateway limiter |

Several participant keys can resolve to one dial target, so **probing per participant key would multiply identical probes** against the same socket. Probe per **distinct dial target**, and pick the label deliberately:

- `host` = dial base URL (deduplicated) — correct for RTT/divergence, but **does not join** with the existing `devshard_gateway_participant_*` series.
- If dashboards need that join, emit one low-cardinality mapping series `devshard_gateway_host_participants{host,participant_key} 1` (cardinality ≈ existing participant metrics) rather than adding `participant_key` to every ping series.

Also decide explicitly what "used" means and where the refcount is incremented, since a wrong choice leaks targets: increment on **first successful inference dispatch** to that escrow's host, decrement on escrow deactivate/settle/retire (`deactivateAndSettleDevshardByID`, `retireRuntime`), and treat the slow reconcile purely as a leak backstop.

### Metrics (sketch)

- `devshard_gateway_host_ping_up{host=...}`
- `devshard_gateway_host_ping_rtt_seconds{host=...}`
- `devshard_gateway_host_clock_divergence_seconds{host=...,source=...}`
- `devshard_gateway_host_ping_last_probe_timestamp_seconds{host=...}`
- `devshard_gateway_host_ping_probe_kind{host=...,kind=...}`
- `devshard_gateway_host_ping_targets`, `devshard_gateway_host_ping_ticks_total`, `…_ticks_skipped_total`

Config: `DEVSHARD_GATEWAY_HOST_PING_INTERVAL`, concurrency, timeout, capability re-probe TTL, plus a **kill switch** (`DEVSHARD_GATEWAY_HOST_PING_DISABLED`) — symmetric with dapi's `DAPI_API__MLNODE_METRICS_DISABLED`, so a misbehaving probe job can be stopped without a rebuild. Surface A needs this as much as Surface B does.

### `devshardd` work

Add a lightweight ping handler at **child** `GET /clock` (root-mounted, like child `/healthz`), registered on the existing middleware-bypass list. Keep `/healthz` for fallback and k8s probes. **Not required for phase 1** (see rollout).

**versiond path (load-bearing):** `devshardd` is never dialed bare — `versiond` owns the listen port and requires a version prefix (`versioned/internal/proxy/proxy.go`). The gateway already has each escrow’s `RoutePrefix` (`/devshard/<version>`); probe URLs are therefore `{host}{RoutePrefix}/clock` (and `{host}{RoutePrefix}/healthz` for fallback). Bare `{host}/clock` does not reach a child; bare `{host}/healthz` is versiond’s *supervisor* health, not the serving binary. Details and Decision D4: [implementation plan](../host-ping-observability-plan.md).

---

## Surface B — Dapi → mlnode

### Target set

Same inventory as [PR #1469](https://github.com/gonka-ai/gonka/pull/1469): participant ML nodes via broker `GetNodes()` → `PoCUrl()` + `node_id` (`Node.Id`).

Match federation exactly: it uses the **un-versioned** `PoCUrl()`, not `PoCUrlWithVersion()` (`internal/server/public/server.go:mlNodeMetricsTargets`). Probing a versioned URL while federation probes the plain one would silently compare two different dial paths, so either reuse `PoCUrl()` or state the divergence and why.

### Probe URLs

Health routes are root-mounted; only API routes carry `API_PREFIX = "/api/v1"`:

| Kind | Path |
|---|---|
| Ping | `{PoCUrl}/api/v1/clock` (new; carries timestamps) |
| Fallback | `{PoCUrl}/readyz` — **root**, not `/api/v1/readyz`; never `/health` or `/livez` (identical heavy handler, see above) |

Build with `url.JoinPath` as federation does — `PoCUrl()` already includes `PoCSegment`, so naive string concatenation breaks segmented hosts.

### Metrics (sketch)

Mirror gateway naming under dapi / observability package:

- `dapi_mlnode_ping_up{node_id=...}`
- `dapi_mlnode_ping_rtt_seconds{node_id=...}`
- `dapi_mlnode_clock_divergence_seconds{node_id=...,source=...}`
- `dapi_mlnode_ping_last_probe_timestamp_seconds{node_id=...}`
- `dapi_mlnode_ping_probe_kind{node_id=...,kind=...}`
- `dapi_mlnode_ping_targets`, `dapi_mlnode_ping_ticks_total`, `…_ticks_skipped_total`

⚠️ **Blocking prerequisite: dapi does not serve `/metrics` today — and this is a regression, not an unbuilt feature.** [PR #1469](https://github.com/gonka-ai/gonka/pull/1469) added public federation (`GET /v1/mlnodes/metrics`) and that route is still bound. What commit `b53fd8fcd` ([PR #1482](https://github.com/gonka-ai/gonka/pull/1482), "Devshard 0.2.14 v4") accidentally removed is dapi's **own** exposition that coexisted with #1469 on the ML server (port 9100):

```
- e.GET("/metrics", echo.WrapHandler(observability.MergedMetricsHandler(devshardobservability.Registry())))
- e.GET("/sd/devshardd", s.getDevshardSDTargets)
- s.e.Server.ConnState = devshardobservability.ConnState("ml")
```

Restore that contract **exactly as at the #1469 merge** (`ee21535`), adapted only where dapi no longer has an in-process `devshardobservability.Registry()` (`MetricsHandler` / empty `MergedMetricsHandler`). Do **not** add new public nginx locations — [#1469](https://github.com/gonka-ai/gonka/pull/1469)’s `/v1/mlnodes/metrics` and `/api/v1/mlnodes/metrics` remain the only public metrics paths; dapi’s own registry is scraped at `api:9100/metrics`. Full restore checklist, tests, and sequencing are in the [implementation plan Step 0](../host-ping-observability-plan.md). Consequences still live today: `decentralized_api_*` gauges are gathered by nobody, `api:9100/metrics` and `api:9100/sd/devshardd` are dead scrape/SD targets, and the observability docs still claim they work. Note also that dapi's existing metrics use the **default** registry with `prometheus.MustRegister` (not `promauto`), while `mlnode_up` is synthesized into federation output and is *not* a registered gauge — so "aligned with `mlnode_up`" is a naming analogy, not a shared instrument.

Three-state nuance vs federation `mlnode_up`:

- Federation: 404 exporter ⇒ **absent**; scrape fail ⇒ `up=0`.
- Ping map: every inventoried node should appear; `up=0` on timeout/error; sticky fallback when ping 404s (old image). Divergence series absent (or `source="date"`) until ping works.

### Mlnode work

Add `/api/v1/clock` returning receive/send unix-ns timestamps with negligible work. Optionally add a truly trivial liveness route so fallback RTT is meaningful on old images — note that "slim `/livez`" is **not** a small change, because `/livez` currently shares its handler (and 5s cache) with `/health`; splitting them changes existing k8s probe behaviour and needs its own review.

### Scheduler placement

Background goroutine inside dapi (near observability), using shared HTTP client + single-flight ticks. Independent of `/v1/mlnodes/metrics` pull-through cache so scrape storms do not multiply pings.

Kill switch (same spirit as `DAPI_API__MLNODE_METRICS_DISABLED`): disable the ping job via env without a rebuild. Treat it as required, not optional — see the hard invariant below.

---

## Rollout phases

The original framing coupled everything to a new endpoint on both `devshardd` and mlnode, which puts the whole feature behind image rollout across a heterogeneous fleet. It decomposes cleanly instead:

| Phase | Prober change | Server change | What you get | Status |
|---|---|---|---|---|
| **1** | Gateway ping job against `/healthz` + `Date` | **none** | RTT + up + ~1s divergence on today's hosts | **done** (gateway + `common/probe`) |
| **2** | — | `devshardd` `/clock` (versioned wire path) | sub-ms divergence, de-biased RTT; capability flips per host | **done** |
| **3a** | dapi ping job (needs ML-port `/metrics`) | fallback `{PoCUrl}/readyz` | fleet map on `api:9100/metrics` even before mlnode `/clock` | **done** |
| **3b** | — | mlnode `/api/v1/clock` | `probe_kind="clock"` on Surface B | **deferred** (Step 5) |
| **ops** | dashboards / alerts / soak | — | operator-facing SLOs | **deferred** (Step 6) |

Phase 1 is the cheapest way to learn whether these gauges are actually load-bearing for operators before spending server-side changes and a fleet upgrade on precision. It also exercises the target-set refcount, scheduler, and cleanup paths — the parts most likely to have bugs — while the only consumer is a dashboard.

Phase 3a lands the dapi job against `/readyz` + `Date` (and sticky demotion when `/api/v1/clock` 404s). Phase 3b (mlnode ping) is optional precision for Surface B and is tracked below as deferred work.

---

## Deferred work (Steps 5–6)

This section is the shareable follow-up plan. The longer working checklist lived in `host-ping-observability-plan.md` (local); **commit and track remaining work here**.

### Step 5 — mlnode ping endpoint (proposal phase 3b)

- Mount `GET /api/v1/clock` under existing `API_PREFIX = "/api/v1"` returning `204` + `X-Server-Recv-Ns` / `X-Server-Send-Ns` (unix ns). No GPU, NVML, manager, DB, or cache work.
- Bypass auth / tracing / response-cache for this path (do **not** share the `/health` 5s cache).
- If an nginx / join-proxy fronts mlnode, allowlist `/api/v1/clock` in the same change.
- Do **not** change `/health` / `/livez` / `/readyz` in the same PR. Optional split of slim `/livez` from `/health` is a **separately reviewed** change (they share one handler + 5s cache today).
- Unit test: status, both headers, ns scale, `send >= recv`, no manager/GPU mocks required.
- **Exit:** dapi reports `probe_kind="clock"` for upgraded mlnodes; ping p99 stays flat while `/health` load is unchanged. Old images keep sticky `date`/`readyz` fallback.

Contract details and checklist: [Ping endpoint shape](#ping-endpoint-shape-both-devshardd-and-mlnode) and the [mlnode PR checklist](#checklist-for-the-mlnode-pr) in the appendix.

### Step 6 — Dashboards, alerts, soak

- Panels per surface (`devshard_gateway_host_ping_*`, `dapi_mlnode_ping_*`): up, warm RTT (gauge + fleet histogram), divergence by `source`, target count, tick rate / skips.
- Alerts: staleness (`time() - last_probe_timestamp > 3×interval`), `rate(ticks_total) == 0`, sustained `ticks_skipped`, `|divergence| > 2s` for `source="date"` and a tighter bound for `source="clock"`.
- 24 h soak on a real fleet: `_targets` flat (no refcount leak), no probe-attributable host load, zero quarantine transitions correlated with probe failures; alert rules fire on a deliberately stopped prober.

### Related follow-ups (not blocking Step 5/6)

- `citest-host-ping` full-stack gate for Surface A (gateway → versiond → `devshardd`); Surface B stays on dapi unit/router tests + mlnode Python handler test.
- Fault-injection citest cells (skewed ping headers, sticky 404 demote, kill switch) where not already covered by unit tests.

---

## Hard invariant: probes never feed the control plane

Stronger than "observability only": ping results **must not** reach `ParticipantRequestLimiter`, quarantine, `CapacityState`, or routing — not even as a tie-breaker. The failure mode is not hypothetical. Probe results are correlated across all targets by construction (one prober, one client, one transport, one tick), so a fault local to the *prober* — DNS blip, socket exhaustion, a paused process, a proxy rule change that 404s the ping path — presents as "the entire fleet is down" and would quarantine everything at once. The inference path fails independently per request and is therefore the safe control signal.

Corollary: the ping job must be able to fail loudly without degrading service. That is exactly what the freshness gauge and tick counters in §5 are for, and why the kill switch exists on both surfaces.

## Out of scope

- Replacing quarantine / PerfTracker / inference failover with these pings.
- Gateway probing every chain-registered host (only used-escrow hosts).
- NTP / hard clock sync, and any attempt to *correct* clocks from these measurements.
- Embedding ping series into public `/v1/mlnodes/metrics` federation output.
- Cross-gateway aggregation of used-host sets.
- Sub-millisecond divergence claims: one round trip cannot separate offset from path asymmetry, so treat these gauges as drift detectors, not measurements.

---

## Acceptance sketch

**Gateway**

- After ≥1 inference on an escrow, its host enters the set and is pinged every X seconds; closed/unused hosts drop out and **all** per-host series (including freshness) are deleted.
- Distinct dial targets are probed once even when several participant keys resolve to the same host.
- Sticky capability: old `devshardd` → RTT + `Date`-derived divergence; new → RTT + sub-ms divergence. Capability never flaps per tick.
- No overlapping tick storms; shared keep-alive client separate from the inference transport; cold-dial samples excluded from the warm RTT gauge.

**Dapi / mlnode**

- Every broker-listed mlnode appears in the ping map; ping endpoint preferred; old images fall back to `/readyz` (never `/health`) with RTT only.
- Ping metrics on a dapi `/metrics` route that **exists** (mounting it is part of the work), not mixed into `/v1/mlnodes/metrics` merge.
- Mlnode ping does not touch GPU/manager health assembly.

**Both**

- Killing the ping job (env switch) leaves inference behaviour byte-identical; no probe result reaches quarantine or routing.
- A stalled prober is detectable: freshness gauge goes stale and `…_ping_ticks_total` stops advancing, rather than gauges silently reporting a healthy fleet.
- Divergence is reported with `source`, and no divergence series is emitted when no timestamp was parsed.

---

## Appendix: MLNode handoff — how dapi connects, what to add

This section is the **normative contract** for the mlnode team. Dapi (Surface B) probes each broker-listed node; sub-millisecond clock divergence requires the ping endpoint below. Until it ships, dapi falls back to `{PoCUrl}/readyz` (RTT / up only — do not treat that RTT as comparable to a true ping).

### How dapi connects

| Item | Value |
|---|---|
| Target list | Same as federation ([PR #1469](https://github.com/gonka-ai/gonka/pull/1469)): `GetNodes()` → un-versioned `PoCUrl()` + `node_id` |
| Preferred probe | `GET {PoCUrl}/api/v1/clock` |
| Fallback probe | `GET {PoCUrl}/readyz` (root-mounted; **never** `/health` or `/livez`) |
| Interval | Config; default ~10–30s; per-target timeout ≪ interval |
| Auth | None — ping must answer without API keys / signed headers |
| Metrics consumer | Dapi’s own `/metrics` (`dapi_mlnode_*`); **not** `/v1/mlnodes/metrics` federation |

Capability sticky: first successful ping → stay on `ping`; definitive `404` / `405` → demote to fallback and stop alternating per tick. Timeout / connection refused → `up=0`, capability **unchanged**.

### Required endpoint (mlnode)

Freeze one wire format so it matches the Go `devshardd` / `common/probe.Handler` contract.

```
GET /api/v1/clock
```

| Requirement | Spec |
|---|---|
| Status | `204 No Content` |
| Body | empty |
| Headers (required) | `X-Server-Recv-Ns: <unix_ns>` — wall clock at **handler entry** |
| | `X-Server-Send-Ns: <unix_ns>` — wall clock **immediately before** the response is written |
| Value format | Decimal integer string, **unix nanoseconds** (e.g. `1735689600123456789`). Never ms/µs. Never float. |
| Ordering | `send_ns >= recv_ns` on the same clock |
| Side effects | None — no GPU, NVML, manager health, inference, DB, or cache |
| Middleware | Exclude from auth, tracing, and any response-cache decorator (do **not** share the `/health` 5s cache) |
| Other methods | `POST`/`PUT`/… → `405` (capability demotion treats 405 like “endpoint absent”) |

**Optional alternate** (prober also parses, but do not ship this instead of headers): `200` + `Content-Type: application/json` body `{"recv_unix_ns":<int>,"send_unix_ns":<int>}` — same units/semantics, no optional fields.

### Example (acceptance)

```http
GET /api/v1/clock HTTP/1.1
Host: mlnode:8080

HTTP/1.1 204 No Content
Date: Tue, 11 Aug 2026 16:00:00 GMT
X-Server-Recv-Ns: 1786454400123456789
X-Server-Send-Ns: 1786454400123456901
```

```bash
curl -si "http://${MLNODE}/api/v1/clock"
# expect: HTTP/1.1 204
# expect: X-Server-Recv-Ns and X-Server-Send-Ns present, digit-only, send >= recv
# expect: empty body; handler p99 stays flat under a 10–30s probe loop
# expect: /health handler load unchanged while ping is probed
```

### Checklist for the mlnode PR

1. Mount `GET /api/v1/clock` under the existing `API_PREFIX = "/api/v1"` router (not at root next to `/readyz`).
2. Capture recv ns at entry; capture send ns immediately before returning; set both headers; return `204`.
3. Bypass auth / tracing / response cache for this path.
4. Unit test: status, both headers, ns scale (value ≈ `time.time_ns()`), `send >= recv`, no manager/GPU mocks required to succeed.
5. If an nginx / join-proxy fronts mlnode, **allowlist** `/api/v1/clock` in the same change — a proxy `404` makes dapi stick to fallback permanently (see Shared probe contract).
6. Do **not** change `/health` / `/livez` / `/readyz` behaviour in the same PR (splitting slim `/livez` is a separate review).

### Out of scope for mlnode

- Emitting Prometheus series itself for this feature (dapi owns `dapi_mlnode_ping_*`).
- NTP sync or correcting clocks from these timestamps.
- Feeding ping results into scheduling / quarantine (dapi hard invariant; mlnode should not invent a control-plane use either).
