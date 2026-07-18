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
        │
        ▼
retire OLD proxy target                  ← stale route lookups retry on NEW
        │                                   accepted OLD requests keep a lease
        ▼
wait for OLD proxy leases to reach 0  OR  VERSIOND_DRAIN_TIMEOUT
        │
        ▼
POST /drain to OLD child                 ← reject non-proxy late arrivals
        │
        ▼
poll OLD child in-flight count until 0  OR  VERSIOND_DRAIN_TIMEOUT
        │
        ▼
SIGTERM OLD child  → wait (long grace) → SIGKILL only as last resort
```

The proxy route value is a generation-specific `Target`, not just an address.
Before forwarding, each request acquires a lease on that target and releases it
after the complete response, including an SSE stream. The route swap publishes
the new target before retiring the old one. A request that loaded the old target
but did not acquire it before retirement retries against the new route; an
already acquired request keeps the old target non-idle until it completes. This
closes the boundary between selecting an old address and entering the old
child's own lifecycle middleware.

### 1.2 Storage prerequisite

Rolling update means old + new `devshardd` run **concurrently**. That only
works when durable state lives in **Postgres-only** storage. SQLite and hybrid
fallback modes are not safe for overlap: two children can touch the same local
data dir, and a new HA child can migrate/quarantine SQLite files while an old
hybrid child is still running.

- **Postgres-only** (`DEVSHARD_STORAGE_MODE=postgres`, with `PGHOST` / `PG*`
  connection env): sessions, payloads, and validation leases are external and
  shared. This is the only mode that permits blue/green overlap.
- **SQLite** or **hybrid** (`sqlite`, `hybrid`, or default `auto` with `PGHOST`
  resolving to `hybrid`): local fallback can write files under the child's data
  dir. Do not run old and new children concurrently in these modes.

versiond does not reimplement storage-mode resolution. For devshard children it
probes the binary that will actually run:

- the currently running child records its startup answer to
  `--print-storage-mode`;
- the incoming binary is probed with `--print-storage-mode` before overlap;
- blue/green is allowed only when **both** answers are exactly `postgres`.

Any uncertainty — old binary without the flag, new binary without the flag,
invalid env, a non-`postgres` mode, or a future unknown mode — fails closed to
the compatible stop-then-start path. This makes the first migration into
`DEVSHARD_STORAGE_MODE=postgres` exclusive, and only later updates of
Postgres-only binaries use blue/green.

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

`rebuildRoutes` only emits running children. Each route value is the `Target`
for one concrete child generation, so same-name children with different SHA,
port, and PID never share admission state. Draining children are excluded from
the new table, but their retired targets stay alive until acquired proxy
requests release their leases.

```653:661:versioned/internal/process/manager.go
func (m *Manager) rebuildRoutes() {
	previous := m.routes.Load().(proxy.RouteTable)
	routes := make(proxy.RouteTable)
	for _, c := range m.processes {
		if c.status == statusRunning {
			routes[c.version.Name] = c.proxyTarget
		}
	}
	m.routes.Store(routes)
	for version, target := range previous {
		if routes[version] != target {
			target.Retire()
		}
	}
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
The implementation uses a bounded child-port pool starting at `BasePort`,
reuses ports after child exit, and reserves versiond's own listen port so a
long-lived supervisor does not eventually allocate it to a child. Port-pool
exhaustion is returned as an explicit start error: versiond does not execute or
route a child without all required ports, and a rolling-update candidate that
cannot obtain a port leaves the current child serving traffic.

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
2. require old+new --print-storage-mode == postgres, else stop/start fallback
3. newChild = startChild(version, NEW port)   // Postgres-only overlap
4. if admin /ready != 200 OR public /healthz != 2xx
      within VERSIOND_READY_TIMEOUT:
        stop newChild; keep old serving; abort swap (retry next poll)
5. lock: move old child from processes -> draining[name]
         set processes[name] = newChild (status running)
         rebuildRoutes()        // route now points to NEW port
         retire old proxy target
   unlock
6. go drainOld(oldChild):
        deadline = now + VERSIOND_DRAIN_TIMEOUT
        wait until proxy leases on old target == 0, or deadline
        POST oldChild /drain
        loop every VERSIOND_DRAIN_POLL_INTERVAL:
            if inflight(oldChild) == 0: break
            if now > deadline: log warn; break
        oldChild.Stop()                         // supervisor sends SIGTERM
        waitForChild(oldChild)                  // confirmed exit and process reap
        release oldChild port
```

Key invariants this enforces:

- **New ready before traffic:** route is swapped only while admin `/ready` is
  `200` and public `/healthz` is `2xx` (step 4–5). Both conditions are
  rechecked together so logical readiness cannot hide a failed public bind.
- **Route new requests to new:** step 5 atomic route swap.
- **No boundary gap:** a stale route lookup either owns an old-target lease or
  retries after retirement and uses the new target.
- **Old finishes in-flight:** step 6 first waits for proxy leases, then confirms
  the child's lifecycle `inflight` count is zero before any signal.
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
requests retain their old-target proxy lease, and child drain starts only after
those leases are released. Legacy children without `/drain/status` receive the
`VERSIOND_DRAIN_KILL_GRACE` cushion before `SIGTERM`.

If governance restores the same version name before its removed child finishes
draining, versiond defers the new start until that child exits. This prevents
two generations from opening the same version data directory. The next
reconcile poll starts the restored version normally after drain completes.

#### g) Graceful supervisor shutdown

Each concrete child process is owned by a small process lifecycle state machine:

```text
Running -> Terminating -> Killing -> Exited
```

`Stop()` moves a running process to `Terminating`, sends `SIGTERM` to its
process group, and starts its graceful-stop timer. If the process has not
exited when that timer expires, the controller moves it to `Killing` and sends
`SIGKILL`. `Done()` is closed only after `cmd.Wait()` confirms the process has
exited and has been reaped. The graceful timeout therefore controls
escalation; it is not a second, competing limit on how long callers wait.

The timeout is exactly `VERSIOND_DRAIN_KILL_GRACE` for non-devshard binaries,
and the max of `VERSIOND_DRAIN_KILL_GRACE` and `DEVSHARD_SHUTDOWN_GRACE` for
devshardd.

When versiond itself stops, `Manager.Shutdown` calls `Stop()` for all current
and draining children before waiting for any one of them. If the manager
shutdown context expires, it calls `ForceStop()` for the remaining children,
but still waits for every `Done()` signal so it never returns while owning an
unreaped child.

```492:509:versioned/internal/process/manager.go
func (m *Manager) Shutdown(ctx context.Context) error {
	...
	for _, c := range children {
		c.Stop()
	}
	select {
	case <-allChildrenDone:
		return nil
	case <-ctx.Done():
		forceStopAll(children)
		<-allChildrenDone
		return ctx.Err()
	}
}
```

This is deliberately a process-level FSM, not the Track B host lifecycle.
The Track B Host FSM and router controller use the same
`Stop`/`ForceStop`/`Done` contract after router admission and host drain have
completed, without putting host-routing policy into the child supervisor.

### 1.5 New configuration (versiond `config.Config`)

The implementation exposes these settings from
`versioned/internal/config/config.go`:

| Env var | Default | Meaning |
|---|---|---|
| `VERSIOND_READY_PATH` | `/ready` | devshardd admin readiness path; public `/healthz` must also pass before routing |
| `VERSIOND_READY_TIMEOUT` | `60s` | max wait for new child to become ready before aborting swap |
| `VERSIOND_DRAIN_PATH` | `/drain` | path versiond POSTs to put the old child into drain mode |
| `VERSIOND_DRAIN_STATUS_PATH` | `/drain/status` | path versiond polls for the old child's in-flight count |
| `VERSIOND_DRAIN_TIMEOUT` | `15m` | shared deadline for old proxy leases and child in-flight work before `SIGTERM` |
| `VERSIOND_DRAIN_POLL_INTERVAL` | `1s` | how often to poll old child in-flight count |
| `VERSIOND_DRAIN_KILL_GRACE` | `10m` | legacy no-status drain cushion and child stop backstop |
| `VERSIOND_HOST_DRAIN_TIMEOUT` | `15m` | host-level budget for accepted proxy work and all child lifecycle counters after versiond receives `SIGTERM` |

And on the child side: `DEVSHARD_SHUTDOWN_GRACE` (default `10m`) consumed in
`app.go`. For new `devshardd` binaries, versiond also sets
`DEVSHARD_ADMIN_ADDR=127.0.0.1:<port>` after the binary advertises admin API
support with `--print-admin-api-version`. Operators normally do not set this
manually; it is the private lifecycle channel between versiond and its child.

> Note: the process lifecycle FSM uses `VERSIOND_DRAIN_KILL_GRACE` as the
> `SIGTERM`-to-`SIGKILL` interval for non-devshard binaries. For devshardd,
> versiond uses the max of `VERSIOND_DRAIN_KILL_GRACE` and
> `DEVSHARD_SHUTDOWN_GRACE`.
> For legacy children without `/drain/status`, the same
> `VERSIOND_DRAIN_KILL_GRACE` is also the pre-`SIGTERM` cushion because
> versiond cannot observe in-flight work.

versiond also garbage-collects old complete per-sha install directories under
`bin/<version>/<sha>/`, keeping desired/live/draining installs and a small
recent complete-install cushion. In-progress download directories without
`install.json` are never removed by GC.

On the first upgrade from the legacy flat layout, versiond checks
`bin/<version>/<binary>` against the adjacent legacy `install.json` before
using the network. A matching install is copied atomically into the canonical
`bin/<version>/<sha>/` directory, with the binary written first and
`install.json` published last as the commit marker. The promoted copy is
verified again before it starts.

The legacy binary and metadata are retained because another versiond instance
using the shared bin mount may still run the previous release. Concurrent
promotions are idempotent because they publish the same verified content with
atomic renames. If the canonical destination cannot be written, versiond logs
a warning and starts directly from the re-verified legacy path instead of
requiring an artifact download.

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
- `--print-storage-mode` is a devshardd storage safety probe. A child that does
  not support it can still start, but versiond treats its mode as unknown and
  will not blue/green-overlap it with another devshardd process. Rolling overlap
  opens only when the running child recorded `postgres` and the incoming binary
  also prints `postgres`.
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

`versiond-router` bootstraps a persistent upstream state from `VERSIOND_HOSTS`.
After bootstrap, `gonka-routerctl` owns that state and applies mutations as
validated nginx transactions. It exposes no network admin API; remote operators
invoke it through the deployment's existing SSH access.

The persisted router FSM is:

```text
active -> draining -> offline -> joining -> active
   ^          |
   +----------+  (cancel before versiond receives SIGTERM)
```

Only `active` hosts are live nginx upstreams. Other states are rendered with
the nginx `down` parameter. A successful `nginx -s reload` stops new sticky
assignments without terminating connections held by old nginx workers.

#### Target flow (evacuate one versiond host)

Applies when taking `versiond-N` out of service: container replace, supervisor
upgrade, scale-down, or decommission.

```text
1. Mark versiond-N down in router upstream (reload nginx config)
        │  → no NEW requests hashed to versiond-N
        │  → in-flight connections to versiond-N keep running
        ▼
2. Poll versiond-N until known idle:
        GET versiond-N:8080/healthz?summary=1
        loop until inflight == 0  OR  ROUTER_DRAIN_TIMEOUT
        ▼
3. Graceful stop versiond-N:
        disable automatic restart
        SIGTERM versiond  →  host FSM drains and reaps children
        wait up to ROUTER_DRAIN_KILL_GRACE  →  SIGKILL only as backstop
        ▼
4. Kill process / free machine:
        stop container or release VM; remove from VERSIOND_HOSTS; reload router
        (or leave marked down if host is gone permanently)
        ▼
5. (Replacement only) Start new versiond-N, wait until healthy, re-add upstream
```

Key invariants:

- **Stop new traffic first:** router marks the upstream `down`
  before any `SIGTERM` to versiond. An established HTTP/SSE request cannot be
  moved and must finish on its original host. A later request for the same
  escrow may be re-hashed to a survivor and recover from shared Postgres;
  affinity is placement, not exclusive in-memory ownership. Non-HA versions
  remain pinned to the legacy host and are protected by the router guard.
- **Drain before kill:** do not free the machine until step 2 reports idle (or
  the safety timeout fires with an operator-visible warning).
- **One host at a time:** with `N−1` replicas still in the pool, other escrows
  keep serving while one host evacuates.

`Devshard-Ha` remains based on the configured multi-host topology while one host
is down; changing it with active capacity could switch the survivor to an unsafe
non-HA storage mode. The normal guard rejects draining the last active host.
Forcing that transition is an explicit outage and leaves nginx returning `502`
until a host is activated.

#### Implemented controls

| Piece | Meaning |
|---|---|
| `gonka-routerctl` | Local, locked router FSM mutation with journal, `nginx -t`, atomic publish, reload, rollback, and audit |
| `gonka-hostctl` | Resumable SSH orchestration for evacuation and replacement; no network listener |
| `GET /healthz?summary=1` | Versioned host state, availability/convergence conditions, admission, and aggregate inflight |
| `VERSIOND_HOST_DRAIN_TIMEOUT` | Internal versiond budget for admission and child idle, default `15m` |
| `ROUTER_DRAIN_TIMEOUT` | External maximum wait for known host idle, default `15m` |
| `ROUTER_DRAIN_POLL_INTERVAL` | External health/process polling interval, default `2s` |
| `ROUTER_DRAIN_KILL_GRACE` | Wait after `SIGTERM` before `SIGKILL`, default `30m` |
| `ROUTER_COMMAND_TIMEOUT` | Deadline for one local or SSH command, default `30s` |

`ROUTER_DRAIN_KILL_GRACE` must exceed versiond's internal shutdown budget. Its
default covers `VERSIOND_HOST_DRAIN_TIMEOUT`, the default child shutdown grace,
and an escalation cushion.

The summary's `proxy_inflight` counts complete versiond proxy responses,
including full SSE stream lifetimes. `lifecycle_inflight` aggregates child
admin counters. Because they observe overlapping work, aggregate `inflight` is
their maximum rather than their sum. `idle=true` requires both zero work and
known counters for every child. Legacy children without `/drain/status` keep
`inflight_known=false`, forcing the timeout path instead of a false idle result.
Child lifecycle counters are sampled in the background and cached; a health
request never fans out to every child. `proxy_inflight` remains a live host
admission count, so a just-accepted request cannot be hidden by the cache.

`ready` means the host is serving, accepting, and has at least one routable
generation. Expected convergence is reported as `progressing=true` with the
`desired_children` and `running_children` counts. `degraded=true` and
`reconcile_error` are reserved for an actual reconcile or oracle failure; they
do not describe a routine generation transition. Healthy versions on the same
host remain available throughout either condition.

On `SIGTERM`, versiond transitions
`serving -> draining -> stopping -> stopped`. Admission closes immediately,
reconciliation and crash restarts freeze, accepted proxy requests finish,
children receive the existing `/drain` and idle sequence, and only then receive
`SIGTERM`. Repeated `SIGTERM` is idempotent so orchestration retries cannot force
the host accidentally; a second `SIGINT` is the explicit interactive transition
to `forcing`.

Active reconcile work is registered as a cancellable control operation. Host
drain cancels that registry before waiting for the poll worker. Child preflight,
downloads, readiness, and stop/start waits inherit the operation context, so a
non-overlap swap cannot make force handling unavailable. Child generations use
the typed lifecycle `preparing -> starting -> running -> retiring -> draining
-> stopping -> stopped`, with `failed -> starting` for supervised retry.

#### Operator commands

Build the local tools from `versiond-router`:

```bash
make build-tools
```

Evacuate a Docker-managed host:

```bash
.bin/gonka-hostctl evacuate \
  --operation-id maintenance-20260718-versiond2 \
  --router-ssh router.example.net \
  --router-runtime docker \
  --router-service versiond-router \
  --upstream versiond2 \
  --versiond-ssh worker-2.example.net \
  --versiond-runtime docker \
  --versiond-service versiond2
```

The command captures the original container restart policy once and reasserts
`restart=no` before every attempt to signal versiond. A durable phase is a
checkpoint, not proof that mutable external runtime state still matches it.
For systemd, use `--router-runtime systemd --versiond-runtime systemd`; the
orchestrator uses `systemctl stop --no-block` so `Restart=` cannot resurrect the
unit. Before changing router state, hostctl validates the runtime shutdown
contract. systemd's `TimeoutStopSec` directly bounds the managed stop job and
must cover `ROUTER_DRAIN_KILL_GRACE`; it must also use `KillMode=control-group`
or `mixed` with `SendSIGKILL=yes`. Hostctl signals Docker directly, while its
required `StopTimeout` protects external `docker stop`, Compose teardown, and
redeploy from undercutting the same budget. Compose deployments, including the
local test network, set `stop_grace_period: 30m` for versiond and
versiond-router. Rerun an interrupted command with the same operation ID and
journal path to continue after its last durable phase.

A direct `SIGTERM` that bypasses router drain closes versiond admission and may
return `503` to a new request already selected by nginx. The router deliberately
does not retry an already-sent inference `POST`: automatic replay could execute
the request twice. Operators must use `gonka-hostctl` for planned maintenance.

Before `SIGTERM`, every fresh or resumed operation repeats the idempotent router
`drain` transition. If evacuation must be abandoned before the durable
`term_requested` phase, run `gonka-hostctl cancel` with the same operation ID
and scope. Cancellation is a durable compensation FSM: it records the intent,
restores any disabled Docker restart policy, checkpoints that action, and then
reactivates the upstream. A failed cancellation must be resumed with `cancel`;
the forward evacuation FSM refuses to cross it. `term_requested` is persisted
before the SSH signal command; at or after it, the operation must be resumed to
`offline` because the remote outcome can be unknown.

After preparing a replacement container or unit, keep it out of the pool until
the replacement transaction completes:

```bash
.bin/gonka-hostctl replace \
  --operation-id replacement-20260718-versiond2 \
  --router-ssh router.example.net \
  --router-runtime docker \
  --router-service versiond-router \
  --upstream versiond2 \
  --upstream-address replacement-versiond2 \
  --versiond-ssh replacement-2.example.net \
  --versiond-runtime docker \
  --versiond-service versiond2 \
  --evacuation-journal \
    ~/.config/gonka/hostctl/maintenance-20260718-versiond2.json
```

The upstream remains `joining`/down until versiond reports `state=serving`,
`ready=true`, `accepting=true`, `available=true`, `reconciled=true`,
`progressing=false`, and `degraded=false`. Router state, recovery journal, and
audit log are stored below `/var/lib/gonka/versiond-router` on a persistent
volume. See `versiond-router/README.md` and `versiond-host-evacuation.md` for the
complete failure and recovery contract.

Docker replacement restores the exact policy captured by evacuation, including
an `on-failure` retry count. A newly provisioned service without that journal
must pass `--docker-restart-policy` explicitly. Policy resolution happens before
the host enters `joining`.

`gonka-routerctl status` never performs recovery or reload. It exposes an
unfinished journal as `pending_operation`; `gonka-routerctl recover` resolves it
under the controller lock. Pre-reload phases roll back. A `reloaded` phase rolls
forward only after the on-disk config matches the journaled SHA and passes
`nginx -t`.

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
  points to new port only after admin `/ready` and public `/healthz`; child
  drain waits for old proxy leases and lifecycle inflight to reach 0; old
  killed at `VERSIOND_DRAIN_TIMEOUT`. Test the process FSM separately:
  graceful `SIGTERM`, timeout escalation to `SIGKILL`, and manager shutdown
  returning only after process reap.
- **Unit (`versioned/internal/proxy`):** hold a request on the old target across
  a route swap, assert new requests use the new target, and assert the retired
  target becomes drained only after the old request completes.
- **e2e (`versioned/e2e`):** drive a long request against the old child, trigger
  an oracle sha change for the same name, assert the long request completes with
  the old binary while a concurrently-started request is served by the new one.
- **devshardd:** test `/ready` flips only after init and chain subscriptions;
  `/drain/status` and `devshardd_lifecycle_inflight_requests` reflect lifecycle
  in-flight HTTP requests; `SIGTERM` honors `DEVSHARD_SHUTDOWN_GRACE`.
- **versiond host FSM:** admission closes atomically on `draining`, leases span
  full streams, reconcile/restarts freeze, and first/second signals exercise
  graceful/idempotent/forced process states.
- **versiond-router:** test every router transition and guard, config validation,
  atomic rollback, interrupted-transaction recovery, and hostctl checkpoint
  resume/order without requiring SSH.
- **full stack (`devshard/testenv`, S10):** pin a long stream to one versiond,
  interrupt and cancel one checkpointed operation, then interrupt and resume a
  real `gonka-hostctl evacuate` from its durable phase before running `replace`.
  Verify new work and the same escrow recover on the survivor, the barrier-held
  stream finishes on its old connection, and the replacement remains down
  until healthy.

### 1.10 Rollout order

**Track A — devshardd binary swap (§1.1–§1.6, required for governance updates):**

1. devshardd: `/ready`, `/drain/status`, configurable shutdown grace.
2. versiond: config flags (no behavior change yet).
3. versiond: per-name two-child model + swap-aware ports + readiness probe.
4. versiond: rewrite `downloadAndSwap` to blue/green + drain.
5. Surface draining state in `/healthz`; process lifecycle FSM for graceful
   stop, forced escalation, and confirmed reap.
6. Tests (§1.9), then enable by default.

**Track B — versiond host removal/replacement (§1.8, HA only):**

7. Add versiond host FSM, aggregate health/inflight, transactional router FSM,
   and resumable SSH operator CLI for drain, stop, replacement, and activation.
8. Run unit/race coverage plus S10: pin a long request to `versiond-N`, mark the
   upstream down, assert completion and survivor routing, then assert process
   exit after idle and healthy replacement activation.

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
