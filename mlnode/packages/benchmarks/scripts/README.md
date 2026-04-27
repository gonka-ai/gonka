# Benchmarks & Validation Scripts

All paths below are relative to `gonka/mlnode/packages/benchmarks/`.

---

## Background

Gonka is a decentralized AI compute network. Participants contribute GPU hardware to run inference and training workloads. The fundamental trust problem: **how do you verify that a node is actually running the model it claims to run?**

A dishonest node could load a cheaper quantized model (e.g., INT4 instead of FP8) to save GPU resources while still claiming the full reward. Validation exists to catch this.

### MLNode and vLLM

Each participant runs **MLNode** — a Python service that wraps a forked vLLM as its inference and Proof-of-Computation (PoC) backend. MLNode exposes a FastAPI server on port 8080 that:

- Proxies OpenAI-compatible inference requests to one or more vLLM backend instances
- Orchestrates PoC vector generation across all backends
- Manages model lifecycle (load, unload, health checks)

The vLLM fork includes PoC v2 — deterministic vector generation integrated directly into the inference engine, so PoC runs inside the same process without offloading the model.

### The Validation Problem

Different GPUs (A100, H100, H200, B200, RTX6000, A800) have different FP8 kernels and floating-point behavior. An honest FP8 run on an A100 and an honest FP8 run on an H100 will produce *slightly* different results. Similarly, different vLLM versions may produce slightly different outputs.

Validation must **tolerate this natural cross-GPU/cross-version variance** while still detecting fraud (a node running INT4 while claiming FP8).

The approach for both validation systems:

1. **Honest pairs** (FP8 vs FP8 across different GPUs): produce a tight, low-distance distribution.
2. **Fraud pairs** (INT4 vs FP8): produce a systematically higher distance distribution.
3. Find a **threshold** that separates them — all honest pairs below, most fraud pairs above.

---

## Overview

There are **two independent validation systems**:

| System | What it validates | Comparison method | Scripts |
|--------|-------------------|-------------------|---------|
| **Inference Validation** | Logprob distributions from text generation | Per-token logprob distance (`distance2`) | `scripts/inference_validation/` |
| **PoC Validation** | Float vectors from Proof-of-Computation | L2 Euclidean distance | `scripts/poc_validation/` |
| **Analysis** | Visualization and threshold search | Scatter plots, histograms, F1-optimal bounds | `scripts/analysis/` |

Both follow the same methodology:

1. Collect outputs from **two configurations** (e.g., FP8 on GPU-A vs FP8 on GPU-B for honest, or FP8 vs INT4 for fraud).
2. Compute a **distance** between paired outputs.
3. Find a **threshold** that separates honest (low distance) from fraud (high distance).

---

## Experiment Structure

All scripts write into a unified directory under `data/experiments/`. **One experiment = one server + one model deployment.** All artifacts from a single run live together in one flat folder.

```
data/experiments/<exp_name>_<YYYY-MM-DD_HHMMSS>/
│
├── server.json                  # (optional) GPU info, ports, health, deployment params
├── poc_config.json              # PoC hyperparams + runtime probe
├── poc_artifacts.json           # Vectors + nonces + timing (nonces_per_min)
├── inference_config.json        # Request params, model info, timing
├── inference_results.jsonl      # Raw inference logprobs (one row per prompt)
├── validation_config.json       # Source artifact path, validation server info, timing
├── validation_results.jsonl     # Combined inference+validation logprobs
└── plots/                       # Analysis PNGs
```

**Not every file needs to be present.** An experiment can contain:
- Just PoC (`poc_config.json` + `poc_artifacts.json`)
- Just inference (`inference_config.json` + `inference_results.jsonl`)
- Just validation (`validation_config.json` + `validation_results.jsonl`)
- Any combination

**`server.json`** is always optional — useful for provenance (GPU type, driver, etc.) but not required by any script. Skip it if you don't have SSH access or are testing locally.

**`validation_config.json`** records which inference artifact was used as input:

