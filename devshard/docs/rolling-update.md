# Rolling Update Plan: versiond + devshardd

Goal (operator requirement):

> Roll out a **new binary under the same version name** (name stays, e.g.
> `v0.2.13`, only the `sha256` changes) such that:
> 1. every request already accepted by the previous instance is allowed to
>    finish — we do **not** kill an instance while it is still processing;
> 2. we stop/kill the old instance **only after** the new one is ready and
>    reachable;
> 3. once the new instance is reachable we route **new** requests to it, while
>    the old instance keeps **draining** its in-flight requests.

This document has two parts:

1. **Part 1 — versiond (detailed).** A concrete, blue/green + drain design for
   the existing `versioned/` supervisor and `devshardd` child (governance binary
   swap), plus a separate **`versiond-router` host-evacuation** track for HA
   (§1.7–§1.8).
2. **Part 2 — Kubernetes (high level).** A non-detailed sketch of how the same
   guarantees map onto a future K8s deployment.

---

## 0. Where we are today (baseline)

### versiond supervisor

`versiond` polls the oracle every `VERSIOND_POLL_INTERVAL` (30s default) and
reconciles desired vs. running children.

- Routing is an in-process reverse proxy keyed by the version prefix:
  `/<version>/...` → `localhost:<port>`. The route table is an
  `atomic.Value` holding `map[versionName]host:port`.

```15:57:versioned/internal/proxy/proxy.go
func Handler(routes *atomic.Value) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		parts := strings.SplitN(path, "/", 2)
		...
		routeMap := routes.Load().(map[string]string)
		target, ok := routeMap[version]
		...
		p := &httputil.ReverseProxy{ ... FlushInterval: -1 }
		p.ServeHTTP(w, r)
	})
}
```

- Same-name/new-sha is detected in `Reconcile` and handled by `downloadAndSwap`:
  it downloads the new binary first, then **stops the old child and starts the
  new one on the same port**.

```392:419:versioned/internal/process/manager.go
// downloadAndSwap downloads the new binary, then atomically replaces the old one.
// The old process is stopped only after the new binary is on disk.
func (m *Manager) downloadAndSwap(ctx context.Context, v oracle.Version, sha string, old *child) error {
	dlErr := m.downloadBinary(ctx, v, sha)
	...
	// Stop old process after new binary is on disk.
	old.cancel()
	waitForChild(old, 5*time.Second)

	m.mu.Lock()
	delete(m.downloading, v.Name)
	delete(m.processes, v.Name)
	m.startChild(ctx, v)
	m.mu.Unlock()
	return nil
}
```

- A child is only added to the route table when its status is `running`, and a
  child reaches `running` after a **TCP-accept** probe (not a readiness probe):

```576:583:versioned/internal/process/manager.go
		// Wait for the child to start accepting connections before routing traffic.
		if !waitForPort(ctx, c.port, 10*time.Second) {
			slog.Warn("child did not start listening in time, routing anyway", "version", c.version.Name)
		}
		m.mu.Lock()
		c.status = statusRunning
		m.rebuildRoutes()
		m.mu.Unlock()
```

- On stop, the child is sent `SIGTERM`, then `SIGKILL` after 5s
  (`cmd.WaitDelay`), and `waitForChild` only waits 5s.

```559:563:versioned/internal/process/manager.go
		cmd.Cancel = func() error {
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		cmd.WaitDelay = 5 * time.Second // SIGKILL after 5s if SIGTERM didn't work
```

### devshardd child

- HTTP server (Echo) exposes `GET /healthz` that always returns `ok` — this is
  a **liveness** signal, not a readiness or drain signal.

```24:24:devshard/cmd/devshardd/server.go
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
```

- On `SIGTERM` it does `server.Shutdown(ctx)` with a **5s** grace window, so any
  request longer than ~5s (long SSE `/chat/completions`, validation) is cut off.

```306:321:devshard/cmd/devshardd/app.go
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = a.server.Shutdown(shutdownCtx)
```

- It already tracks in-flight work via a Prometheus gauge `devshard_inflight`
  (by stage), so the supervisor has a data source for "is this child idle?".

