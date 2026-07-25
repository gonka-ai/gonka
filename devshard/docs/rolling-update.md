# Rolling updates: versiond + devshardd

Operator requirement:

> Roll out a **new binary under the same version name** (name stays, e.g.
> `v0.2.13`, only the `sha256` changes) such that:
> 1. every request already accepted by the previous instance is allowed to
>    finish — we do **not** kill an instance while it is still processing;
> 2. we stop/kill the old instance **only after** the new one is ready and
>    reachable;
> 3. once the new instance is reachable we route **new** requests to it, while
>    the old instance keeps **draining** its in-flight requests.

This document describes:

1. **Part 1 — versiond (implemented).** Blue/green + drain for governance
   binary swaps inside `versioned/` + `devshardd` (Track A), plus a separate
   **`versiond-router` host-evacuation** track for HA (§1.7–§1.8, Track B —
   not shipped).
2. **Part 2 — Kubernetes (sketch).** How the same guarantees map onto a future
   K8s deployment.

Related: [release-0.2.14-v4.md](./release-0.2.14-v4.md),
[v4-deploy-test-plan.md](./v4-deploy-test-plan.md) §7,
[testenv/docs/scenarios.md](../testenv/docs/scenarios.md).

---

## 0. Historical baseline (pre-rollout)

Before Track A, same-name SHA changes were **stop-then-start on one port**:

- Route table was `map[versionName]host:port` (string addresses only).
- `downloadAndSwap` downloaded the new binary, `SIGTERM`ed the old child
  (`WaitDelay` ~5s), then started the new child on the **same** port.
- “Ready” meant TCP-accept only (`waitForPort`), not HTTP readiness.
- `devshardd` shutdown grace was a hard-coded ~5s `server.Shutdown`.

That could not keep old in-flight work (long SSE / validation) alive while a
proven-ready new child took new traffic. The sections below describe the
**current** implementation that closes those gaps.

---

## Part 1 — versiond rolling update (current behavior)

### 1.1 Flow summary

```
poll detects same name, new sha256
        │
        ▼
download into bin/<name>/<sha>/          ← old child binary untouched on disk
        │
        ▼
probe --print-storage-mode (old recorded + new binary)
        │
        ├── not both exactly postgres → exclusive stop/start (no overlap)
        │
        ▼
start NEW child on a NEW port            ← old keeps serving
        │
        ▼
wait NEW admin /ready == 200 and public /healthz == 2xx
        │                               (VERSIOND_READY_TIMEOUT; abort keeps old)
        ▼
atomic route swap → NEW Target           ← retire OLD Target
        │
        ▼
wait OLD proxy leases == 0  OR  VERSIOND_DRAIN_TIMEOUT
        │
        ▼
POST OLD /drain; poll /drain/status until inflight == 0
        │                               (or deadline)
        ▼
SIGTERM OLD → process FSM grace → SIGKILL backstop → reap
```

The proxy route value is a generation-specific `proxy.Target`, not just an
address. Each forwarded request `acquire`s a lease and `release`s it after the
full response (including SSE). A stale lookup that loses the race with
retirement retries against the new route; an already acquired request stays on
the old generation until it completes.

```52:85:versioned/internal/proxy/proxy.go
func Handler(routes *atomic.Value, opts ...HandlerOption) http.Handler {
	// ...
		target, ok := acquireTarget(routes, version)
		if !ok {
			http.Error(w, fmt.Sprintf("version %q not found", version), http.StatusNotFound)
			return
		}
		defer target.release()
		reverseProxy(target.Address(), rest).ServeHTTP(w, r)
}
```

(Versionless observability paths in the same handler are orthogonal to rolling
swap; see `isVersionlessObsPath` / `WithSessionVersionLookup`.)

### 1.2 Storage prerequisite

Rolling update means old + new `devshardd` may run **concurrently**. That is
safe only with **Postgres-only** storage. SQLite and hybrid can write under the
child data dir; overlapping children would race local files / migrate paths.

- **Postgres-only** (`DEVSHARD_STORAGE_MODE=postgres` + `PGHOST` / `PG*`):
  shared external state — only mode that permits blue/green overlap.