```json
{
  "source_inference_artifact": "/path/to/other_experiment/inference_results.jsonl",
  "validation_model_info": { ... },
  "request_params": { ... },
  "performance": { ... }
}
```

This makes cross-experiment references explicit — you always know which inference data was revalidated against which server.

### Keeping everything in one folder

All scripts accept `--exp-dir` to write into an existing experiment directory. For a full pipeline run, create the directory once and pass it to every step:

```bash
EXP=data/experiments/qwen06b_h200_full_2026-03-18_144658

python scripts/poc_validation/collect_data.py   --config my_config.json --exp-dir $EXP
python scripts/inference_validation/inference.py --url http://HOST:API_PORT  --exp-dir $EXP
python scripts/inference_validation/validation.py \
  --inference-artifact $EXP/inference_results.jsonl \
  --validation-url http://HOST:API_PORT \
  --exp-dir $EXP
```

> **Run steps sequentially, not in parallel.** PoC generation and inference/validation both saturate the GPU. Running them concurrently degrades throughput and can produce unreliable timing metrics. Always wait for one step to finish before starting the next.

If `--exp-dir` is not provided, each script creates a new timestamped directory under `data/experiments/` using `--exp-name`.

**Naming convention:** when unsure what to name an experiment, ask. A good name includes the model and GPU type, e.g., `qwen06b_h200`, `qwen235b_fp8_a100`. The timestamp is appended automatically.

> **Always verify the GPU type before naming an experiment.** Run `nvidia-smi --query-gpu=name,count --format=csv,noheader` on the target server (via SSH or `docker exec`) to confirm. Do not assume the GPU type from the hostname, provider, or previous experiments — machines get redeployed.

---

## Prerequisites

### Docker images

| Image | Tag | URL |
|-------|-----|-----|
| vLLM | `v0.15.1-alpha1` | `ghcr.io/product-science/vllm:v0.15.1-alpha1` |
| MLNode | `3.0.13-alpha1` | `ghcr.io/product-science/mlnode:3.0.13-alpha1` |

### Server requirements

The target server must be running the mlnode API (`api.app:app` on port 8080). Key endpoints:

- `POST /api/v1/inference/up` — Start vLLM with a model (synchronous)
- `POST /api/v1/inference/up/async` — Start vLLM (async, poll with `/inference/up/status`)
- `POST /api/v1/inference/down` — Stop vLLM
- `GET /v1/models` — List served models (proxied to vLLM)
- `POST /api/v1/inference/pow/init/generate` — Start continuous PoC generation (fans out to all backends)
- `POST /api/v1/inference/pow/stop` — Stop PoC generation

### Starting the server

On the target machine:

```bash
. /app/packages/api/.venv/bin/activate
cd /app/packages
python -m uvicorn api.app:app --host 0.0.0.0 --port 8080
```

### Deployment parameters

#### Qwen3-235B-A22B (FP8 / INT4)

| Quantization | Model ID |
|--------------|----------|
| FP8 | `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` |
| INT4 (W4A16 GPTQ) | `chriswritescode/Qwen3-235B-A22B-Instruct-2507-INT4-W4A16` |

| Parameter | Value | Notes |
|-----------|-------|-------|
| `--max-model-len` | `240000` | Required |
| `--tensor-parallel-size` | `4` on H100, H200, A100; `2` on B200 | Required |
| `--attention-backend` | `FLASHINFER` | Baked as default in vLLM v0.15.1. Override via `additional_args` if needed. |
| `--max-num-batched-tokens` | `32768` | Baked as default. Must be `≥ batch_size × seq_len` for PoC (see below). |
| `--logprobs-mode` | `processed_logprobs` | Baked as default. Can also be overridden per-request (see below). |
| `--compilation-config` | `'{"custom_ops": ["+quant_fp8", "+rms_norm", "+silu_and_mul", "+fused_moe", "+rotary_embedding", "+apply_rotary_emb", "none"]}'` | Baked as default for Qwen 235B. |

#### Choosing the attention backend