```54:57:devshard/observability/metrics_lifecycle.go
	inflight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "devshard_inflight",
		Help: "In-flight devshard operations by stage.",
	}, []string{"stage"})
```

### Gap analysis vs. the requirement

| Requirement | Today | Gap |
|---|---|---|
| New ready before traffic | TCP-accept only, then routed | No real readiness gate |
| New reachable → route new requests to it | Route swapped after start | OK at proxy layer (per-request reverse proxy) |
| Old finishes in-flight | `SIGTERM` then `SIGKILL` after 5s | **Old is killed, not drained** |
| Don't kill old until idle | `waitForChild(old, 5s)` | **No idle wait** |
| Same name, new binary | stop-then-start on **same port** | **Brief 404 gap; old + new can't coexist** |

The single blocking fact: to keep the old child alive **while** the new child is
already ready, both must run **at the same time, on different ports**. The
current swap is stop-then-start on one port, so the two can never overlap.

---

## Part 1 — versiond rolling update (detailed)

### 1.1 Design summary

Convert the in-place swap into a **blue/green + drain** swap inside versiond:

```
poll detects same name, new sha
        │
        ▼
download new binary to disk (old keeps running, already exec'd in memory)
        │
        ▼
start NEW child on a NEW port            ← old child keeps serving on old port
        │
        ▼
wait for NEW child READINESS (HTTP /ready, not just TCP)
        │
        ▼
atomic route swap: version → NEW port    ← new requests go to NEW child
        │                                   in-flight requests stay on OLD conn
        ▼
mark OLD child "draining" (out of route table, NOT killed)
        │
        ▼
poll OLD child in-flight count until 0  OR  VERSIOND_DRAIN_TIMEOUT
        │
        ▼
SIGTERM OLD child  → wait (long grace) → SIGKILL only as last resort
```

At the proxy layer this is already graceful: `proxy.Handler` builds a
`httputil.ReverseProxy` **per request** and dials the target named in the route
table at that moment. A request that already started keeps its own connection to
the old child until it completes; only *new* requests observe the swapped route.
The only thing we must change is: **stop killing the old child immediately**.

### 1.2 Storage prerequisite (must resolve first)

Rolling update means old + new `devshardd` run **concurrently**. That only
works when durable state lives in a **shared external database** (Postgres in a
separate process). **SQLite is not supported** for rolling update.

- **Postgres** (`PGHOST` + related `PG*` env): session store (`devshard/storage`),
  payloads, and validation leases are multi-writer and shared — safe for
  overlap. Validation leases dedupe duplicate validation across instances.
- **SQLite** (`devshard/storage` under `cfg.DataDir`): single-writer file.
  Two `devshardd` processes opening the same data dir will contend / corrupt.
  Do not enable blue/green drain without Postgres.

**Requirement:** point every child at the same external Postgres before enabling
overlap. versiond must fail closed to stop/start when `PGHOST` is unset or when
the devshard data dir still has SQLite-owned sessions in `_meta.db`.
Single-instance / local dev without overlap may keep using SQLite; see
`devshard/docs/storage-design.md` (storage-mode selection) and
`devshard/docs/release-0.2.13-v2-r2.md` (HA ⇒ Postgres).

### 1.3 devshardd changes

New `devshardd` exposes lifecycle controls on a loopback admin listener selected
by versiond through `DEVSHARD_ADMIN_ADDR`. These endpoints are not registered on
the public devshard traffic listener.

1. **Readiness endpoint** `GET /ready` on the admin listener (distinct from
   `/healthz`):
   - returns `200` only after chain runtime is connected, host manager has
     recovered sessions, store is started, and the listener is accepting.
   - returns `503` until then.
   - This is what versiond gates the route swap on (replaces the TCP-only
     `waitForPort`).

2. **Drain/idle endpoint** `GET /drain/status` on the admin listener:
   - returns active in-flight HTTP count from the lifecycle middleware. versiond
     polls this to decide the old child is idle.
   - exports the same count as Prometheus gauge
     `devshardd_lifecycle_inflight_requests`.
   - `POST /drain` on the same admin listener flips the child into "reject new,
     finish existing" mode as a belt-and-braces measure (route is already
     swapped, so new traffic shouldn't arrive, but this guards retries/direct
     hits). It must not be exposed through the public versiond proxy.

