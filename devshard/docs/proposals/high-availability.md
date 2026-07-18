# Proposal: High-Availability Architecture

**Status:** Draft / proposal
**Scope:** Make every node-side service horizontally scalable and rolling-update
safe, with no single point of failure, and deliver a **Kubernetes-ready**
deployment packaged as a **Helm chart**.

This proposal builds on the current architecture
([../high-availability-architecture.md](../high-availability-architecture.md)) and the binary-rollout design
([../rolling-update.md](../rolling-update.md)). It does **not** change the
inference-chain.

---

## 1. Goals

1. **No single point of failure.** Every node-side service runs ≥2 instances,
   ideally across machines.
2. **Rolling updates** with zero dropped in-flight work (see
   [../rolling-update.md](../rolling-update.md)).
3. **Scale the read/serve path** (chain queries, inference fan-out, PoC
   callbacks) horizontally.
4. **Keep exactly-once chain effects.** Even with many instances, each chain
   transaction and each block-driven action happens **once**.
5. **Kubernetes-ready deployment via Helm.** The HA decomposition above is the
   prerequisite for packaging the node stack as a **Helm chart**: one chart (or
   subcharts) per service, externalized shared dependencies (Postgres, NATS,
   Redis), and native K8s primitives for scaling, health, drain, and rolling
   updates (see §9).

### What already meets the bar

| Service | HA today |
|---------|----------|
| `proxy` | Immutable; run N behind a VIP/L4 LB |
| `edge-api` | Stateless; N instances + `edge-api-router` (round-robin) |
| `versiond` + `devshardd` | N instances on separate machines + `versiond-router` (sticky hash) **on shared Postgres** |

### What blocks HA today

`decentralized-api` (dapi) is a single monolithic process: its **chain event
listener / phase engine has no leader election**, it embeds a **per-process
NATS**, holds a **local keyring** for signing, and queries the **chain
directly**. Two dapi instances would duplicate transactions and ML commands
(see [../high-availability-architecture.md](../high-availability-architecture.md) §5). This proposal restructures
dapi so it can be made highly available.

---

## 2. Target architecture (overview)

```text
                          ┌──────────────────────────────┐
            clients ────▶ │   proxy (N, behind L4 LB)     │
                          └───────────────┬───────────────┘
        ┌──────────────────────┬──────────┴───────────┬───────────────────────┐
        ▼                      ▼                      ▼                        ▼
 ┌─────────────┐      ┌────────────────┐     ┌────────────────┐      ┌────────────────┐
 │  edge-api    │      │ dapi: edge-srv │     │ dapi: node-mgr │      │ versiond[-router]│
 │  (N, HA)     │      │ (N, PoC/admin) │     │ (broker/PoC)   │      │  → devshardd    │
 │  HA chain    │      │  REST callbacks│     │  + leader      │      │  (shared PG)    │
 │  proxy+cache │      └───────┬────────┘     └───────┬────────┘      └────────────────┘
 │  + event hub │              │                      │
 └──────┬───────┘              │ publish chain msgs   │ publish chain msgs
        │ events (pub/sub)     ▼                      ▼
        │              ┌──────────────────────────────────────┐
        ├─────────────▶│        NATS (standalone, HA)          │  ◀── one queue for
        │              │  subjects: chain.tx, events.*, ...    │      all chain-bound msgs
        │              └───────────────────┬──────────────────┘
        │                                  ▼  (single consumer / msg)
        │                        ┌──────────────────────┐
        │                        │   signer service     │  signs with warm key,
        │                        │   (N, queue group)   │  sends tx to chain
        │                        └──────────┬───────────┘
        │                                   ▼
        ▼                              inference-chain
   Redis (shared edge-api state:        (gRPC + RPC)
   leader lock, event cursor, cache)
```

Two pillars:

- **A. edge-api becomes the highly-available chain access + event hub.** A
  **separate** edge-api tier gathers block events from the chain, **publishes**
  them to NATS (and other subscribers), and **caches** chain queries so node
  services do not each open their own gRPC/RPC subscriptions. The same surface
  can later be reused by **dashboards and monitoring** (read APIs + event stream)
  without coupling observability to dapi or devshardd. Redis backs edge-api's
  shared state and leader election.