As of vLLM v0.15.1, `FLASHINFER` is the **baked default** attention backend. You do not need to set it explicitly — it is used automatically unless overridden.

To override, pass `"--attention-backend", "<BACKEND>"` in `additional_args` when calling `/inference/up`. You can also set the `VLLM_ATTENTION_BACKEND` environment variable before starting the API server; the CLI flag takes precedence when both are set.

Available backends (GPU-dependent):

| Backend | Notes |
|---------|-------|
| `FLASH_ATTN` | FlashAttention v2. |
| `FLASHINFER` | FlashInfer. **Default.** Required for MoE models like Qwen3-235B for correct PoC behavior. |
| `TRITON_ATTN` | Triton-based. Fallback when Flash/FlashInfer unavailable. |
| `FLEX_ATTENTION` | PyTorch Flex Attention. |

#### Choosing the logprobs mode

`--logprobs-mode` controls what values are returned in the `logprobs` and `prompt_logprobs` fields. As of vLLM v0.15.1, `processed_logprobs` is the **baked default**.

| Mode | Description |
|------|-------------|
| `processed_logprobs` | Logprobs **after** all logit processors (temperature, top-k/top-p, penalties). Token IDs are returned as strings, `-inf` is clamped to `-9999.0`. **Default.** |
| `raw_logprobs` | Logprobs from the model's raw output **before** any post-processing. Reflects the true model distribution without sampling-parameter artifacts. |
| `processed_logits` | Raw logit values (not log-softmax) after processors. |
| `raw_logits` | Raw logit values before processors. |

**Per-request override:** The logprobs mode can also be set on individual API requests via the `logprobs_mode` field, overriding the deployment-level default. Requests without the override fall back to the deployment default. Mixed batches (some requests raw, some processed) are handled automatically.

**Auto-detection for validation:** When a validation request (with `enforced_tokens`) does not specify `logprobs_mode`, vLLM automatically detects whether the original inference used raw or processed logprobs and sets the mode accordingly. This handles backward compatibility with older vLLM versions.

> **Priority chain:** explicit `logprobs_mode` on the request > auto-detected mode from enforced token IDs > deployment-level `--logprobs-mode` default.

#### Loading a model

Since `attention_backend`, `logprobs_mode`, `max_num_batched_tokens`, and `compilation_config` are baked as defaults, deployment only requires the model-specific parameters:

**FP8:**

```bash
curl -X POST http://<HOST>:<API_PORT>/api/v1/inference/up \
  -H "Content-Type: application/json" \
  -d '{
    "model": "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
    "dtype": "auto",
    "additional_args": ["--tensor-parallel-size", "4", "--max-model-len", "240000"]
  }'
```

**INT4 (W4A16 GPTQ):**

```bash
curl -X POST http://<HOST>:<API_PORT>/api/v1/inference/up \
  -H "Content-Type: application/json" \
  -d '{
    "model": "chriswritescode/Qwen3-235B-A22B-Instruct-2507-INT4-W4A16",
    "dtype": "auto",
    "additional_args": ["--tensor-parallel-size", "4", "--max-model-len", "240000"]
  }'
```

Any baked default can be overridden by including it in `additional_args`.

### Python environment (client side)

```bash
cd gonka/mlnode/packages/benchmarks
PYTHONPATH="src:../common/src:$PYTHONPATH" python scripts/<script>.py <args>
```

---

## Pipeline at a Glance

```
                         ┌──────────────────────────────────────┐
                         │  Target server (GPU machine)         │
                         │                                      │
                         │  mlnode API (:8080)                  │
                         │    ├─ vLLM backend A (:5001)         │
                         │    └─ vLLM backend B (:5002)         │
                         └──────────────┬───────────────────────┘
                                        │
              ┌─────────────────────────┼─────────────────────────┐
              │                         │                         │
     PoC Collection              Inference Collection       Revalidation
              │                         │                         │
   collect_data.py              inference.py              validation.py
              │                         │                         │
              ▼                         ▼                         ▼
         data/experiments/<exp>__<timestamp>/
         ├─ poc_config.json        ├─ inference_config.json
         ├─ poc_artifacts.json     ├─ inference_results.jsonl
         │                         ├─ validation_config.json
         │                         └─ validation_results.jsonl
              │                         │
              └────────┬────────────────┘
                       │
                   Analysis
                       │
         ┌─────────────┼─────────────┐
         │                           │
  poc_l2_histogram.py    inference_length_vs_distance.py
         │                           │
         ▼                           ▼
    data/plots/                 data/plots/
```

