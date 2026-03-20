# Inference + Validation Scripts

This folder contains a 2-step benchmark flow for an **already running OpenAI-compatible vLLM server**.

The flow is split into:

1. `inference.py` - run **inference only** and save a pure inference artifact.
2. `validation.py` - take inference artifact, run **validation only**, and save combined results.

## What These Scripts Do

### `inference.py`

- Requires `--exp-name`.
- Connects to an already running vLLM endpoint (`--url`).
- Waits for server readiness via `/v1/models`.
- Resolves model id from served models (or uses `--model` if provided and available).
- Runs inference on prompts and stores:
  - pure inference rows (`inference_results.jsonl`)
  - inference config + vLLM runtime probe (`inference_config.json`)

### `validation.py`

- Reads `inference_results.jsonl` from an existing experiment folder.
- Connects to validation vLLM endpoint (`--validation-url`).
- Re-runs validation with enforced tokens from stored inference results.
- Saves:
  - combined inference+validation rows (`inference_validation_results.jsonl`)
  - validation config + runtime probe (`validation_config.json`)
- Compares inference config/runtime fields with validation config/runtime fields.
  - If different, prints warnings and continues.
  - Execution is not interrupted.

## Artifact Structure

Artifacts are created under:

`benchmarks/data/inference_validation/<exp_name>__<YYYY-MM-DD_HHMMSS>/`

Each experiment folder contains:

- `inference_config.json`
- `validation_config.json`
- `inference_results.jsonl`
- `inference_validation_results.jsonl`

## JSONL Row Schemas

### `inference_results.jsonl`

Each line includes:

- `prompt`
- `language`
- `inference_result`
- `inference_model`
- `request_params`
- `metadata`

### `inference_validation_results.jsonl`

Each line includes:

- `prompt`
- `inference_result`
- `validation_result`
- `inference_model`
- `validation_model`
- `request_params`

## Usage

Run from repository root (or adjust paths).

### 1) Run Inference

```bash
python3 mlnode/packages/benchmarks/scripts/inference_validation/inference.py \
  --exp-name my_exp \
  --url http://HOST:8000
```

Optional useful flags:

- `--model <model_id>`
- `--n-prompts 1000`
- `--prompts-file /path/to/prompts.txt` (one prompt per line)
- `--max-workers 64`
- `--max-tokens 3000`
- `--temperature 0.99`
- `--seed 42`
- `--top-logprobs 5`
- `--wait-timeout-s 120`

### 2) Run Validation

```bash
python3 mlnode/packages/benchmarks/scripts/inference_validation/validation.py \
  --inference-artifact mlnode/packages/benchmarks/data/inference_validation/my_exp__YYYY-MM-DD_HHMMSS/inference_results.jsonl \
  --validation-url http://HOST:8000
```

Optional useful flags:

- `--validation-model <model_id>`
- `--max-workers 64`
- `--wait-timeout-s 120`
- `--max-attempts 3`
- `--retry-backoff-start-s 1.0`
- `--retry-backoff-mult 2.0`

## Notes

- These scripts assume vLLM server is already up; they do not start ML node or deploy services.
- `validation.py` uses request params stored in the inference artifact.
- If validation text differs from inference text for a prompt, a warning is printed and the row is still saved.