- **B. dapi is decomposed into independently-scalable services** around a
  **standalone HA NATS** queue, a **stateless signer service**, **Postgres** as
  the only stateful backend, and stateless REST/echo workers.

---

## 3. Pillar A — edge-api as the HA event hub & chain cache

### Purpose of a separate edge-api

**edge-api** is split out as its own service so the node has one **chain-facing
tier** with two jobs:

1. **Block events** — subscribe to the inference-chain once (CometBFT
   `NewBlock` + per-tx events), normalize them, and **transmit** the stream to
   **NATS** and other subscribers (dapi node-manager, devshardd, PoC workers).
2. **Query cache** — serve and **cache** chain read APIs (participants, epochs,
   params, escrows, etc.) so every consumer does not dial gRPC/RPC independently.

That separation keeps dapi and devshardd focused on their domain logic while
edge-api owns **how** the node talks to the chain. The same HTTP/gRPC read
surface and event fan-out can be **reused later** by **dashboard and monitoring
systems** (status pages, ops tooling, external observers) without embedding
chain clients in each product binary.

Today edge-api is a stateless read-only proxy for 22 Tier A routes. We extend it
to be **the** chain-access layer for the whole node.

### 3.1 Move the event listener into edge-api

- Relocate the chain event listener that lives in dapi
  (`decentralized-api/internal/event_listener/`) into edge-api.
- edge-api subscribes to the chain (CometBFT WebSocket `NewBlock` + RPC
  `BlockResults` per-tx events) and **re-publishes** normalized events to
  **NATS** and other consumers (dapi services, devshardd) via pub/sub.
- Consumers (dapi node-manager, PoC services, devshardd) **subscribe to
  edge-api events** instead of opening their own chain subscriptions. This
  removes N independent chain subscriptions and centralizes block processing.

### 3.2 Leader election (only one instance triggers events)

edge-api scales to N instances, but **block-driven side effects must fire
once**. So:

- **Every instance stays in sync** (each can serve queries and hold a warm event
  cursor), but **only the elected leader** advances the canonical block cursor
  and **emits** the authoritative event stream.
- **Redis** holds the leader lock (e.g. `SET NX PX` lease with renewal) and the
  shared **event cursor** (`last_processed_height`) so a new leader resumes
  exactly where the old one stopped — no gaps, no replays.
- Emitted events are **propagated to all instances** (and downstream services)
  via pub/sub, so followers and consumers see the same stream the leader
  produced. On leader loss, another instance takes the lock within the lease TTL
  and continues from the Redis cursor.

### 3.3 Redis as edge-api shared state

| Redis use | Why |
|-----------|-----|
| Leader lock (lease + renew) | Single active event emitter |
| Event cursor (`last_processed_height`) | Gap-free failover |
| Chain query cache (optional) | Reduce duplicate chain gRPC load; TTL per route |
| Fan-out / pub-sub of events | Propagate the leader's event stream to all instances and subscribers |

> edge-api thus becomes a **highly-available proxy + cache for the
> inference-chain**, the **single source of chain events** for the node, and a
> stable integration point for future **dashboard / monitoring** consumers.

### 3.4 Consumers stop querying the chain directly

- **dapi no longer queries the inference-chain directly.** It uses HA edge-api
  for chain reads and subscribes to edge-api for events. This shrinks dapi to its
  unique responsibilities (below).
- **devshardd** likewise subscribes to edge-api events (escrow created/settled,
  new block/phase) rather than maintaining its own chain WebSocket, reducing
  per-child chain connections. (devshardd keeps its own gRPC tx path for
  disputes, or routes them through the signer queue — see §5.)

---

## 4. Pillar B — decentralized-api becomes single-purpose

After Pillar A, dapi sheds chain-query and event-subscription duties. Its
remaining unique responsibilities are:

- **Node manager** (broker: ML node lifecycle per epoch phase).
- **Admin panel** (admin REST: node CRUD, model registration, setup report,
  etc.).
- **PoC / cPoC** handler + scraper (artifact ingest, commit worker, off-chain
  validation, proof serving).

These become independently deployable, mostly-stateless services. The only
mutable backends are **Postgres** (shared) and the **NATS** queue.

### 4.1 Service decomposition