- **SQLite / hybrid / auto→hybrid**: exclusive stop/start only.

versiond does **not** reimplement storage resolution and does **not** probe
`PGHOST` or local SQLite `_meta.db` for the overlap gate. It asks the binaries:

- running child records `--print-storage-mode` at preflight;
- incoming binary is probed with `--print-storage-mode` before overlap;
- overlap is allowed only when **both** answers are exactly `postgres`.

Anything else (legacy binary without the flag, probe error, `hybrid`,
`sqlite`, unknown) fails closed to stop/start. See
[storage-design.md](./storage-design.md) and
[release-0.2.14-v4.md](./release-0.2.14-v4.md).

### 1.3 devshardd lifecycle (admin listener)

Public traffic listener keeps `/healthz` (liveness) and inference routes.
Lifecycle controls live on a **loopback admin** listener when versiond sets
`DEVSHARD_ADMIN_ADDR=127.0.0.1:<port>` (after `--print-admin-api-version`
succeeds). Those paths are **not** registered on the public Echo instance.

The lifecycle controller is a table-driven FSM:

```text
starting --chain ready--> serving --chain disconnected--> disconnected
                              ^                              |
                              +--------- chain ready --------+

starting / serving / disconnected --drain--> draining
draining --any later lifecycle event------------> draining
```

The state table owns readiness, draining, and request-admission projections.
`starting` and `disconnected` report `ready=false` but preserve the existing
admission behavior during chain subscription startup/reconnect. Only
`draining` closes admission. The transition to `draining` and request
admission/inflight accounting use the same lock, so a racing request is either
accepted and counted for its full response or rejected before its handler runs.
Late chain-ready callbacks cannot move a draining process back to serving.

1. **`GET /ready` (admin)** — `200` when chain-event subscriptions report ready
   and the child is not draining; otherwise `503`. versiond also requires
   public `/healthz` `2xx` before publishing the route for admin-capable
   children (`waitForChildServingReady`).
2. **`GET /drain/status` (admin)** — `{ready, draining, inflight}` from the
   lifecycle middleware (all non-lifecycle HTTP, including full SSE duration).
   Same count as Prometheus `devshardd_lifecycle_inflight_requests`.
3. **`POST /drain` (admin)** — reject new non-lifecycle work; finish accepted
   requests. Belt-and-braces after the proxy has already retired the old target.
4. **`DEVSHARD_SHUTDOWN_GRACE`** (default `10m`) — `server.Shutdown` budget on
   `SIGTERM` (public + admin servers).

```387:391:devshard/cmd/devshardd/app.go
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), a.shutdownGrace)
	defer shutdownCancel()
	_ = a.server.Shutdown(shutdownCtx)
	if a.adminServer != nil {
		_ = a.adminServer.Shutdown(shutdownCtx)
	}
```

### 1.4 versiond supervisor

#### a) Serving + draining children

`Manager` keeps:

- `processes map[string]*child` — current **serving** child per version name;
- `draining map[string][]*child` — retired generations finishing work.

`rebuildRoutes` publishes only `statusRunning` children as `proxy.RouteTable`
(`map[string]*Target`), then retires replaced targets:

```1642:1658:versioned/internal/process/manager.go
func (m *Manager) rebuildRoutes() {
	previous := m.routes.Load().(proxy.RouteTable)
	routes := make(proxy.RouteTable)
	for _, c := range m.processes {
		if c.status == statusRunning {
			if c.proxyTarget == nil {
				c.proxyTarget = proxy.NewTarget(fmt.Sprintf("localhost:%d", c.port))
			}
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

`/healthz` lists both serving and draining entries (`status`, `sha256`,
`binary_version`).

#### b) Port pool

Each child gets a port from a bounded pool (`BasePort`…65535). Ports are reused
after exit; versiond’s own listen port (`:8080`) is reserved. Exhaustion is an
explicit start error — a rolling candidate that cannot allocate a port leaves
the current child serving.

```138:154:versioned/internal/process/manager.go
func (m *Manager) assignPort() (int, error) {
	for port := m.cfg.BasePort; port <= maxChildPort; port++ {
		// skip allocated + reserved
		...
		return port, nil
	}
	return 0, fmt.Errorf("%w in range %d-%d", errChildPortPoolExhausted, ...)
}
```

Admin-capable children also allocate a second loopback admin port.

#### c) Readiness gate

`waitForChildServingReady` replaces TCP-only accept:

- **Admin-capable:** admin `VERSIOND_READY_PATH` must return `200` **and**
  public `/healthz` must return `2xx`, within `VERSIOND_READY_TIMEOUT`.
- **Legacy (no admin API):** readiness probe on the public port with the
  documented `/ready` → `/healthz` → TCP fallback for the default path only.

Failure aborts the swap: new child is stopped; old keeps serving; next reconcile
retries.

#### d) `downloadAndSwap` (blue/green + drain)

```724:799:versioned/internal/process/manager.go
func (m *Manager) downloadAndSwap(...) error {
	// downloadBinary
	if !m.rollingOverlapAllowed(...) {
		// exclusive: Stop old → wait → startChild new
		return nil
	}
	// newChild on fresh port → waitForChildReady
	// move old → draining; processes[name]=new; rebuildRoutes; retire old Target
	go m.drainAfterProxy(old, proxyDrained)
}
```

`drainAfterProxy` waits for proxy leases (same `VERSIOND_DRAIN_TIMEOUT`
deadline), then `POST /drain` and polls `/drain/status` until `inflight == 0`
(or deadline / legacy no-status cushion), then `Stop()` + reap + `releasePort`.

Invariants:

- New ready before traffic (admin `/ready` + public `/healthz`).
- New requests use the new `Target` after publish.
- No boundary gap: acquire lease or retry after retirement.
- Old finishes in-flight: proxy leases then lifecycle inflight before SIGTERM.
- Don’t kill until idle (or drain timeout / legacy cushion).

#### e) Removed versions

Route removal is immediate (`404` for new requests). The child moves to
`draining` and drains asynchronously. Restoring the same name while draining
**defers** the new start until the old generation exits (avoids two generations
sharing one data dir).

#### f) Process FSM + supervisor shutdown

Each OS process is owned by `supervisedProcess`:

```text
Running → Terminating → Killing → Exited
```

The supervisor is an event-driven, table-defined FSM. Each
`(state, event)` entry selects both the next state and one action:
`SIGTERM`, `SIGKILL`, or completion after `cmd.Wait`. Events are stop request,
force request, grace expiry, and process exit. Only the `cmd.Wait` result emits
the exit event, so no signal path can close `Done()` before the process is
reaped.

`Stop()` sends `SIGTERM` to the process group and arms grace;
expiry / `ForceStop()` escalate to `SIGKILL`. `Done()` closes only after
`cmd.Wait()` reaps. Grace is `VERSIOND_DRAIN_KILL_GRACE` for non-devshard
binaries, and `max(VERSIOND_DRAIN_KILL_GRACE, DEVSHARD_SHUTDOWN_GRACE)` for
devshardd.

`Manager.Shutdown` stops all current + draining children first, then waits for
every `Done()`. If the shutdown context expires it `ForceStop`s remaining
children but still waits for reap.

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

### 1.5 Configuration

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
| `VERSIOND_DRAIN_KILL_GRACE` | `10m` | legacy no-status cushion; exact non-devshard stop grace and lower bound for devshardd |
| `DEVSHARD_SHUTDOWN_GRACE` | `10m` | `devshardd` HTTP shutdown budget after `SIGTERM` |
| `VERSIOND_HOST_SHUTDOWN_BUDGET` | `25m` | one absolute deadline for host admission drain, graceful child stop, and HTTP shutdown; expiry forces remaining work before reap |

versiond sets `DEVSHARD_ADMIN_ADDR` per child when `--print-admin-api-version`
is supported. Operators normally do not set it by hand.

**Install layout:** per-sha dirs `bin/<name>/<sha>/` with `install.json` commit
marker; GC keeps desired/live/draining plus a small recent cushion; incomplete
downloads are never GC’d. Legacy flat installs are promoted atomically into the
per-sha layout when possible.

**Legacy compatibility:** unsupported `--print-*` flags fall back only on
recognized usage errors (fail-closed on timeouts/signals). Storage mode unknown
⇒ no overlap. Default `/ready` missing ⇒ `/healthz` then TCP. Missing
`/drain/status` ⇒ `VERSIOND_DRAIN_KILL_GRACE` cushion instead of full drain
timeout. Oracle names must be safe unique path components; sha256 must be 64 hex
chars — invalid oracle responses fail the poll and leave running children alone.


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

Bootstrap environment is read only when no persistent router state exists.
On restart, pending recovery and the stored state take precedence, so a missing
or malformed `VERSIOND_HOSTS` cannot block startup or overwrite the live pool.

The persisted router FSM is:

```text
add -> joining -> active -> draining -> stopping -> offline
                 ^          |
                 +-- cancel-+

