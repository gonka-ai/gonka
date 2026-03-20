# PoC Validation Scripts

This folder contains the data collection pipeline for **PoC (Proof-of-Computation) vector validation** against already running vLLM servers with PoC support.

The goal is to verify that PoC vector generation is consistent across different GPUs, vLLM versions, and model configurations — and to detect fraud (e.g., a node running INT4 instead of FP8).

## Pipeline Overview

```
1. collect_data.py          Collect PoC vectors from one or more servers
2. poc_l2_histogram.py      Compare two runs by L2 distance (scripts/analysis/)
```

## What These Scripts Do

### `collect_data.py`

- Reads server list and model params from `config.json`.
- Calls `/api/v1/pow/generate` on each server with a block hash, public key, and nonce range.
- Decodes returned base64 FP16 vectors to FP32.
- Saves per-server artifact files and a run config with vLLM runtime probe.
- Supports `--continue` to resume interrupted collection.

### `scripts/analysis/poc_l2_histogram.py`

- Takes two collection runs and matches vectors by nonce.
- Computes L2 (Euclidean) distance between paired vectors.
- Plots a histogram with a configurable percentile threshold line (default p98).
- When server names differ across runs (e.g., cross-version comparison), automatically cross-compares the first server from each.

## Config Schema

`config.json` controls which servers to collect from and with what parameters:

```json
{
  "model": "Qwen/Qwen3-0.6B",
  "seq_len": 1024,
  "k_dim": 12,
  "block_hash": "TEST_BLOCK",
  "public_key": "test_pub_key",
  "block_height": 100,
  "batch_size": 64,
  "nonce_count": 500,
  "servers": {
    "my_server": "http://127.0.0.1:8000"
  }
}
```

- `model`: Model name (must match what the server is serving).
- `seq_len`: Sequence length for PoC generation.
- `k_dim`: Output vector dimension (default 12).
- `block_hash` / `public_key`: Deterministic seeds. Can also use `block_hashes` / `public_keys` arrays for multi-seed mode.
- `batch_size`: How many nonces to process per batch.
- `nonce_count`: Total nonces to generate (0..N-1).
- `servers`: Map of `name → URL`. One artifact file is saved per server.

## Artifact Structure

Artifacts are created under:

`benchmarks/data/poc_calidation/<EXP_NAME>_<YYYY-MM-DD_HHMMSS>/`

Each experiment folder contains:

- `run_config.json` — input config, vLLM runtime probe, CLI args
- `artifacts_<server_name>.json` — per-server results (single-seed mode)
- `artifacts_<server>_<hash>_<pubkey>.json` — per-server results (multi-seed mode)

Each artifact JSON includes:

- `server_name`, `server_url`, `block_hash`, `public_key`
- `nonces` — list of nonce integers
- `vectors` — list of decoded float arrays (or null on failure)
- `artifacts` — raw server response with base64 vectors
- `encoding` — `{dtype, k_dim, endian}`
- `timing` — `{started_at, finished_at, elapsed_seconds, nonces_per_min}`

## Usage

Run from `benchmarks/` directory (or adjust paths).

### 1) Collect Data

Edit `config.json` to point at your server(s), then run:

```bash
python scripts/poc_validation/collect_data.py
```

To compare two servers or two vLLM versions, run collection twice with different server configs. For example:

```bash
# Run 1: collect from vLLM v0.9.1
# (edit config.json: servers → {"vllm_091": "http://127.0.0.1:8001"})
python scripts/poc_validation/collect_data.py

# Run 2: collect from vLLM PS (v0.15.1)
# (edit config.json: servers → {"vllm_ps": "http://127.0.0.1:8000"})
python scripts/poc_validation/collect_data.py
```

Optional flags:

- `--config <path>` — path to config file (default: `config.json` in this folder)
- `--continue` — resume from latest run, skipping already-completed tasks

### 2) Compare Runs

```bash
# Auto-detect two most recent runs:
python scripts/analysis/poc_l2_histogram.py

# Compare specific runs:
python scripts/analysis/poc_l2_histogram.py \
  --run-a data/poc_calidation/tinyllama_091_2026-02-25_235406 \
  --run-b data/poc_calidation/tinyllama_ps_2026-02-26_000537
```

Optional flags:

- `--percentile <N>` — threshold percentile (default: 98)
- `--out <dir>` — output directory for plots (default: `data/plots/`)
- `--server <name>` — only compare this server (default: all common servers)

Output: histogram PNG in `data/plots/` and summary stats printed to stdout.

## Typical Experiment Scenarios

### Cross-version (honest pair)

Collect from two vLLM versions with the same model and precision. Expect L2 distances near zero (mean ~0.001).

### Cross-GPU (honest pair)

Collect from same vLLM version on different GPUs (e.g., A100 vs H100). FP8 kernels differ across GPU architectures, so distances will be small but non-zero.

### Fraud detection (FP8 vs INT4)

Collect from an honest FP8 server and a fraudulent INT4 server. The L2 distances should be significantly larger (mean ~0.2+), forming a clearly separated distribution from the honest pairs.

## Important Notes

- `VLLM_ATTENTION_BACKEND` must match across runs for meaningful comparison. Different attention backends (FlashInfer vs FlashAttention) produce numerically different results, leading to inflated distances.
- The `EXP_NAME` is hardcoded in the script as `"poc_validation"`. Rename output folders after collection if you want descriptive names.
- These scripts call the vLLM PoC endpoint directly (`/api/v1/pow/generate`), not the MLNode proxy path (`/api/v1/inference/pow/generate`).