| New service | Responsibility | Scaling | State |
|-------------|----------------|---------|-------|
| **edge-srv (REST workers)** | Echo HTTP for PoC callbacks (`/v2/poc-batches/...`) and admin REST events | **Multi-instance** (immutable) | none (writes to Postgres / publishes to NATS) |
| **node-manager** | Broker reconciliation + phase engine reactions (PoC stage commands, validation sampling) | **Leader-elected** (single active driver) | Redis lock + Postgres |
| **signer** | Sign chain messages with the warm key and broadcast | **Multi-instance**, NATS queue group (one message consumed once) | warm keyring only |
| **PoC services** | Artifact store, commit worker, off-chain validation, proof client | Mixed (callbacks scale; commit driven by node-manager leader) | Postgres |

### 4.2 Standalone HA NATS queue

Replace the **embedded per-process NATS**
(`decentralized-api/internal/nats/server/server.go`) with a **standalone,
clustered NATS (JetStream)** shared by all instances.

- **Every chain-bound message is published to NATS** (subject e.g. `chain.tx`),
  carrying the message and metadata. Producers are any service that needs to
  write to chain (node-manager, PoC commit, edge-srv, devshardd disputes).
- The queue is the **single, durable, ordered-enough** path to the chain. It
  survives instance restarts and decouples producers from the signer.

### 4.3 Signer service (warm-key signing, exactly-once consume)

- The **signer** is a **NATS queue-group consumer** of `chain.tx`: with a queue
  group, **each message is delivered to exactly one signer instance**, so N
  signers share the load but never double-sign the same message.
- The signer holds the **warm key** (Cosmos keyring), wraps in `authz.MsgExec`
  with feegrant from the cold account where applicable (current model in
  `cosmosclient/tx_manager`), signs, and broadcasts to the chain.
- Broadcast/observe/retry state moves to the durable NATS streams
  (`txs_to_send` / `txs_to_observe` equivalents) so any signer instance can pick
  up retries. Idempotency keys (e.g. inference id + msg type) guard against
  duplicate submission across retries/failover.
- Because signing is isolated behind the queue, the **warm key lives only in the
  signer** — other services never need the keyring.

### 4.4 Postgres as the only HA data backend

- All mutable state that must survive instance loss lives in **Postgres**
  (payloads, stats, PoC artifacts/commits metadata, config/cursors as needed).
  Per-process SQLite KV (e.g. dapi `apiconfig` `last_processed_height`) moves to
  Postgres/Redis so any instance is interchangeable.
- This mirrors the devshardd rule: **multi-instance ⇒ Postgres**
  ([../high-availability-architecture.md](../high-availability-architecture.md) §4).
- Operational mode: `DEVSHARD_STORAGE_MODE=postgres` (resolved by
  `common/storage/mode`; default `auto` is hybrid-if-`PGHOST`-else-sqlite).
  (`VERSIOND_FORCE` does not imply postgres; multi-versiond compose must set
  the mode explicitly.) In postgres mode, payload and session stores are
  **Postgres-only**: boot fails if Postgres is unreachable (no SQLite/file
  fallback). Before serving traffic, boot fully migrates any local SQLite
  sessions (journal, sealed inferences, validation obs) and any on-disk file
  payloads into Postgres, then quarantines the local artifacts. See
  [../storage-design.md](../storage-design.md)#storage-mode-selection.

### 4.5 Stateless echo workers (PoC callbacks + admin)

- The Echo HTTP layer for **PoC callbacks** and **admin REST** is **immutable**:
  it only reads/writes Postgres or publishes to NATS. Therefore it can run
  **N instances** behind the proxy with no coordination.
- The only operations needing single-execution semantics (phase-driven stage
  commands) belong to the **leader-elected node-manager**, not the echo workers.

---

## 5. Exactly-once & coordination summary

| Concern | Mechanism |
|---------|-----------|
| One event emitter | edge-api **leader election** (Redis lease) |
| Gap-free event resume | Redis **event cursor** |
| One phase-engine driver | node-manager **leader election** (Redis lease) |
| Chain writes load-shared but once | NATS **queue group** → single signer per message + idempotency keys |
| Duplicate validation across devshardd | Postgres **validation leases** (existing; [../high-availability-architecture.md](../high-availability-architecture.md) §4) |
| Sticky devshard sessions | `versiond-router` consistent hash (existing) |
| Shared mutable state | **Postgres** (+ Redis for locks/cursors/cache) |