3. **Honor a long shutdown grace.** Replace the hard 5s in `app.go` with a
   configurable `DEVSHARD_SHUTDOWN_GRACE` (default large enough for max
   inference, e.g. 10m). On `SIGTERM`, stop accepting new requests but let
   in-flight ones finish up to the grace window.

   ```306:310:devshard/cmd/devshardd/app.go
   	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
   	defer shutdownCancel()
   	_ = a.server.Shutdown(shutdownCtx)
   ```
   → make `5*time.Second` come from config.

### 1.4 versiond changes

#### a) Allow two children per version name

Today `Manager.processes` is `map[string]*child` (one child per name). Introduce
a notion of `current` (serving) + `draining` children so a name can have both
during a swap. Minimal shape:

- keep `processes map[string]*child` for the **serving** child (route table key),
- add `draining []*child` (or `map[string][]*child`) for children that have been
  taken out of the route table but are still finishing work.

`rebuildRoutes` already only emits running children; ensure draining children are
**excluded** from the route table (they keep their port but receive no new
traffic).

```653:661:versioned/internal/process/manager.go
func (m *Manager) rebuildRoutes() {
	routes := make(map[string]string)
	for _, c := range m.processes {
		if c.status == statusRunning {
			routes[c.version.Name] = fmt.Sprintf("localhost:%d", c.port)
		}
	}
	m.routes.Store(routes)
}
```

#### b) New child gets a NEW port

`assignPort` currently returns a **stable** port per name, which forces overlap
onto the same port. For a swap, allocate a fresh port for the incoming child so
old and new coexist; release the old port after the old child fully exits.

```60:71:versioned/internal/process/manager.go
func (m *Manager) assignPort(name string) int {
	if port, ok := m.assignedPorts[name]; ok {
		return port
	}
	port := m.nextPort
	m.nextPort++
	m.assignedPorts[name] = port
	return port
}
```
→ add a swap-aware allocation (e.g. `assignSwapPort(name)`) that returns a new
port even when `name` already has one, and a `releasePort` on drain completion.

#### c) Readiness gate instead of TCP-accept

Replace/augment `waitForPort` with an HTTP readiness probe against the new
child's `/ready`, with timeout `VERSIOND_READY_TIMEOUT`. Only mark the new child
`running` (and thus routable) once `/ready` returns `200`.

```631:649:versioned/internal/process/manager.go
func waitForPort(ctx context.Context, port int, timeout time.Duration) bool {
	...
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
	...
}
```
→ add `waitForReady(ctx, port, path, timeout)` doing an HTTP GET on `/ready`.

#### d) Rewrite `downloadAndSwap` as blue/green + drain

New flow (replaces lines 392–419):

```text
1. downloadBinary(new sha)                      // old child untouched in memory
2. newChild = startChild(version, NEW port)   // same PGHOST / shared Postgres, no SQLite-owned sessions
3. if !waitForReady(newChild, VERSIOND_READY_TIMEOUT):
        stop newChild; keep old serving; abort swap (retry next poll)
4. lock: move old child from processes -> draining[name]
         set processes[name] = newChild (status running)
         rebuildRoutes()        // route now points to NEW port
   unlock
5. go drainOld(oldChild):
        deadline = now + VERSIOND_DRAIN_TIMEOUT
        loop every VERSIOND_DRAIN_POLL_INTERVAL:
            if inflight(oldChild) == 0: break
            if now > deadline: log warn; break
        oldChild.cancel()                       // SIGTERM, long WaitDelay
        waitForChild(oldChild, max(VERSIOND_DRAIN_KILL_GRACE, DEVSHARD_SHUTDOWN_GRACE))
        release oldChild port
```

Key invariants this enforces:

- **New ready before traffic:** route is swapped only after `/ready` is `200`
  (step 3–4).
- **Route new requests to new:** step 4 atomic route swap.
- **Old finishes in-flight:** step 5 waits for `inflight == 0` before any
  signal; the proxy keeps existing requests on the old connection meanwhile.
