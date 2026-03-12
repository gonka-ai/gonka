# Binary Singularity — Deploy Stack

Branch: [`Mayveskii/gonka → dev/binary-singularity`](https://github.com/Mayveskii/gonka/tree/dev/binary-singularity)  
Relates to: [PR #859](https://github.com/gonka-ai/gonka/pull/859) · [GiP #860](https://github.com/gonka-ai/gonka/discussions/860) · base: `upgrade-v0.2.11`

Four deployment tiers: LITE/MEDIUM/HARD for reproducible testing,
PRODUCTION for real node infrastructure with GNK tokens and live inference.

## Tiers

| Tier | Stack | What it does | GNK tokens |
|------|-------|-------------|------------|
| **LITE** | Docker Compose, embedder + mock-node | Reproducible convergence proof | No |
| **MEDIUM** | Docker Compose + opengnk + quality-middleware | SDK client flow with live Gonka (optional) | Optional |
| **HARD** | k3d (K3s-in-Docker), 1 server + 3 agents | Multi-node mesh, full experiment replay | No |
| **PRODUCTION** | On real Gonka node (deploy/join) + BS layer | Live inference, continuous slot distillation | **Yes** |

## PRODUCTION — Real Node Infrastructure

Runs on top of the existing node stack (`deploy/join/`).
Real DAPI, real mlnode, real GNK token consumption.

```bash
cd deploy/binary-singularity/production
cp .env.example .env
nano .env  # REQUIRED: fill GONKA_PRIVATE_KEY, GONKA_ADDRESS, GONKA_API_KEY
docker compose up -d
```

Architecture on a real node:
```
┌─── EXISTING NODE (deploy/join) ──────────────────────────┐
│  tmkms → node → api (DAPI :9000/:9100/:9200)            │
│  mlnode-308 → inference (nginx :8080/:5050)              │
└──────────────────────┬───────────────────────────────────┘
                       │
┌──────────────────────┴───────────────────────────────────┐
│  BINARY SINGULARITY LAYER                                │
│                                                          │
│  embedder (:8686)  — CPU-only, all-MiniLM-L6-v2         │
│  opengnk  (:8080)  — SDK proxy, secp256k1 signing       │
│  quality-middleware (:9090) — L6/L8/L9/DX + mesh pool    │
│  bs-runtime        — continuous slot distillation        │
│    └─ watches live traffic                               │
│    └─ distills high-quality responses → PatternSlots     │
│    └─ ingests raw binary input (BS_RAW_INPUT) on startup │
└──────────────────────────────────────────────────────────┘
```

Services:
- **opengnk** — SDK clients connect here (OpenAI-compatible). Handles Transfer Agent whitelist, multi-wallet rotation, tool-call simulation.
- **quality-middleware** — wraps opengnk, measures L6/L8/L9/DX per request, exposes `GET /quality/stats` and `POST /quality/search` (CPU cosine pool).
- **bs-runtime** — continuous mode: watches quality-middleware, distills PatternSlots from responses with quality ≥ threshold. No experiment loop.
- **embedder** — CPU-only fastembed sidecar. Used by both DAPI semantic cache (L2) and slot distillation.

For SDK clients (gonka-agent, any OpenAI client):
```python
from openai import OpenAI
client = OpenAI(base_url="http://<node>:8080/v1", api_key="not-needed")
response = client.chat.completions.create(
    model="Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
    messages=[{"role": "user", "content": "Hello!"}]
)
```

## Raw Binary Input — Any Data Source

Set `BS_RAW_INPUT` in `.env` to feed **any file** into the binarizer:

```bash
# Developer workflow log
BS_RAW_INPUT=/home/user/Документы/text

# Git commit history
BS_RAW_INPUT=/home/user/repos/project/.git/logs/HEAD

# Downloaded specification
BS_RAW_INPUT=/tmp/downloaded_spec.md

# Custom markup or notes
BS_RAW_INPUT=/home/user/notes/architecture-decisions.txt
```

The file is transmitted **as-is** to the cluster embedder. No pre-processing.
The embedder splits by lines (`BS_CHUNK_LINES`, default 50), embeds each chunk,
and distills PatternSlots stored in the slot directory.

This works in ALL tiers (LITE, MEDIUM, HARD, PRODUCTION).

## LITE — Reproducible Test

```bash
cd deploy/binary-singularity/lite
cp .env.example .env
./run.sh                               # standard run
./run.sh --hub-key gnk_live_...        # with hub approval
BS_RAW_INPUT=/path/to/file ./run.sh    # raw binary ingestion
```

## MEDIUM — With opengnk Proxy

```bash
cd deploy/binary-singularity/medium
cp .env.example .env && nano .env
docker compose up -d
curl http://localhost:9090/quality/stats | jq .
# Mesh pool search:
curl -X POST http://localhost:9090/quality/search \
  -d '{"query": [0.1, 0.2, ...], "top_k": 5}'
```

## HARD — K3s Mesh

```bash
cd deploy/binary-singularity/hard
./run.sh                               # 1 server + 3 agents
./run.sh --raw-input /path/to/text     # raw binary ingestion
./run.sh --destroy                     # tear down cluster
```

## gonka-agent Integration

The agent reads `BS_*` env vars from `.env.example`:
```bash
# In gonka-agent/.env
BS_RAW_INPUT=/path/to/your/data
BS_SLOT_DIR=~/.gonka-cache/slots
BS_EMBED_URL=http://localhost:8686
BS_QUALITY_URL=http://localhost:9090
BS_MIN_SIM_BPS=7500
BS_DISTILL_MODE=continuous
```

Agent flow:
1. On startup, if `BS_RAW_INPUT` is set → ingest, create slots
2. On each task, check slot store first (cosine sim ≥ `BS_MIN_SIM_BPS`)
3. On successful completion, distill result → new slot
4. Slots shared via quality-middleware mesh pool across nodes/clients

## Experiment Results (Bookworm, CPU-only, no GPU)

| Exp | Tier | Runs | PQM | Slots | Peak RAM | Slot latency | Verdict |
|-----|------|------|-----|-------|----------|-------------|---------|
| 1 | LITE | 256 | — | 4 | — | — | baseline |
| 2 | LITE | 9,216 | 0.988 | 4 | 19.25 MB | ~5 ms | Hub APPROVED |
| 3 | HARD | 15,360 | **1.001** | 6 | 23.1 MB | ~5 ms | DOMINATES GPU |
| 4 | HARD (raw) | 11,520 | **1.020** | 197 | 20.8 MB | ~5 ms | DOMINATES GPU |

### Network correlation (live data, epochs 161–191, 2,503,595 inferences)

| Metric | Network (no BS) | With BS | Delta |
|--------|----------------|---------|-------|
| L6 cache hit rate | 0.000473 (M=571) | 0.27+ (slot store) | **571×** |
| L8 latency mean | 1280 ms (GPU) | ~5 ms (slot hit) | **~250×** |
| L9 completion | 90.4% | 100% tracked | +10% |
| Memory | 16 GB GPU VRAM | 19–23 MB CPU | **~700×** less |
| GPU saves/epoch (20% spec.) | 0 | **940,698** | — |

## Key Parameters

```
Hit rate formula:  H = repeat_fraction × (1/M) × (1 - stream_fraction)
Optimal L2 threshold: 4250 bps (F1=0.986, quality_matrix_research_v2)
Current deployment:   7500 bps (loses 64% of valid hits)
Coherence floors:     sim≤6250→3000 · sim 6250-8000→4000 · sim>8000→4500
Loop closure margin:  -800 bps
PQM formula:          QualityScore(binary) / QualityScore(single_gpu)
Network composite:    0.7236 (6/10 axes measured, epochs 161-191)
```

Economics (H100 $2.50/h): 1 node ~$1,355/year · 33 nodes ~$43,578/year · full protocol ~$155,800/year

Related: [PR #859](https://github.com/gonka-ai/gonka/pull/859) ·
[GiP #860](https://github.com/gonka-ai/gonka/discussions/860) ·
[PR #856](https://github.com/gonka-ai/gonka/pull/856) (Continuous PoC) ·
[PR #812](https://github.com/gonka-ai/gonka/pull/812) (perf) ·
[PR #793](https://github.com/gonka-ai/gonka/pull/793) (EpochGroupCache) ·
[GiP #816](https://github.com/gonka-ai/gonka/issues/816) (Node Manager) ·
[GiP #840](https://github.com/gonka-ai/gonka/issues/840) (Prometheus)

## Protocol Compatibility

No conflict with Gonka protocol (#859 / #860):
- PatternSlot store at client/DAPI level, below chain consensus
- Hub check uses read-only `/api/public/stats/historical`
- `CacheQualityParams.Enabled = false` by default — governance activates
- PruningState wire format additive (fields 5-8, no changes to 1-4)
- Prefix collision free: 47=EpochGroup (upstream), 48-51=ours

## Shipped components

| Component | Path | Format |
|-----------|------|--------|
| Deploy stack (4 tiers) | `deploy/binary-singularity/` | Docker Compose + K3s + K8s |
| Quality middleware | `examples/quality-middleware/` | Go source |
| K8s overlay | `test-net-cloud/k8s/overlays/binary-singularity/` | Kustomize |
| gonka-agent (source) | `gonka-agent/` | Go module |
| gonka-agent (binary) | `gonka-agent/bin/gonka` | ELF x86_64, static, 6 MB |
| Binary artifact | `text` | 720 KB, SHA256: 81b5449a... |
