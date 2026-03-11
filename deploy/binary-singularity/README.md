# Binary Singularity — Deploy Stack

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

## Experiment Results

| Exp | Tier | Runs | PQM | Slots | Verdict |
|-----|------|------|-----|-------|---------|
| 1 | LITE | 256 | — | 4 | baseline |
| 2 | LITE | 9216 | 0.988 | 4 | APPROVED |
| 3 | HARD | 15360 | 1.001 | 6 | DOMINATES |
| 4 | HARD (raw) | 11520 | 1.020 | 197 | DOMINATES |

## Protocol Compatibility

No conflict with Gonka protocol (#859 / #860):
- PatternSlot store at client/DAPI level, below chain consensus
- Hub check uses read-only `/api/public/stats/historical`
- `CacheQualityParams.Enabled = false` by default — governance activates
- PruningState wire format additive (fields 5-8, no changes to 1-4)
- Prefix collision free: 47=EpochGroup (upstream), 48-51=ours