offline -> joining -> active     replacement
offline -> removed               terminal decommission
```

Only `active` hosts are live nginx upstreams. Other states are rendered with
the nginx `down` parameter. A successful `nginx -s reload` stops new sticky
assignments without terminating connections held by old nginx workers.
Transitions are selected from an explicit `(from, to)` handler table. A durable
transfer binds its intermediate edges to one operation ID, immutable membership
ID, host, and final target. `removed` is not stored: the membership disappears,
and a later add with the same name creates a new membership ID.

#### Target flow (evacuate one versiond host)

Applies when taking `versiond-N` out of service: container replace, supervisor
upgrade, scale-down, or decommission.

```text
1. Mark versiond-N down in router upstream (reload nginx config)
        │  → no NEW requests hashed to versiond-N
        │  → in-flight connections to versiond-N keep running
        ▼
2. Persist signal intent and move the membership from draining to stopping. The
   edge verifies the durable transfer owner and traffic barrier before starting
   a managed stop:
        disable automatic restart
        SIGTERM versiond
        ▼
3. versiond owns graceful shutdown under one absolute deadline:
        close proxy admission
        wait for accepted proxy leases (including complete SSE streams)
        drain and gracefully stop children, stop HTTP
        on VERSIOND_HOST_SHUTDOWN_BUDGET expiry, force remaining work
        reap children before process exit
        ▼
4. hostctl waits for process exit:
        wait up to ROUTER_DRAIN_KILL_GRACE  →  SIGKILL only as backstop
        ▼
5. Choose the durable outcome:
        replacement → keep the logical host offline
        decommission → remove it from router state and reload nginx
        then update the deployment's VERSIOND_HOSTS bootstrap source
        ▼
6. Replacement/addition: enter joining, start versiond-N,
        wait for GET /ready, then activate the upstream
```

Key invariants:

- **Stop new traffic first:** router marks the upstream `down`
  before any `SIGTERM` to versiond. An established HTTP/SSE request cannot be
  moved and must finish on its original host. A later request for the same
  escrow may be re-hashed to a survivor and recover from shared Postgres;
  affinity is placement, not exclusive in-memory ownership. Non-HA versions
  remain pinned to the legacy host and are protected by the router guard.
- **One owner of drain state:** hostctl never infers idleness from `/healthz`.
  versiond owns the admission leases and child lifecycle counters, so its host
  FSM decides when graceful drain is complete. Its table defines both allowed
  state transitions and whether a state accepts new proxy leases, with
  `serving` as the sole accepting state. If the internal budget expires,
  versiond logs the remaining work and forces teardown before the outer
  runtime backstop.
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
| `gonka-routerctl` | Local, locked table-driven router FSM with membership IDs, durable transfer ownership, desired/applied generations, forward-only reconciliation, receipt index, WAL, `nginx -t`, atomic publish, reload, and audit outbox |
| `gonka-hostctl` | Table-driven, journaled SSH workflows for add, evacuation, decommission, replacement, and cancellation; no network listener |
| `GET /healthz` | Compatibility health response; it is not an evacuation control-plane API |
| `GET http://127.0.0.1:8081/ready` | Loopback-only replacement admission gate; `200` only for a serving, accepting, available, fully reconciled host; public `:8080/ready` returns `404` |
| `VERSIOND_HOST_SHUTDOWN_BUDGET` | One internal deadline for graceful versiond shutdown before forced escalation, default `25m` |
| `ROUTER_READY_TIMEOUT` | External maximum wait for replacement/addition readiness, default `15m` |
| `ROUTER_DRAIN_POLL_INTERVAL` | External readiness/process polling interval, default `2s` |
| `ROUTER_DRAIN_KILL_GRACE` | Wait after `SIGTERM` before `SIGKILL`, default `30m` |
| `ROUTER_COMMAND_TIMEOUT` | Deadline for one local or SSH command, default `30s` |