---

## 1. Inference Validation

### Step 1: Run Inference

```bash
python scripts/inference_validation/inference.py \
  --exp-name qwen235b_fp8_h100 \
  --url http://<HOST>:<API_PORT> \
  --n-prompts 1000 \
  --multilingual --langs en ch hi ar sp
```

The `--url` should point to the **mlnode API** (port 8080), which proxies `/v1/*` requests to vLLM backends with least-connections load-balancing. Pointing directly at a single vLLM backend port (e.g., 5001) also works but bypasses load-balancing and only uses one backend.

| Flag | Default | Description |
|------|---------|-------------|
| `--exp-name` | `inference` | Experiment name prefix (when `--exp-dir` not set) |
| `--exp-dir` | (auto) | Write into existing experiment directory |
| `--url` | (required) | Server URL (mlnode API recommended, e.g. `http://HOST:8080`) |
| `--model` | auto-detect | Model id |
| `--n-prompts` | 1000 | Total prompts |
| `--multilingual` | off | Mixed-language prompts |
| `--langs` | `en` | Languages (e.g., `en ch hi ar sp`) |
| `--max-tokens` | 3000 | Max tokens per response |
| `--temperature` | 0.99 | Sampling temperature |
| `--seed` | 42 | Random seed |
| `--top-logprobs` | 5 | Top logprobs to collect |
| `--max-workers` | 64 | Concurrent requests |

**Output:** `data/experiments/<exp_name>__<ts>/inference_config.json` + `inference_results.jsonl`

Timing is recorded in `inference_config.json` under `performance`: `total_time_seconds`, `output_tokens_per_second`, `average_time_per_prompt_seconds`.

### Step 2: Run Validation

Re-send prompts with **enforced tokens** to a (potentially different) server.

```bash
python scripts/inference_validation/validation.py \
  --inference-artifact data/experiments/<exp>__<ts>/inference_results.jsonl \
  --validation-url http://<HOST>:<API_PORT>
```

To write into a different experiment directory (e.g., for revalidation):

```bash
python scripts/inference_validation/validation.py \
  --inference-artifact data/experiments/exp_a__<ts>/inference_results.jsonl \
  --validation-url http://<HOST>:<API_PORT> \
  --exp-dir data/experiments/exp_b__<ts>
```

