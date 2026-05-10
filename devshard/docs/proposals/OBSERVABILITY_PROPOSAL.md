# Observability proposal (testenv lab → production & chain)

## Reference implementation

The **concrete stack** (Grafana Alloy, VictoriaMetrics, cAdvisor, Node Exporter, phased Loki/Tempo/Grafana) and how to run it in **`subnet/testenv`** are documented here:

**[`subnet/testenv/OBSERVABILITY.md`](../../testenv/OBSERVABILITY.md)**

This proposal states **why** that stack exists beyond “nice charts,” and **what** should evolve next—especially for **inference-chain** (Cosmos SDK) and **shared** environments.

## Goals (two tracks)

### Track A — Subnet testenv observability

Use the same pipeline to **observe protocol and load tests**: correlate participant containers, subnetctl, and mock-server behavior during E2E scenarios (see **`subnet/docs/proposals/PROTOCOL_TESTING_PROPOSAL.md`**). The target is to **instrument gossip** (fan-out, retries, drops) and the **full subnet protocol flow** (inference hand-off, diffs, mempool, timeout votes, finalization) so metrics and (later) logs and traces make regressions and stuck states **easier to explain** than raw Docker logs alone.

### Track B — Observability as a product under test

Run Alloy + backends in testenv as an **experimental lab** to **validate configuration, security, and scaling patterns** before adopting them for **production** and for **inference-chain** nodes. Today it is hard to answer *why* a host is **slow or stuck**: **consensus**, **pruning**, **state sync**, **storage**, and application layers can all contribute, but signals are fragmented. The long-term aim is **one analytical place** (metrics, logs, traces) where operators can **narrow performance and availability issues** to a real subsystem.

## Problem statement

- **Symptom:** Host or validator **failures**, **stalls**, or **latency spikes** where the **root cause** is unclear.
- **Cause gap:** Multiple concurrent concerns—**hardware faults**, **hosts under heavy load** (CPU, disk, memory, network saturation), and software layers such as **Tendermint/Cosmos consensus**, **ABCI**, **pruning**, **IAVL / DB**, **P2P**, **mempool**, and **custom modules**—all without **consistent, correlated** telemetry tying infra signals to chain behavior.
- **Need:** **Instrumentation** at **Cosmos SDK and adjacent layers**, plus **uniform collection** (Alloy or equivalent) so blockchain-relevant dimensions (height, peer set, module, stage) appear alongside infra metrics.

## Direction: experimental lab → hardened pipeline

The following items are **proposed**; they extend what **`subnet/testenv/OBSERVABILITY.md`** describes for phase 1.

### 1. Richer identity in scraped data (Alloy)

- Attach **hot / warm key addresses** (and other **epoch- or group-scoped** labels) to **scraped metrics**, **telemetry**, and **log metadata** where the chain or subnet exposes them, so queries can slice by **participant identity** without guessing from container names alone.

### 2. Authenticated ingestion (writers and backends)

- **Alloy** (or collectors) should **sign** outbound **remote write** / **OTLP** / **log push** to backends so tampering and spoofing are harder.
- **VictoriaMetrics, Loki, and Tempo** (or proxies in front of them) should enforce **authorization**: only **actors tied to the current epoch / active participant set** may **ingest** production-grade data. Testenv may keep relaxed rules for developer velocity; **production and shared testnets** should not rely on “Docker network only” as the sole control (see also the vmauth note in **`OBSERVABILITY.md`**).

### 3. Scale the observability plane

- Define **horizontal scaling**, **retention**, and **multi-tenant** boundaries for metrics/logs/traces as usage grows from **one laptop** to **many validators** and **CI** runners.
- Reuse patterns proven in testenv compose before rolling them to **inference-chain** deployments.

### 4. OpenTelemetry at Cosmos SDK and nearest layers

- Add **OTel metrics and traces** (and structured logs where feasible) **inside and around** the **Cosmos SDK / CometBFT** stack used by **inference-chain**: block lifecycle, commit phases, **store** access, **pruning**, **gRPC / REST**, and **critical module** paths.
- Goal: when a node is **stuck or slow**, dashboards and trace waterfalls show **which stage** (e.g. commit, pruning tick, compaction) dominates—not only that CPU or disk is high.

## Relationship to other docs

| Document | Role |
|----------|------|
| **`subnet/testenv/OBSERVABILITY.md`** | Current stack, phases, ports, operational notes |
| **`subnet/docs/proposals/TESTENV_PROPOSAL.md`** | Testenv as dev + E2E lab |
| **`subnet/docs/proposals/PROTOCOL_TESTING_PROPOSAL.md`** | Protocol tests that benefit from correlated observability |

## Non-goals (for this proposal text)

- Mandating a single vendor; **Alloy / VictoriaMetrics / Loki / Tempo** are the **current** choices in-repo and may evolve.

## Summary

Observability in **testenv** is intentionally **dual-purpose**: **support subnet and protocol tests today**, and **exercise** the **same tool chain and policies** we want for **production** and **inference-chain** debugging—**identity-aware collection**, **authenticated ingestion**, **scale**, and **deep SDK-level instrumentation** so **performance and stuck-node** investigations have a **single, trustworthy analytical surface**.