`ROUTER_DRAIN_KILL_GRACE` must exceed versiond's internal shutdown budget. Its
default leaves five minutes between versiond's `25m` deadline and the external
`30m` kill backstop. Admission drain, child drain, graceful child stop, and HTTP
shutdown share the same absolute deadline; phase-local limits can only shorten
a phase and are never added to the host budget. After expiry, versiond forces
remaining processes and confirms their reap during the outer reserve.

Proxy leases still count complete versiond responses, including full SSE stream
lifetimes, and child lifecycle counters still come from private child admin
endpoints. Both signals are consumed inside versiond's shutdown state machine;
they are deliberately not exported through a special `/healthz` response.
Legacy `/healthz` clients therefore keep the existing JSON array contract.

On `SIGTERM`, versiond transitions
`serving -> draining -> stopping -> stopped`. Admission closes immediately,
reconciliation and crash restarts freeze, accepted proxy requests finish,
children receive the existing `/drain` and idle sequence, and only then receive
`SIGTERM`. Repeated `SIGTERM` is idempotent so orchestration retries cannot force
the host accidentally; a second `SIGINT` is the explicit interactive transition
to `forcing`.

Active reconcile work is registered as a cancellable control operation. Host
drain cancels that registry before waiting for the poll worker. Poll unwind may
consume at most ten percent of the host shutdown budget and is capped at five
seconds; if a worker ignores cancellation, versiond logs it and proceeds to
child drain or forcing. Child preflight, downloads, readiness, and stop/start
waits inherit the operation context, so a non-overlap swap cannot make force
handling unavailable. Child generations use the typed lifecycle `preparing ->
starting -> running -> retiring -> draining -> stopping -> stopped`, with
`failed -> starting` for supervised retry.

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
Every stop-side action therefore re-observes the runtime as `running`,
`stopped`, or `absent`. Before the first router drain, absence is rejected to
catch an incorrect `--versiond-service`; an intentionally removed runtime
requires `--allow-absent-runtime`. Once `draining` is durable, a stopped or
absent service needs no signal and the workflow continues toward the router
target. Docker absence must name the configured service in an explicit
missing-object response; systemd absence requires `LoadState=not-found`.
Other probe failures remain fail-closed.
For systemd, use `--router-runtime systemd --versiond-runtime systemd`; the
orchestrator uses `systemctl stop --no-block` so `Restart=` cannot resurrect the
unit. Before changing router state for a running service, hostctl validates the
runtime shutdown contract. systemd's `TimeoutStopSec` directly bounds the
managed stop job and must cover `ROUTER_DRAIN_KILL_GRACE`; it must use
`KillMode=mixed` with `SendSIGKILL=yes`. The initial systemd `SIGTERM` then
reaches versiond alone, while the final timeout still covers its complete
cgroup. Hostctl signals Docker directly, while its
required `StopTimeout` protects external `docker stop`, Compose teardown, and
redeploy from undercutting the same budget. Compose deployments, including the
local test network, set `stop_grace_period: 30m` for versiond and
versiond-router. Rerun an interrupted command with the same operation ID and
journal path to continue after its last durable phase.

A direct `SIGTERM` that bypasses router drain closes versiond admission and may
return `503` to a new request already selected by nginx. The router deliberately
does not retry an already-sent inference `POST`: automatic replay could execute
the request twice. Operators must use `gonka-hostctl` for planned maintenance.