- **Don't kill until idle:** `SIGTERM` is sent only after idle or the safety
  `VERSIOND_DRAIN_TIMEOUT`.

#### e) `child` status + lifetimes

Add `statusDraining` to the status enum and surface it in `/healthz` (`Status()`
output) so operators can observe a drain in progress.

```77:89:versioned/internal/process/manager.go
func (m *Manager) Status() []health.StatusEntry {
	...
	for _, c := range m.processes {
		out = append(out, health.StatusEntry{Name: c.version.Name, Port: c.port, Status: c.status})
	}
	return out
}
```
→ also iterate `draining[...]` so draining children appear in `/healthz`.

#### f) Removed versions drain asynchronously

When governance removes a version from the approved set, versiond must remove
the route immediately so no new requests reach that version. The old child is
then moved from `processes` to `draining`, marked `restart=false`, and stopped
through the same `drainAndStop` path used by binary swaps.

This keeps the reconcile poll loop responsive while the old child finishes
accepted work. New requests get `404` for the removed version, existing
requests stay on their already-open connection, and legacy children without
`/drain/status` receive the `VERSIOND_DRAIN_KILL_GRACE` cushion before
`SIGTERM`.

#### g) Graceful supervisor shutdown

`Manager.Shutdown` must wait long enough for child graceful shutdown. When
versiond itself is being stopped, children are `SIGTERM`'d and waited on with the
same per-child stop timeout used elsewhere: exactly `VERSIOND_DRAIN_KILL_GRACE`
for non-devshard binaries, and the max of `VERSIOND_DRAIN_KILL_GRACE` and
`DEVSHARD_SHUTDOWN_GRACE` for devshardd.

```492:509:versioned/internal/process/manager.go
func (m *Manager) Shutdown(ctx context.Context) error {
	...
	for _, c := range children {
		waitForChild(c, 10*time.Second)
	}
	return nil
}
```

### 1.5 New configuration (versiond `config.Config`)

Add to `versioned/internal/config/config.go`:

| Env var | Default | Meaning |
|---|---|---|
| `VERSIOND_READY_PATH` | `/ready` | devshardd readiness path the supervisor probes |
| `VERSIOND_READY_TIMEOUT` | `60s` | max wait for new child to become ready before aborting swap |
| `VERSIOND_DRAIN_PATH` | `/drain` | path versiond POSTs to put the old child into drain mode |
| `VERSIOND_DRAIN_STATUS_PATH` | `/drain/status` | path versiond polls for the old child's in-flight count |
| `VERSIOND_DRAIN_TIMEOUT` | `15m` | max time to wait for old child to go idle before `SIGTERM` |
| `VERSIOND_DRAIN_POLL_INTERVAL` | `1s` | how often to poll old child in-flight count |
| `VERSIOND_DRAIN_KILL_GRACE` | `10m` | legacy no-status drain cushion and child stop backstop |

And on the child side: `DEVSHARD_SHUTDOWN_GRACE` (default `10m`) consumed in
`app.go`. For new `devshardd` binaries, versiond also sets
`DEVSHARD_ADMIN_ADDR=127.0.0.1:<port>` after the binary advertises admin API
support with `--print-admin-api-version`. Operators normally do not set this
manually; it is the private lifecycle channel between versiond and its child.

> Note: `cmd.WaitDelay` must not be shorter than the child's own graceful
> shutdown window. For non-devshard binaries, versiond waits exactly
> `VERSIOND_DRAIN_KILL_GRACE` after `SIGTERM`. For devshardd, versiond uses
> the max of `VERSIOND_DRAIN_KILL_GRACE` and `DEVSHARD_SHUTDOWN_GRACE`.
> For legacy children without `/drain/status`, the same
> `VERSIOND_DRAIN_KILL_GRACE` is also the pre-`SIGTERM` cushion because
> versiond cannot observe in-flight work.

versiond also garbage-collects old complete per-sha install directories under
`bin/<version>/<sha>/`, keeping desired/live/draining installs and a small
recent complete-install cushion. In-progress download directories without
`install.json` are never removed by GC.

