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

`Stop()` sends `SIGTERM` to the process group and arms grace;
expiry / `ForceStop()` escalate to `SIGKILL`. `Done()` closes only after
`cmd.Wait()` reaps. Grace is `VERSIOND_DRAIN_KILL_GRACE` for non-devshard
binaries, and `max(VERSIOND_DRAIN_KILL_GRACE, DEVSHARD_SHUTDOWN_GRACE)` for
devshardd.

`Manager.Shutdown` stops all current + draining children first, then waits for
every `Done()`. If the shutdown context expires it `ForceStop`s remaining
children but still waits for reap.

### 1.5 Configuration

| Env var | Default | Meaning |
|---|---|---|
| `VERSIOND_READY_PATH` | `/ready` | Admin readiness path; public `/healthz` must also pass for admin children |
| `VERSIOND_READY_TIMEOUT` | `60s` | Max wait for incoming child before aborting swap |
| `VERSIOND_DRAIN_PATH` | `/drain` | POST path to put old child into drain mode |
| `VERSIOND_DRAIN_STATUS_PATH` | `/drain/status` | Poll path for lifecycle inflight |
| `VERSIOND_DRAIN_TIMEOUT` | `15m` | Shared deadline for proxy leases + child inflight before SIGTERM |
| `VERSIOND_DRAIN_POLL_INTERVAL` | `1s` | Inflight poll cadence |
| `VERSIOND_DRAIN_KILL_GRACE` | `10m` | Legacy no-status cushion; SIGTERM→SIGKILL backstop (and min for non-devshard) |
| `DEVSHARD_SHUTDOWN_GRACE` | `10m` | `devshardd` HTTP shutdown budget after SIGTERM |

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

Re-use versiond `/healthz` draining visibility from §1.4a for public host
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

### 1.9 Test coverage

- **Unit (`versioned/internal/process`):** swap readiness abort keeps old serving;
  drain waits for proxy leases then lifecycle inflight; drain timeout; async
  version removal; re-add deferred while draining; postgres-only overlap gate;
  port pool exhaustion; process FSM graceful stop / SIGKILL escalation / reap;
  manager shutdown forces then waits for Done.
- **Unit (`versioned/internal/proxy`):** retired-route acquire retries; acquired
  request stays on retired target across route swap until release.
- **e2e (`versioned/e2e`):** `TestSameNameNewSHA_RollingUpdateDrainsOld` — long
  request completes on old binary while concurrent work hits the new one.
- **testenv:** `TestVersiondRollingUpdateSameVersionSHA` (Postgres overlap + SSE
  continuity) and `TestVersiondRollingUpdateHybridFallback` (no overlap).
  Target: `make -C devshard/testenv citest-versiond-rolling-update` (see
  [testenv/docs/scenarios.md](../testenv/docs/scenarios.md)).
- **testermint:** `VersiondTests` same-version binary update drains old requests
  and keeps serving.
- **devshardd:** lifecycle tests for `/ready`, `/drain` / `/drain/status`, and
  `devshardd_lifecycle_inflight_requests`.

Manual operator walkthrough: [v4-deploy-test-plan.md](./v4-deploy-test-plan.md) §7.


### 1.10 Status

**Track A — devshardd binary swap (§1.1–§1.6): implemented** on the v4 /
rolling-update line. Overlap is enabled automatically when both children report
`postgres` via `--print-storage-mode`; otherwise versiond uses exclusive
stop/start. No feature flag.

**Track B — versiond host removal/replacement (§1.8): not implemented** —
operator / future router automation. See also draft host-evacuation work
(e.g. PR discussion for whole-`versiond` evacuation).


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