Before `SIGTERM`, the `draining -> stopping` edge revalidates the membership,
transfer owner, and final target. Each hostctl operation uses a table of
`current -> next + handler` transitions and checkpoints `next` only after the
handler succeeds. Replay can therefore safely repeat the one edge whose remote
mutation completed before the local journal advanced. If evacuation must be
abandoned before the durable `term_requested` phase, run `gonka-hostctl cancel`
with the same operation ID and scope. A second command never waits behind the
active operation lock: it reports the owner action, PID, and journal phase.
Interrupt a still-running pre-signal `evacuate` process before invoking
`cancel`.
Cancellation is a durable compensation FSM: it records the intent, restores any
disabled Docker restart policy, checkpoints that action, and then reactivates
the upstream. A failed cancellation is resumed with `cancel` while versiond
remains running. If the process has already stopped before router reactivation,
the original evacuation or decommission command reasserts `restart=no`,
atomically abandons the impossible compensation, and resumes forward to
`offline` or `removed`. `term_requested` is persisted before the SSH signal
command; at or after it, the original operation must be resumed because the
remote outcome can be unknown.

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
    ~/.local/state/gonka/hostctl/maintenance-20260718-versiond2.json
```

The upstream remains `joining`/down until the loopback-only
`GET http://127.0.0.1:8081/ready` returns `200`. That gate means versiond is
serving, accepting, has an available child, and is fully reconciled without a
progressing or degraded condition. Router desired state,
applied-generation metadata, completion receipt index, pending WAL, audit
outbox, and audit log are stored below `/var/lib/gonka/versiond-router` on a
persistent volume. The audit may be rotated; state, applied metadata, receipts,
outbox, and journal are control-plane data and must remain together. See
`versiond-router/README.md` and
`versiond-host-evacuation.md` for the complete failure and recovery contract.
The readiness gate is deliberately strict: failure to reconcile any approved
version keeps the host out of the pool even if another version is already
serving.

Docker replacement restores the exact policy captured by evacuation, including
an `on-failure` retry count. A newly provisioned service without that journal
must pass `--docker-restart-policy` explicitly. Policy resolution happens before
the host enters `joining`.

For permanent scale-down, `gonka-hostctl decommission` continues past
`offline` and transactionally removes the host from the router pool. Removal is
idempotent, never leaves an empty configured pool, and requires an active
replacement owner when deleting the legacy host. For scale-up,
`gonka-hostctl add` records a new host as `joining`, starts the provisioned
service with an explicit Docker restart policy, waits for `/ready`, and then
activates it. Add or remove the host in the deployment's `VERSIOND_HOSTS`
bootstrap source after the runtime transaction; persistent state remains the
live authority, while that source remains necessary for disaster recovery.

`evacuate` and `decommission` may also adopt a membership already left
`offline`. Before changing runtime policy or the local journal, hostctl rejects
an `active_transfer` owned by another operation and classifies the configured
runtime as running, stopped, or absent. A present runtime must be stopped and a
present Docker container is pinned to `restart=no`; an already removed
container or systemd unit is accepted with a warning. The observed
`router_offline` state is then checkpointed: evacuation completes, while
decommission proceeds directly to `removed`.

`gonka-routerctl status` never performs recovery or reload. It exposes an
unfinished journal as `pending_operation` and reports desired/applied
generations, render revision/source SHA, and `application.converged`;
`gonka-routerctl recover` resolves it under the controller lock. The WAL write
is the desired-state commit point. Recovery republishes the committed config
projection and converges forward through `nginx -t`, reload, and
applied-generation persistence. Before application, a repaired template, proxy
policy, or renderer schema creates a new projection revision in the same WAL,
so recovery does not require deleting committed state. Audit delivery is
retried from the outbox and cannot roll back an applied routing generation. If
both outbox persistence and direct audit append fail, routing remains committed
but the audit event may be lost with a warning.