---

## 6. Rolling updates

Rolling updates apply per service and reuse the design in
[../rolling-update.md](../rolling-update.md). The HA stack depends on the same
**drain semantics** at two layers: binary swap inside a live supervisor, and
whole-host evacuation behind the sticky router.

### Rolling-update concepts (summary)

The [rolling-update plan](../rolling-update.md) defines how we roll out **new
binaries without dropping in-flight work**. Three operator guarantees:

1. Requests already accepted by an old instance may **finish** — we do not kill
   while work is still running.
2. A **new** instance must be **ready** before it receives traffic.
3. After the new instance is reachable, **new** requests go to it; the old
   instance **drains** until idle, then exits.

**Blue/green + drain inside `versiond` (Part 1 §1.1).** When governance publishes
a **same version name, new `sha256`** binary, `versiond` downloads the new
`devshardd`, starts it on a **new port** while the old child keeps serving,
waits for **`GET /ready`** (not just TCP accept), atomically swaps the in-process
route table so new requests hit the new child, marks the old child **draining**
(out of the route table but still alive), polls **in-flight count** until zero
(or a drain timeout), then `SIGTERM` with a **long shutdown grace**. Old and new
can overlap only when durable state lives in **shared Postgres** — SQLite is
single-writer and cannot support concurrent children (Part 1 §1.2).

**Two drain layers — do not conflate (Part 1 §1.7–§1.8).**

| Event | Layer | Router involved? |
|-------|--------|------------------|
| Same name, new **sha256** (governance binary update) | **versiond** blue/green + devshardd child drain | **No** — `versiond-router` upstream unchanged |
| **versiond host** removal, replace, or supervisor upgrade | **`versiond-router`** host evacuation | **Yes** — mark upstream `down`, drain pinned escrows, then stop the host |

During a devshardd binary swap, sticky routing is unchanged: the router still
points at `versiond-N:8080`; only the child port inside versiond swaps. Router
drain is for when the **versiond process itself** must leave the pool (scale-down,
host maintenance, versiond binary upgrade).

**Signals the plan adds to `devshardd`:** `/healthz` (liveness), `/ready`
(readiness gate for route swap), `/drain/status` (in-flight work), and configurable
`DEVSHARD_SHUTDOWN_GRACE` so long SSE streams are not cut at 5s.

**Kubernetes mapping (Part 2).** The same guarantees map to `RollingUpdate`
(`maxUnavailable: 0`, `maxSurge: 1`), **readinessProbe** → `/ready`,
**preStop** (drop from endpoints before `SIGTERM`), and
**terminationGracePeriodSeconds** aligned with shutdown grace. Pod/host evacuation
maps to Part 1 §1.8 (router drain), not the in-versiond binary swap.

### How rolling updates apply in this HA proposal

- **Stateless services** (edge-api, edge-srv echo workers, signer): standard
  rolling update — bring a new instance up, health-check, route to it, drain the
  old. Behind their routers / queue groups this is transparent.
- **Leader-elected services** (edge-api emitter, node-manager): a rolling update
  may trigger a leader handoff; the Redis lease + cursor make this safe (new
  leader resumes from the cursor).
- **versiond / devshardd** (same version, new binary): blue/green + drain inside
  versiond, with the **shared Postgres** making old+new overlap correct — see
  [../rolling-update.md](../rolling-update.md) §1.
- **versiond host** replace, scale-down, or maintenance: drain at
  **`versiond-router`** (mark upstream down, wait for pinned escrows idle, then
  stop the host) — see [../rolling-update.md](../rolling-update.md) §1.8.
- **NATS / Redis / Postgres**: run in their own HA/cluster modes; updated with
  their native rolling procedures, independent of app rollouts.

---

## 7. Phasing (suggested)

1. **Standalone NATS + signer service.** Externalize NATS; move warm-key signing
   into a queue-group signer. dapi publishes chain msgs to NATS instead of
   signing inline. (Unblocks multi-instance for the write path.)
2. **edge-api event hub + Redis leader election.** Move the event listener into
   edge-api; add Redis lock + cursor; publish events to subscribers.