| Flag | Default | Description |
|------|---------|-------------|
| `--inference-artifact` | (required) | Path to `inference_results.jsonl` |
| `--validation-url` | (required) | Server URL (mlnode API recommended, e.g. `http://HOST:8080`) |
| `--exp-dir` | (artifact's parent) | Experiment directory to write into |
| `--validation-model` | auto-detect | Model id on validation server |
| `--artifact-tag` | (none) | Suffix for filenames |
| `--max-workers` | 64 | Concurrent requests |

**Output:** `validation_config.json` + `validation_results.jsonl` in the experiment directory.

`validation_config.json` records `source_inference_artifact` — the path to the inference data that was revalidated.

### Step 3: Analyze

```bash
# Explicit honest/fraud experiment dirs:
python scripts/analysis/inference_length_vs_distance.py \
  --honest data/experiments/exp_honest__<ts> \
  --fraud data/experiments/exp_fraud__<ts> \
  --find-bounds

# Auto-detect from data/experiments/:
python scripts/analysis/inference_length_vs_distance.py
```

---

## 2. PoC Validation

### Collecting PoC data

`collect_data.py` starts continuous generation via `/api/v1/inference/pow/init/generate` through the **mlnode API** (port 8080), fanning out to **all healthy vLLM backends**.

```bash
python scripts/poc_validation/collect_data.py \
  --config my_config.json \
  --exp-name qwen235b_fp8_h100
```

**Config schema:**

```json
{
  "model": "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8",
  "seq_len": 1024,
  "k_dim": 12,
  "block_hash": "TEST_BLOCK",
  "public_key": "test_pub_keys",
  "block_height": 100,
  "batch_size": 32,
  "servers": {
    "my_server": "http://<HOST>:<API_PORT>"
  }
}
```

The `servers` URL must point to the **mlnode API port** (8080).

> **Standard PoC test parameters:** When running PoC manually (via `curl` or ad-hoc scripts), always use the canonical values from the config above: `seq_len: 1024`, `k_dim: 12`, `block_hash: "TEST_BLOCK"`, `public_key: "test_pub_keys"`, `block_height: 100`. Using different values (e.g., `seq_len: 16`) produces non-comparable results and invalid throughput numbers.
>
> **`batch_size`:** Use `32` across all GPU types. Requires `--max-num-batched-tokens 32768`.

**SSH tunnel for remote servers** — add to config when inbound ports are firewalled:

```json
{
  "ssh": { "host": "<SSH_HOST>", "port": 22, "user": "root", "key": "/path/to/key" }
}
```

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | `config.json` | Config JSON path |
| `--exp-name` | `poc_validation_stream` | Experiment name prefix (when `--exp-dir` not set) |
| `--exp-dir` | (auto) | Write into existing experiment directory |
| `--warmup-seconds` | 5 | Warmup before measurement |
| `--measurement-seconds` | 30 | Measurement window |
| `--receiver-port` | 9999 | Callback receiver port |
| `--callback-host` | 127.0.0.1 | Callback host |
| `--no-tunnel` | off | Disable SSH tunnel |

**Output:** `data/experiments/<exp_name>_<ts>/poc_config.json` + `poc_artifacts.json`

### Comparing runs

```bash
python scripts/analysis/poc_l2_histogram.py \
  --run-a data/experiments/<run_a> \
  --run-b data/experiments/<run_b>
```

---

## 3. Typical Workflows

### Cross-GPU honest comparison

1. Run `inference.py` against **server A** (e.g., H100) → experiment A.
2. Run `validation.py` against **server B** (e.g., A100) with `--inference-artifact` from experiment A → experiment B.
3. Analyze with `inference_length_vs_distance.py --honest <exp_b>`.

For PoC: `collect_data.py` against both servers → `poc_l2_histogram.py`.

### Fraud detection

1. Collect honest pair (FP8 on GPU-A → FP8 on GPU-B).
2. Collect fraud pair (INT4 on GPU-A → FP8 on GPU-B).
3. `--honest <honest_exp> --fraud <fraud_exp> --find-bounds`.

### Revalidation of existing artifacts

1. Have an existing `inference_results.jsonl` from any experiment.
2. Start model on a new server.
3. `validation.py --inference-artifact <path> --validation-url <new_server> --exp-dir <new_exp>`.

---

## 4. Remote Server Setup (Cloud)

### Port mapping

Cloud providers expose internal ports via random external ports:

| Internal Port | Service |
|---------------|---------|
| 22 | SSH |
| 5001 | vLLM backend |
| 8080 | mlnode API |

### Initial setup via SSH

```bash
ssh -i <key> -p <ssh_port> root@<host>
touch ~/.no_auto_tmux
ln -sf /lib/x86_64-linux-gnu/libcuda.so.1 /lib/x86_64-linux-gnu/libcuda.so
export LD_LIBRARY_PATH=/lib/x86_64-linux-gnu:$LD_LIBRARY_PATH
. /app/packages/api/.venv/bin/activate && \
  cd /app/packages && \
  python -m uvicorn api.app:app --host 0.0.0.0 --port 8080
```

> **CUDA Error 803 workaround:** If vLLM worker processes fail with `system has unsupported display driver / cuda driver combination`, make sure `export LD_LIBRARY_PATH=/lib/x86_64-linux-gnu:$LD_LIBRARY_PATH` is set **before** starting the server. This ensures the correct system CUDA libraries are found first.

> **Triton `cannot find -lcuda` fix:** If vLLM crashes during torch.compile with `subprocess.CalledProcessError` from Triton's `_build()`, the linker can't find `libcuda.so`. Create the missing symlink: `ln -sf /lib/x86_64-linux-gnu/libcuda.so.1 /lib/x86_64-linux-gnu/libcuda.so`. This is common on cloud containers where the versioned `.so.1` exists but the unversioned `.so` does not.

### Model pre-download

```bash
ssh -i <key> -p <ssh_port> root@<host> \
  ". /app/packages/api/.venv/bin/activate && huggingface-cli download <model_name>"
```

**Monitoring download progress:** the easiest way to watch the model download is `df -h ./` on the remote machine. Qwen3-235B-FP8 is ~230 GB; Qwen3-235B-INT4-W4A16 is ~120 GB. Watch the `Used` column grow until it plateaus.

```bash
ssh -i <key> -p <ssh_port> root@<host> "df -h ./"
```

### SSH reverse tunnel

When the remote server cannot reach the client (common with cloud):

```bash
ssh -i <key> -R 9999:127.0.0.1:9999 -p <ssh_port> root@<host> -N
```

Or add `"ssh"` block to `collect_data.py` config for automatic tunnel management.

---

## 5. Shared Validation Library (`src/validation/`)

| File | Role |
|------|------|
| `data.py` | Pydantic models: `PositionResult`, `Result`, `ModelInfo`, `RequestParams`, `ValidationItem`. JSONL I/O. |
| `utils.py` | API calls (`inference()`, `validation()`). `EnforcedTokens`. Distance metrics (`distance2`, `token_distance2`). |
| `analysis.py` | `process_data()`, `find_optimal_bounds_parallel()`, plotting. |
| `stats.py` | Distribution fitting (normal, gamma, lognorm, beta). KS-test selection. |
| `prompts.py` | Multilingual prompts from HuggingFace Alpaca datasets. |
| `runner.py` | Parallel `generate_and_validate` with `ThreadPoolExecutor`. |

---

## 6. Ideal Report

A full pipeline run produces enough data for this report. **Not all sections are required** — run only what you need and report accordingly.

### PoC Performance
- **nonces/min** — from `poc_artifacts.json` → `timing.nonces_per_min`
- Number of backends, warmup, measurement window

### Inference Performance
- **output tokens/sec** — from `inference_config.json` → `performance.output_tokens_per_second`
- Total time, number of prompts, avg time per prompt

### Inference Validation
- **mean / median / max distance** — from `inference_length_vs_distance.py` output
- Length-vs-distance scatter plot
- Whether it's self-validation (same server) or cross-GPU

### PoC Validation (when comparing two runs)
- **L2 distance histogram** — from `poc_l2_histogram.py`
- Percentile threshold, mean/max L2

### Summary table example

```
| Metric                  | Value         |
|-------------------------|---------------|
| PoC nonces/min          | 55,423        |
| Inference tokens/sec    | 2,686         |
| Validation mean dist    | 0.0073        |
| Validation max dist     | 0.0124        |
```

---

## 7. Important Notes

- **SSH key** for remote servers: use your SSH key for SSH tunnels and remote access.
- **Attention backend** must match across runs for meaningful PoC comparison. `FLASHINFER` is the baked default in vLLM v0.15.1.
- **Logprobs mode**: `processed_logprobs` is the baked default. vLLM v0.15.1 supports per-request overrides and auto-detects the correct mode for validation requests. See [Choosing the logprobs mode](#choosing-the-logprobs-mode).
- **Experiment naming**: use the same `--exp-name` for PoC and inference to correlate results.
- **Resumable PoC**: `collect_data.py --continue` resumes from latest run.
- **Timing**: both `inference_config.json` and `validation_config.json` include `performance` with `total_time_seconds`, `output_tokens_per_second`, etc.