The devshardd lifecycle endpoints (`/ready`, `/drain/status`, and `/drain`)
remain a private admin API for versiond-managed children. Host evacuation uses
versiond's own shutdown state machine and does not expose those child controls
through the public router.

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

### 1.9 Test coverage

- **Unit (`versioned/internal/process`):** readiness abort keeps the old child
  serving; drain waits for proxy leases and lifecycle inflight; timeout,
  asynchronous removal, deferred re-add, Postgres-only overlap, port-pool
  exhaustion, process escalation, reap, and forced manager shutdown are covered.
- **Unit (`versioned/internal/proxy`):** retired-target acquisition retries,
  while a request that already owns a target lease remains on that target
  through the route swap until its response or SSE stream completes.
- **e2e (`versioned/e2e`):**
  `TestSameNameNewSHA_RollingUpdateDrainsOld` verifies that a long request
  completes on the old binary while concurrent work reaches the new binary.
- **devshardd:** lifecycle tests cover the complete state/event matrix,
  terminal drain, atomic admission/inflight accounting, `/ready`, `/drain`,
  `/drain/status`, `devshardd_lifecycle_inflight_requests`, and
  `DEVSHARD_SHUTDOWN_GRACE`.
- **versiond host FSM:** admission closes atomically on `draining`, leases span
  full streams, reconcile/restarts freeze, and first/second signals exercise
  graceful/idempotent/forced process states.
- **versiond-router:** test every router transition and guard, config validation,
  committed-intent recovery, desired/applied convergence, and hostctl checkpoint
  resume/order without requiring SSH. Removal must drop the DNS name from the
  rendered pool after recovery from a reload failure and replay idempotently
  from the terminal operation receipt. Re-adding the same host name must create
  a new membership ID. State and pending journal fixtures from schemas 1
  through 4 must migrate without losing an in-progress transfer. A rejected
  config must recover through a new projection revision after its render
  source is fixed, while an already applied operation remains immutable.
- **full stack (`devshard/testenv`):**
  `TestVersiondRollingUpdateSameVersionSHA` covers Postgres overlap and SSE
  continuity, and `TestVersiondRollingUpdateHybridFallback` covers the
  non-overlap fallback. `TestVersiondHostEvacuation` pins a long stream to one
  host, verifies survivor routing and graceful exit, replaces the host behind
  the `/ready` gate, then decommissions and adds it back with a new membership.
- **testermint:** `VersiondTests` same-version binary update drains old requests
  and keeps serving.

Manual operator walkthrough: [v4-deploy-test-plan.md](./v4-deploy-test-plan.md) §7.


### 1.10 Status

**Track A — devshardd binary swap (§1.1–§1.6): implemented** on the v4 /
rolling-update line. Overlap is enabled automatically when both children report
`postgres` via `--print-storage-mode`; otherwise versiond uses exclusive
stop/start. No feature flag.

**Track B — versiond host removal/replacement (§1.8): implemented** on the
host-evacuation line:

7. The versiond host FSM has an internal absolute shutdown budget,
   transactional router FSM, replacement `/ready` gate, and resumable SSH
   operator CLI for drain, stop, replacement, and activation.
8. Unit/race coverage and `TestVersiondHostEvacuation` pin a long request
   to `versiond-N`, mark the upstream down, assert completion and survivor
   routing, then assert process exit after idle and healthy replacement
   activation.

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
- The drain/idle accounting (`devshardd_lifecycle_inflight_requests` via
  `/drain/status`) feeds the versiond binary-swap drain loop (§1.1), the
  versiond-router host-evacuation loop (§1.8), and K8s preStop logic / dashboards.
  (`devshard_inflight` remains a separate stage-ops gauge.)
- §1.1 (binary swap inside a live versiond) and §1.8 / this section (whole
  pod/host removal) remain separate: K8s `RollingUpdate` handles pod lifecycle;
  versiond `downloadAndSwap` handles governance sha changes without pod restart.

### 2.6 Not in scope here

- Helm/manifests, HPA, PodDisruptionBudget, mesh config — to be detailed when
  the K8s track is picked up. This section only fixes the rollout **semantics**
  so they match the versiond behavior.