#### Legacy compatibility and oracle validation

The first versiond upgrade can manage binaries built before these endpoints and
preflight flags existed. Compatibility is intentionally narrow:

- `--print-binary-version` and `--print-protocol-version` preflight checks fall
  back only when the child exits with a recognized "unsupported flag" usage
  error. Timeouts, signals, OOM-like failures, and other execution errors fail
  closed so the next poll can retry.
- `--print-admin-api-version` is an optional capability probe for devshardd
  binaries. If it is supported, versiond starts the child with
  `DEVSHARD_ADMIN_ADDR` and sends readiness/drain traffic to that private
  listener. If it is unsupported, versiond keeps using the legacy public
  lifecycle paths for that child.
- readiness fallback applies only to the default `VERSIOND_READY_PATH=/ready`.
  If `/ready` is missing with 404/405/501, versiond tries `/healthz`, then a TCP
  connect probe. Custom readiness paths do not use this fallback.
- drain status fallback treats 404/405/501 from `/drain/status` as a legacy
  child. versiond waits `VERSIOND_DRAIN_KILL_GRACE` before `SIGTERM` because
  it cannot observe whether old in-flight work is still running, instead of
  waiting the full `VERSIOND_DRAIN_TIMEOUT`.

This keeps old released devshardd binaries deployable under a new versiond while
still failing closed for ambiguous failures. One consequence of the legacy drain
fallback is that an in-flight request on an old binary longer than
`VERSIOND_DRAIN_KILL_GRACE` plus that binary's own shutdown window can be cut
during the first legacy-to-new swap; stamped binaries with `/drain/status` get
the full idle wait.

Oracle data is validated before reconciliation. Version names must be simple
path components, unique, and non-empty; sha256 values must be 64 hex characters.
An invalid oracle response fails the fetch for that poll and leaves the current
children running unchanged. This is stricter than skipping bad entries because
the oracle reflects governance-approved chain params and should be internally
consistent.

### 1.6 Single-instance limits (be honest)

Even with blue/green + drain inside **one** versiond:

- The window is **one swap per version name at a time** — fine for "same name,
  new binary".
- Escrows already mapped to the old child stay there until they finish; new
  escrows go to the new child. With a single versiond there is no consistent
  hashing, so **all new requests** go to the new child after the swap — which is
  exactly what we want for a binary rollout.

### 1.7 Two drain layers (do not conflate)

Part 1 (§1.1–§1.6) and `versiond-router` drain solve **different events** at
**different layers**. They are not substitutes for each other.

| Event | Who handles drain | Router involved? |
|---|---|---|
| Same version **name**, new **sha256** (governance binary update) | **versiond** blue/green + devshardd child drain (§1.1) | **No** — versiond stays up on `:8080`; only the devshardd child swaps |
| **versiond host** removal, replacement, or supervisor upgrade | **`versiond-router`** (or K8s Service — Part 2) | **Yes** — whole process/container leaves the pool |

During a devshardd binary swap, sticky routing is unchanged:

```text
versiond-router → versiond-2:8080   (same upstream throughout)
                      └─ versiond proxy: old devshardd :9001 → new :9002
```

The router never sees the child swap. **Do not** remove a versiond upstream from
the router pool for a governance sha change — that would unnecessarily evacuate
every escrow pinned to that host. In-versiond drain (§1.1) is the correct tool
for binary rollout.

Router drain is only needed when the **versiond process itself** must stop
(restart, replace, scale-down, host maintenance, versiond binary upgrade). Killing
versiond kills its in-process proxy and all devshardd children regardless of
§1.1 drain logic.

### 1.8 versiond-router: draining versiond hosts (HA)

When N versiond instances sit behind `versiond-router` (nginx consistent hash on
escrow ID — see `versiond-router/nginx.conf.template`), **removal or replacement
of a versiond host** must be managed at the router layer. This is a separate
operational track from §1.1; it does not replace and is not required for
devshardd binary swaps.

Today `versiond-router` only renders a static upstream list from `VERSIOND_HOSTS`
(`versiond-router/entrypoint.sh`). It has **no drain support** — that must be
added (or handled by an operator runbook until automated).