3. **Point dapi & devshardd at edge-api** for chain reads + events; remove their
   direct chain subscriptions/queries.
4. **Split dapi** into node-manager (leader-elected) + stateless echo workers
   (PoC callbacks + admin); migrate per-process SQLite state to Postgres/Redis.
5. **Enable N-instance deployment** of every service + rolling updates per
   [../rolling-update.md](../rolling-update.md).
6. **Helm chart.** Package the decomposed stack for Kubernetes: Deployments,
   Services, Ingress (or Gateway API), ConfigMaps/Secrets, optional HPA/PDB, and
   values for shared Postgres / NATS / Redis — see §9.

---

## 9. Kubernetes & Helm (deployment target)

Docker Compose overlays (`local-test-net/`, `deploy/join/`) prove multi-instance
topology today; **production HA** should land on **Kubernetes** with a **Helm
chart** that encodes the same service boundaries as this proposal.

### Why HA work must precede the chart

A Helm chart alone does not make a monolith HA. The chart assumes:

- **Stateless or externally stateful** workloads (no embedded NATS, no
  per-pod SQLite as the source of truth).
- **Shared backends** wired via values: `devshard-postgres`, dapi Postgres,
  NATS cluster, Redis.
- **Leader-elected** components (edge-api event emitter, node-manager) use Redis
  leases — not K8s leader election alone — so behavior is identical in Compose
  and K8s.
- **Rolling updates** map to K8s `RollingUpdate` + readiness/preStop + long
  `terminationGracePeriodSeconds` as described in
  [../rolling-update.md](../rolling-update.md) Part 2.

### Intended chart shape (high level)

| Chart / workload | K8s notes |
|------------------|-----------|
| `edge-api` | `Deployment` + `Service`; `readinessProbe` → `/healthz`; HPA-friendly |
| `edge-api` event hub | Same image/chart; leader via Redis; subscribers use cluster DNS or NATS |
| `versiond` | `Deployment` + sticky `Service` or Ingress consistent-hash on escrow id |
| `devshardd` | Child of versiond in-process today; chart may deploy versiond only |
| `signer` | `Deployment`; NATS queue-group consumer; **one message consumed once** |
| `dapi` services | Split Deployments: echo workers (scale out), node-manager (leader) |
| `proxy` | Optional Ingress / Gateway in front of edge-api, dapi, versiond paths |
| **Dependencies** | Postgres, NATS, Redis as subcharts or external endpoints in `values.yaml` |

### Helm deliverable

- **Single umbrella chart** (or app-of-apps) for a Gonka node: enable/disable
  HA overlays (multi edge-api, multi versiond, external NATS/Redis) via values.
- **Documented values** for `PGHOST`, NATS URL, Redis URL, chain gRPC/RPC URLs,
  replica counts, resource limits, and graceful shutdown timeouts aligned with
  inference/SSE duration.
- **CI**: `helm template` / `helm lint` on chart changes; optional kind smoke.

Compose remains the **developer / integration-test** path; Helm is the **target
for production Kubernetes** once Pillars A–B and phasing steps 1–5 are in place.

---

## 8. Open questions

- **Event delivery guarantees** to subscribers: at-least-once (with idempotent
  handlers) vs effectively-once via Redis Streams / NATS JetStream consumers.
- **edge-api emitter vs node-manager leader:** one shared leadership domain or
  two independent leases? (Two keeps query scaling independent from phase-engine
  scaling.)
- **devshardd disputes:** route through the signer queue (uniform) or keep
  devshardd's own warm-key tx path? Uniform is cleaner but requires devshardd to
  publish to NATS.
- **Redis vs NATS JetStream KV** for the cursor/lock — avoid adding Redis if
  JetStream KV suffices. (Proposal assumes Redis per the stated direction.)
- **Cache invalidation** for edge-api chain cache around epoch/phase boundaries.

---

## References

- Current architecture: [../high-availability-architecture.md](../high-availability-architecture.md)
- Rolling updates: [../rolling-update.md](../rolling-update.md) (K8s rolling-update semantics, Part 2)
- edge-api extraction: [../pixelplex-changes.md](../pixelplex-changes.md)
- Storage modes: [../storage-design.md](../storage-design.md)
- Runtime topology / merge: [../merge-plan.md](../merge-plan.md)
