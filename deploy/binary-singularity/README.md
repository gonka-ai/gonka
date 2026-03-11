# Binary Singularity — Deploy Stack

Reproducible deploy for all environments. Each tier is independently runnable
and builds on the previous. All tiers use the same `scenario-runner` binary
and produce comparable metrics (convergence proof, hub approval, PQM).

## Tiers

| Tier | Stack | RAM | Time | PQM target |
|------|-------|-----|------|------------|
| **LITE** | Docker Compose, 2 containers | 1 GB | ~15 min | ≥ 0.95 |
| **MEDIUM** | Docker Compose + opengnk proxy + quality-middleware | 2 GB | ~30 min | ≥ 1.00 |
| **HARD** | k3d (K3s-in-Docker), 1 server + 3 agents, full mesh | 4 GB | ~60 min | ≥ 1.01 |

## Requirements

All tiers:
- Linux x86_64 (Debian Bookworm or Ubuntu 22.04+)
- Docker ≥ 24.x with Compose plugin

MEDIUM:
- Gonka account keys (`GONKA_PRIVATE_KEY` + `GONKA_ADDRESS`) — optional, falls back to mock

HARD:
- [k3d](https://k3d.io) ≥ 5.x: `curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash`
- kubectl
- Go ≥ 1.22 (for local runner build)

## LITE — Quick Start

```bash
cd deploy/binary-singularity/lite
cp .env.example .env
./run.sh                               # standard 12-iteration run
./run.sh --hub-key gnk_live_...        # with hub approval check
./run.sh --raw-input /path/to/text     # Exp 4: raw binary ingestion
```

Expected output (convergence proof):
```
SlotModeHitRate: 100%  AvgSlotLatency: ~5ms  GPU baseline: ~120ms
HubApproval: PQM ≥ 0.988  Verdict: APPROVED
```

## MEDIUM — With opengnk Proxy

```bash
cd deploy/binary-singularity/medium
cp .env.example .env
# Fill GONKA_PRIVATE_KEY + GONKA_ADDRESS for live inference
# Leave empty to use mock-node (fully reproducible)
nano .env
docker compose up -d
# Check quality metrics:
curl http://localhost:9090/quality/stats | jq .
# Run experiment with live Gonka models:
docker compose --profile live up opengnk
MODELS=Qwen/Qwen3-235B-A22B-Instruct-2507-FP8 docker compose run runner ...
```

Mesh pool signals (cross-node semantic pooling):
```
GET http://localhost:9090/quality/stats → {"mesh_pool": {"node_count":N, "slot_hits":M, ...}}
```

## HARD — K3s Mesh (mirrors bookworm Exp 3/4)

```bash
cd deploy/binary-singularity/hard
./run.sh                               # 1 server + 3 agents, 12 iterations
./run.sh --raw-input /path/to/text     # Exp 4 replay
./run.sh --iterations 36 --hub-key KEY # full 3072-run scale
./run.sh --destroy                     # tear down cluster
```

Topology:
```
k3d-bs-mesh-server-0  (control plane)
k3d-bs-mesh-agent-0   (embedder pod)
k3d-bs-mesh-agent-1   (mock-node pod replica 1)
k3d-bs-mesh-agent-2   (mock-node pod replica 2)
```

## opengnk SDK Integration

The MEDIUM/HARD stacks integrate [opengnk](https://github.com/gonkalabs/opengnk/tree/dev)
as the production inference proxy. The quality-middleware wraps it and measures:
- **L6**: slot/cache reuse rate via `X-Cache: HIT` header
- **L8**: latency CV (consistency metric)
- **L9**: HTTP completion rate
- **DX**: explicit feedback via `X-Inference-Feedback`
- **Mesh pool**: cross-node semantic signals via `X-BS-Node-ID` header

To use opengnk with real Gonka nodes:
```bash
# Start proxy + quality measurement
docker compose --profile live up -d opengnk quality-middleware
# Route runner through quality middleware (wraps opengnk)
QM_UPSTREAM=http://opengnk:8080 docker compose up -d quality-middleware
```

## Experiment Results Reference

| Exp | Tier | Runs | PQM | Slots | Verdict |
|-----|------|------|-----|-------|---------|
| 1 | LITE | 256 | — | 4 | baseline |
| 2 | LITE | 9216 | 0.988 | 4 | APPROVED |
| 3 | HARD | 15360 | 1.001 | 6 | DOMINATES |
| 4 | HARD (raw) | 11520 | 1.020 | 197 | DOMINATES |

All results reproducible. See `binary-singularity/results/` for stored artifacts.

## Protocol Compatibility

These stacks are designed to **not conflict with the Gonka protocol** (#859 / #860):
- `PatternSlot` store operates at client/DAPI level, below chain consensus
- Hub check uses public read-only API only (`/api/public/stats/historical`)
- `CacheQualityParams.Enabled = false` by default — requires governance tx
- All new prefixes (48-51) are above `EpochGroupValidationEntryPrefix` (47)
- PruningState wire format is additive (fields 5-8 new, fields 1-4 unchanged)