#### Target flow (evacuate one versiond host)

Applies when taking `versiond-N` out of service: container replace, supervisor
upgrade, scale-down, or decommission.

```text
1. Mark versiond-N down in router upstream (reload nginx config)
        │  → no NEW requests hashed to versiond-N
        │  → in-flight connections to versiond-N keep running
        ▼
2. Poll versiond-N until idle:
        GET versiond-N:8080/healthz  (child status visibility)
        and aggregate devshardd GET /drain/status on that host
        loop until inflight == 0  OR  ROUTER_DRAIN_TIMEOUT
        ▼
3. Graceful stop versiond-N:
        SIGTERM versiond  →  versiond.Shutdown waits on children (§1.4f)
        wait up to ROUTER_DRAIN_KILL_GRACE  →  SIGKILL only as backstop
        ▼
4. Kill process / free machine:
        stop container or release VM; remove from VERSIOND_HOSTS; reload router
        (or leave marked down if host is gone permanently)
        ▼
5. (Replacement only) Start new versiond-N, wait until healthy, re-add upstream
```

Key invariants:

- **Stop new traffic first:** router marks the upstream `down` (or removes it)
  before any `SIGTERM` to versiond. Consistent hash means escrows already on
  `versiond-N` cannot fail over to another replica — that instance must drain
  its pinned escrows before exit.
- **Drain before kill:** do not free the machine until step 2 reports idle (or
  the safety timeout fires with an operator-visible warning).
- **One host at a time:** with `N−1` replicas still in the pool, other escrows
  keep serving while one host evacuates.

#### What to build (router track)

| Piece | Meaning |
|---|---|
| Upstream `down` / removal + `nginx -s reload` | Stop routing new escrows to the host being evacuated |
| `ROUTER_DRAIN_TIMEOUT` | Max wait for a host to go idle before forced stop |
| `ROUTER_DRAIN_POLL_INTERVAL` | How often to poll versiond `/healthz`; direct devshardd `/drain/status` polling requires access to the admin listener |
| `ROUTER_DRAIN_KILL_GRACE` | Wait after `SIGTERM` to versiond before `SIGKILL` / container kill |
| Operator script or sidecar | Orchestrate steps 1–4; re-render `VERSIOND_HOSTS` and reload |

Re-use versiond `/healthz` draining visibility from §1.4e for public host
evacuation. The devshardd endpoints from §1.3 (`/ready`, `/drain/status`,
`/drain`) are an internal admin API for versiond-managed children; consume them
directly only from a trusted sidecar or local supervisor with admin listener
access.

#### When to use which layer

- **Governance publishes new sha256 for an existing name** → §1.1 only (every
  versiond reconciles independently; router unchanged).
- **Replace or remove a versiond host** → §1.8 only (router drain, then kill).
- **Both at once** (e.g. new devshardd binary *and* new versiond supervisor on
  the same machine) → §1.1 swap first while the host stays in the pool, *then*
  §1.8 if the host itself must leave; or evacuate via §1.8 and start fresh on
  a new host (coarser, acceptable for maintenance windows).

Part 2 (K8s) maps the same host-evacuation semantics onto Service endpoints +
`preStop` instead of nginx reload; it is the same layer as §1.8, not §1.1.

### 1.9 Test plan

- **Unit (`versioned/internal/process`):** extend `manager_test.go` with a swap
  scenario asserting: old child still routed/alive until new `/ready`; route
  points to new port after readiness; old child not `SIGTERM`'d until inflight
  reports 0; old killed at `VERSIOND_DRAIN_TIMEOUT`.
- **e2e (`versioned/e2e`):** drive a long request against the old child, trigger
  an oracle sha change for the same name, assert the long request completes with
  the old binary while a concurrently-started request is served by the new one.
- **devshardd:** test `/ready` flips only after init and chain subscriptions;
  `/drain/status` and `devshardd_lifecycle_inflight_requests` reflect lifecycle
  in-flight HTTP requests; `SIGTERM` honors `DEVSHARD_SHUTDOWN_GRACE`.

### 1.10 Rollout order

**Track A — devshardd binary swap (§1.1–§1.6, required for governance updates):**

