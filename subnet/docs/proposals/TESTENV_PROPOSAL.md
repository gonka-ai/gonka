# Subnet testenv proposal

## What it is

**`subnet/testenv`** is the **default lab** for subnet work: a **self-contained Docker Compose** stack that runs the subnet **without** `decentralized-api` or a live Cosmos chain. A **mock gRPC mainnet** (`mock-server`) serves escrow and host metadata from local config; **`subnethost`** runs one participant per service with stub inference/validation engines; **testenv `subnetctl`** exposes an OpenAI-style HTTP front-end for the escrow owner.

It exists to **decouple subnet development** from mainnet, testnet, and Testermint so protocol and transport changes can be exercised **locally in minutes**. See **`subnet/testenv/README.md`** for layout, `make` targets, and generated compose.

## Development workflow: hot reload

For day-to-day coding, **development mode** layers **`docker-compose.dev.yml`** on the generated base stack: dev images (**`Dockerfile.dev`**), **Air** for Go rebuild/restart on file changes, and optional **Delve** on selected services for breakpoints.

Commands and debug ports are documented in **`subnet/testenv/DEVELOPMENT-MODE.md`**.

## Observability

The same compose network can run an **observability stack** (metrics today; logs/traces/dashboards phased). Architecture, services (e.g. cAdvisor, Node Exporter, Grafana Alloy, VictoriaMetrics, Loki, Tempo), and how to enable it are described in:

**[`subnet/testenv/OBSERVABILITY.md`](../../testenv/OBSERVABILITY.md)**

Strategy for using this stack in tests, hardening it for production, and **inference-chain / Cosmos SDK** instrumentation: **`subnet/docs/proposals/OBSERVABILITY_PROPOSAL.md`**.

## Automated E2E protocol tests

Testenv is also the **intended runtime** for **multi-process protocol E2E**: real HTTP between `subnetctl` and participants, real gossip, mock chain RPCs, Docker-level faults, and (when implemented) **Python-driven** scenario harnesses plus **dev-only control-plane** rules on `mock-server` / `subnethost`.

Goals, scenario model, injection from tests, and assertion strategy are specified in:

**[`subnet/docs/proposals/PROTOCOL_TESTING_PROPOSAL.md`](./PROTOCOL_TESTING_PROPOSAL.md)**

## Summary

| Role | Pointer |
|------|---------|
| Spin up / config / everyday usage | `subnet/testenv/README.md` |
| Hot reload + debugging | `subnet/testenv/DEVELOPMENT-MODE.md` |
| Metrics / observability runbook | `subnet/testenv/OBSERVABILITY.md` |
| Observability goals (prod + chain) | `subnet/docs/proposals/OBSERVABILITY_PROPOSAL.md` |
| Protocol E2E testing design | `subnet/docs/proposals/PROTOCOL_TESTING_PROPOSAL.md` |

Together, testenv is **the** environment for **subnet development**, **observable** runs, and **automated protocol E2E** in CI or on a laptop.