1. devshardd: `/ready`, `/drain/status`, configurable shutdown grace.
2. versiond: config flags (no behavior change yet).
3. versiond: per-name two-child model + swap-aware ports + readiness probe.
4. versiond: rewrite `downloadAndSwap` to blue/green + drain.
5. Surface draining state in `/healthz`; long `WaitDelay` for drained children.
6. Tests (§1.9), then enable by default.

**Track B — versiond host removal/replacement (§1.8, HA only):**

7. versiond-router: upstream `down` + reload; drain poll loop; operator script
   or sidecar for graceful stop → kill → free machine → re-add on replace.
8. Tests: long request pinned to `versiond-N`; mark upstream down; assert
   request completes; assert no new requests arrive; assert process exit after idle.

---

## Part 2 — Kubernetes deployment (non-detailed)

The same three guarantees map cleanly onto native K8s primitives; the goal is to
let the platform do drain/readiness and keep `devshardd`/`versiond` stateless
enough to be rescheduled.

### 2.1 Shape

- Run `devshardd` (or `versiond`+`devshardd`) as a `Deployment` behind a
  `Service`. Shared Postgres stays external (multi-writer, as today).
- Put a **sticky** layer in front for escrow affinity: either the existing
  `versiond-router` pattern (nginx consistent hash on escrow ID) or an
  ingress / service mesh with consistent hashing on the escrow path segment.
- **Pod/host evacuation** (Part 1 §1.8) maps to Service endpoint removal +
  `preStop` below — not to the in-versiond devshardd binary swap in §1.1.

### 2.2 Rolling update strategy

```yaml
strategy:
  type: RollingUpdate
  rollingUpdate:
    maxUnavailable: 0   # never drop capacity
    maxSurge: 1         # bring new pod up first
```

- `maxUnavailable: 0` + `maxSurge: 1` → new pod is created and must pass
  readiness **before** an old pod is removed (new ready before traffic).

### 2.3 Readiness + drain

- **readinessProbe** → `GET /ready` on a private/admin devshardd listener, not
  through the public inference path. Endpoints only include a pod once it is
  truly ready; new traffic flows to the new pod automatically.
- **terminationGracePeriodSeconds**: large (cover max inference, e.g. minutes)
  so in-flight requests can finish after the pod is told to stop.
- **preStop hook**: fail readiness / `sleep` so the pod is removed from Service
  endpoints **before** `SIGTERM`, then let in-flight requests drain. devshardd's
  configurable shutdown grace (`DEVSHARD_SHUTDOWN_GRACE`) must be ≤
  `terminationGracePeriodSeconds`.

```text
new pod created → readinessProbe 200 → added to Service endpoints
old pod: preStop (drop from endpoints + sleep) → SIGTERM → finish in-flight
         → exits before terminationGracePeriodSeconds → SIGKILL only as backstop
```

### 2.4 State considerations

- **Postgres required** for rolling update (same as Part 1 §1.2): session store,
  payloads, and validation leases must live in shared external Postgres so old
  and new pods can overlap safely. SQLite is not supported for pod replacement
  with concurrent overlap.
- Keep pods otherwise stateless: `cfg.DataDir` holds only routing markers
  (e.g. `.pg-bound`), not session data.

### 2.5 What carries over from Part 1

- devshardd admin `/ready` + `/drain` + configurable shutdown grace are the
  **same** building blocks K8s probes and hooks consume — implement them once
  for both.
- The drain/idle accounting (`devshard_inflight`) feeds the versiond binary-swap
  drain loop (§1.1), the versiond-router host-evacuation loop (§1.8), and K8s
  preStop logic / dashboards.
- §1.1 (binary swap inside a live versiond) and §1.8 / this section (whole
  pod/host removal) remain separate: K8s `RollingUpdate` handles pod lifecycle;
  versiond `downloadAndSwap` handles governance sha changes without pod restart.

### 2.6 Not in scope here

- Helm/manifests, HPA, PodDisruptionBudget, mesh config — to be detailed when
  the K8s track is picked up. This section only fixes the rollout **semantics**
  so they match the versiond behavior.
