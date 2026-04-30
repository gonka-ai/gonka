---
name: mlnode-validate
description: Validate a deployed MLNode end-to-end against a known-honest reference for a specific model. The caller picks the MLNode URL and the target model; the skill ensures the model is downloaded, deploys it, measures full-system PoC throughput, and verifies a pre-computed honest PoC vector set is accepted under an L2 threshold. Produces a JSON + text report. Self-contained inside the gonka repo (no external code, no callback receiver).
---

# MLNode PoC v2 Validation

The caller decides what to test. This skill never picks a default MLNode and
never picks a default model. Both must be passed explicitly.

This file is the contract. The skill is implemented by two scripts under
`mlnode/packages/benchmarks/scripts/poc_validation/`. Do not read or edit
those scripts as part of running this skill -- run `validate.py` and
report what it prints.

## Two file types -- keep them straight

The skill works with two distinct artifact kinds. Do not conflate:

- **Golden reference** (committed in repo): one JSON per supported
  model under
  `mlnode/packages/benchmarks/scripts/poc_validation/artifacts/<sanitized model>.json`.
  Pre-computed by the project: contains the honest PoC vectors,
  canonical PoC params, consensus-default `additional_args`, and the
  consensus-default `stat_test` block. Read-only at validate time.
- **Per-run report** (produced on the caller's machine, NOT
  committed): one experiment directory per `validate.py` invocation
  under `mlnode/packages/benchmarks/data/experiments/<exp_name>_<ts>/`.
  Layout matches `mlnode/packages/benchmarks/scripts/README.md` --
  one experiment = one server + one model deployment. Each directory
  contains `validate_config.json` (inputs), `validate_report.json`
  (full structured result), and `validate_report.txt` (short text
  summary).

When this skill says "reference", it means the golden file. When it
says "report", it means the per-run output.

## Available golden references

The repo ships these references under
`mlnode/packages/benchmarks/scripts/poc_validation/artifacts/`. Each
one is keyed by model id; the auto-lookup `<sanitized model>.json`
picks one filename per model. Variants beyond the default require an
explicit `--reference <path>`.

| Model | Filename | Vectors | Deploy notes |
|-------|----------|---------|--------------|
| `Qwen/Qwen3-0.6B` | `qwen-qwen3-0.6b.json` | 32 | local dev / single GPU |
| `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` (default lookup) | `qwen-qwen3-235b-a22b-instruct-2507-fp8.json` | 32 | tp=4, FlashInfer baseline. Quick smoke test. |
| `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8` (extended) | `qwen-qwen3-235b-a22b-instruct-2507-fp8-deepgemm.json` | 2000 | tp=2, DeepGEMM MoE backend (`VLLM_USE_DEEP_GEMM=1`, `VLLM_MOE_USE_DEEP_GEMM=1`), recorded on 4xB200. Pass with `--reference`. |

For Qwen3-235B the same model id has two references, and they
exercise different code paths (tp-size + MoE backend). When
validating qwen235b, run `validate.py` **twice** -- once with the
default lookup, once with the deepgemm variant -- and report both
verdicts. Example:

```bash
# Run 1: default 32-nonce reference (FlashInfer, tp=4)
python3 mlnode/packages/benchmarks/scripts/poc_validation/validate.py \
    --mlnode-url "$MLNODE_URL" \
    --model     Qwen/Qwen3-235B-A22B-Instruct-2507-FP8

# Run 2: extended 2000-nonce reference (DeepGEMM, tp=2)
python3 mlnode/packages/benchmarks/scripts/poc_validation/validate.py \
    --mlnode-url "$MLNODE_URL" \
    --model     Qwen/Qwen3-235B-A22B-Instruct-2507-FP8 \
    --reference mlnode/packages/benchmarks/scripts/poc_validation/artifacts/qwen-qwen3-235b-a22b-instruct-2507-fp8-deepgemm.json
```

Each run lands in its own experiment directory, so reports do not
collide. If the caller is short on time and explicitly asks for a
quick check, the default-lookup run alone is acceptable; otherwise
both should be run.

## Required inputs

The caller MUST supply both:

- `MLNODE_URL` -- base URL of the MLNode under test (e.g.
  `http://1.2.3.4:8080`). No default.
- `MODEL` -- target HuggingFace model id, exactly as the MLNode
  would receive it (e.g. `Qwen/Qwen3-235B-A22B-Instruct-2507-FP8`,
  `Qwen/Qwen3-0.6B`). The full `org/repo` form. No default.

If either is missing, ask the caller for it before running anything.
A short reference like "qwen 0.6B" is NOT enough -- ask for the full
HF id. Do not pick a model from the available artifacts on the
caller's behalf.

### Where every other parameter comes from

Beyond `MLNODE_URL` and `MODEL`, **the caller does not need to pass
anything**. Every other parameter has a documented source:

- `seq_len`, `k_dim`, `block_hash`, `public_key`, `node_id`,
  `node_count` -- all read from the golden reference for `MODEL`.
  The reference pins the exact PoC inputs its vectors were computed
  under.
- vLLM `additional_args` -- the consensus-default deploy args for
  this model are saved in the reference's `additional_args` field
  and used as-is. The caller can pass extra flags only if they ask
  for them: `--tp-size` and `--max-model-len` add-or-update the
  reference baseline (the same flag's existing value is replaced,
  otherwise appended); `--extra-arg <token>` appends arbitrary
  tokens.
- `stat_test.dist_threshold` / `p_mismatch` / `fraud_threshold` --
  resolved per-key with provenance: server default (0.02 / 0.001 /
  0.01) is the floor; the reference's `stat_test` block (which
  mirrors the on-chain consensus params for this model) overrides;
  CLI flags (`--threshold`, `--p-mismatch`, `--fraud-threshold`) are
  the top. All three values are always sent to the server and shown
  in the report with their source.

## Optional inputs

The caller can override deploy / validation / sampling defaults. Only
pass these flags when the caller asks for them:

- Stat-test parameters. These three flags fill the `stat_test` block
  the MLNode runs (per-nonce L2 mismatch test + binomial fraud test).
  `--threshold` IS the `dist_threshold` field of that block, just
  named more concisely on the CLI. Defaults come from the artifact;
  any key not provided is filled by the MLNode server defaults.
  - `--threshold <float>` (e.g. `0.2`) -- the `dist_threshold` field.
    L2 distance above which a single nonce counts as a per-nonce
    mismatch. Default: artifact's `dist_threshold`.
  - `--p-mismatch <float>` (e.g. `0.001`) -- per-nonce probability of
    a benign mismatch under honest computation. Default: artifact's
    `stat_test.p_mismatch` if present, else server default.
  - `--fraud-threshold <float>` (e.g. `0.01`) -- `probability_honest`
    cutoff at which the server flags fraud. Default: artifact's
    `stat_test.fraud_threshold` if present, else server default.
- Deploy overrides (only pass when the caller explicitly asks; the
  artifact's `additional_args` already encode the consensus-default
  deploy config for this model):
  - `--dtype <auto|float16|bfloat16|fp8>` -- vLLM dtype.
  - `--tp-size <int>` -- add-or-update `--tensor-parallel-size`.
  - `--max-model-len <int>` -- add-or-update `--max-model-len`.
  - `--extra-arg <token>` -- pass once per token to append additional
    vLLM args. The caller owns avoiding conflicts with the artifact
    baseline.
- Phase skips. By default all four phases run; each flag drops one phase:
  - `--skip-download` -- the model is known to be cached already.
  - `--skip-deploy` -- vLLM is known to already be serving `MODEL`.
  - `--skip-throughput` -- skip the throughput measurement (faster runs).
  - `--skip-validate` -- skip the vector-validation step (deploy / measure only).
- Sampling: `--warmup-seconds <int>`, `--measure-seconds <int>`,
  `--sample-interval <int>`, `--batch-size <int>`.
- Timeouts (seconds): `--download-timeout <float>`,
  `--deploy-timeout <float>`, `--validation-timeout <float>`.
- Output location:
  - `--exp-dir <path>` -- write into this experiment directory.
  - `--exp-name <name>` -- prefix used to auto-create the experiment
    dir under `data/experiments/<exp-name>_<ts>/` (default:
    `mlnode_validate_<sanitized model>`).
  - `--reference <path>` -- override the golden reference path (for
    testing custom references). Default: looked up from `MODEL`.
    `--artifact` is accepted as a legacy alias.

## How to run

Bare minimum (recommended starting form):

```bash
python3 mlnode/packages/benchmarks/scripts/poc_validation/validate.py \
    --mlnode-url "$MLNODE_URL" \
    --model     "$MODEL"
```

Add overrides only when the caller asks for them. The deploy config the
caller sees is the artifact's `additional_args` plus any of `--dtype`,
`--tp-size`, `--max-model-len`, `--extra-arg` they passed; the caller
does not need to know what's in the artifact's baseline.

## What the script does, in order

The script prints `[i/4]` headers (1-indexed) as it progresses. The
agent should relay these headers to the caller while the run is in
flight so the caller can see which phase is slow.

1. `[1/4] download` -- ensures the requested HF repo is cached on the
   MLNode.
   - `POST /api/v1/models/status {hf_repo}` -- returns one of
     `DOWNLOADED | DOWNLOADING | NOT_FOUND | PARTIAL`.
   - If not `DOWNLOADED`, `POST /api/v1/models/download {hf_repo}` to
     start the download, then poll `/models/status` until `DOWNLOADED`.
   - Skipped with `--skip-download`. Hard-fails if the status flips
     back to `NOT_FOUND` mid-poll, or the timeout expires.

2. `[2/4] deploy` -- starts vLLM if it is not already running.
   - `POST /api/v1/inference/up/async {model, dtype, additional_args}`.
   - Polls `GET /api/v1/inference/up/status` until `is_running == true`
     or `status` becomes `failed`/`cancelled`. Server-side `elapsed_seconds`
     is printed on each poll so the caller sees progress.
   - Skipped with `--skip-deploy`; if so, the script verifies vLLM is
     already running and aborts otherwise.

3. `[3/4] throughput` -- measures full-system PoC throughput.
   - `POST /api/v1/inference/pow/init/generate {block_hash, ..., params}`
     (the artifact's params); the proxy fans out to every healthy vLLM
     replica with a different `group_id` so they process disjoint
     nonces. The first response reports `backends` and `n_groups`.
   - After `--warmup-seconds`, samples
     `GET /api/v1/inference/pow/status` every `--sample-interval` for
     `--measure-seconds`. Reports per-replica `nonces_per_second` and
     the sum across replicas (the system-level throughput for this
     model). Then `POST /api/v1/inference/pow/stop`.
   - Skipped with `--skip-throughput`.

4. `[4/4] validate` -- verifies pre-computed honest vectors.
   - `POST /api/v1/inference/pow/generate` with `wait=true`,
     `nonces=[...]`, `validation.artifacts=<artifact>`, and the full
     `stat_test` block (`dist_threshold`, `p_mismatch`,
     `fraud_threshold`). The MLNode recomputes the same nonces,
     compares them to the supplied vectors under L2 (per-nonce
     mismatch test), then runs a binomial fraud test using
     `p_mismatch` and `fraud_threshold`. Returns
     `{n_total, n_mismatch, mismatch_nonces, p_value, fraud_detected}`.
   - The pass criterion is the binomial test alone: `fraud_detected ==
     false`. A non-zero `n_mismatch` with `fraud_detected == false`
     means the MLNode produced a few mismatches but they fall within
     the statistical tolerance the test allows; this is still a PASS,
     and the script labels it `PASS (with mismatches within stat-test
     tolerance)`.
   - Skipped with `--skip-validate`.

After the four phases, the script writes three files into the
experiment directory (`mlnode/packages/benchmarks/data/experiments/<exp_name>_<ts>/`,
matching the layout in `mlnode/packages/benchmarks/scripts/README.md`):

- `validate_config.json` -- the resolved inputs only (mlnode URL,
  model, reference path + meta, deploy config, PoC params,
  stat_test with provenance, raw CLI args). Lets a future reader
  reproduce the run without re-deriving any defaults.
- `validate_report.json` -- the full structured report (config +
  per-phase results + verdict). This is the audit trail.
- `validate_report.txt` -- short human-readable summary; first line
  after the banner is `verdict: <PASS|FAIL|...>` for at-a-glance
  reading.

## How to report back to the caller

After the script returns, read `<exp_dir>/validate_report.json` and
emit a summary using the template below. The report exposes
everything you need; do NOT abbreviate it down to a one-liner -- the
caller invokes this skill to know exactly what was tested and
against what.

**Required template** (every section is mandatory; mark a field N/A
only when its phase was skipped):

```
verdict: <PASS | PASS-with-mismatches | FAIL | validation skipped>

target
  mlnode:    <mlnode_url>
  model:     <model>
  reference: <reference.path>
             <reference.n_vectors pre-computed nonces; source: <reference.source>>

PoC params (from reference)
  seq_len=<>, k_dim=<>, block_hash=<>, public_key=<>, node_id=<>, node_count=<>
  throughput_batch_size=<poc_params.throughput_batch_size> (cli)
  validation_batch_size=<poc_params.validation_batch_size> (cli)

deploy config (reference.additional_args + CLI overrides)
  dtype=<>, additional_args=<list>
  applied? <yes if deploy.action=="deployed", "no - vLLM was already running" otherwise>

stat test (server-default -> reference -> cli, with provenance per key)
  dist_threshold=<v> (<source>)
  p_mismatch=<v> (<source>)
  fraud_threshold=<v> (<source>)

phases
  [download]    <action> in <elapsed>s   OR  skipped
  [deploy]      <action> in <elapsed>s   OR  skipped
  [throughput]  backends=<n>  avg_sum=<v> nonces/s  min/max=<a>/<b>
                per-backend (last sample): [<...>]   (only if n_backends > 1)
  [validate]    n_total=<>, n_mismatch=<>, p_value=<>, fraud_detected=<>
                mismatch_nonces=<list, truncated to 20>   (only if non-empty)

experiment dir: <exp_dir>
  config (json):  <exp_dir>/validate_config.json
  report (json):  <exp_dir>/validate_report.json
  report (txt):   <exp_dir>/validate_report.txt
```

**Why every section matters**: the caller asked the skill to deploy
and validate a model. They cannot judge the result without seeing
which `additional_args` actually shaped that deployment, which
`stat_test` triple the verdict was judged under, or which
`block_hash`/`public_key`/`seq_len`/`k_dim` the PoC was computed with
-- those four together define the test. The script writes them all
into the report; surface them.

If `deploy.action == "already_running"`, say so explicitly and warn
that the listed `additional_args` were NOT applied to the live vLLM
process -- they are what the script *would* have sent. Caller may
need to restart vLLM to actually exercise those flags.

If one replica's `nonces_per_second` is significantly below the
others (e.g. 50% of median), call it out. Otherwise the per-backend
list is informational.

## Pass criteria

The MLNode runs the binomial fraud test internally and returns
`fraud_detected`. PASS is defined by that single field; the per-nonce
`n_mismatch` count is informational. There are three outcomes the
agent must distinguish and surface:

- Clean PASS -- `validation.passed == true`,
  `validation.has_mismatches == false`,
  `validation.response.n_mismatch == 0`,
  `validation.response.fraud_detected == false`.
- PASS with mismatches within stat-test tolerance --
  `validation.passed == true`,
  `validation.has_mismatches == true`,
  `validation.response.n_mismatch > 0`,
  `validation.response.fraud_detected == false`.
  The fraud test allows up to a few mismatches per `p_mismatch`. This
  is still a PASS; surface `n_mismatch`, `mismatch_nonces`, `p_value`,
  and the `stat_test` parameters that were used so the caller can see
  the test was met. Do not call this a failure.
- FAIL -- `validation.passed == false`,
  `validation.response.fraud_detected == true`. Surface
  `n_mismatch`, `mismatch_nonces`, `p_value`, and the `stat_test`
  block. Do not retry.

Exit code is the authoritative signal:

- `0` -- PASS (with or without mismatches inside tolerance), or the
  validate phase was skipped so there is no fraud verdict to fail on.
- `2` -- validation ran and the fraud test fired (`fraud_detected:
  true`).
- `1` -- hard error before validation could run (download failed,
  deploy timed out, etc.). Surface the script's last `ERROR:` line.

## When no artifact exists for the requested model

`validate.py` looks up the artifact under
`mlnode/packages/benchmarks/scripts/poc_validation/artifacts/`. If the
file for `MODEL` is missing, the script exits `1` and prints:

- which model was requested,
- the expected artifact filename,
- the available artifacts in that directory,
- the exact `make_artifact.py` command to bake one.

The agent must NOT invent vectors and MUST NOT pick a different model
to substitute. Stop, surface the script's message, and either:

- ask the caller to choose from the printed list of available artifacts, or
- bake a new artifact against a trusted MLNode that already serves
  `MODEL` (canonical precision, attention backend, GPU class):

  ```bash
  python3 mlnode/packages/benchmarks/scripts/poc_validation/make_artifact.py \
      --mlnode-url "$TRUSTED_MLNODE_URL" \
      --model     "$MODEL" \
      --num-nonces 32 --batch-size 32 \
      --out mlnode/packages/benchmarks/scripts/poc_validation/artifacts/<filename printed by validate.py>
  ```

  `make_artifact.py` does not deploy; the trusted MLNode must already
  be serving `MODEL`. It pulls vectors via
  `POST /api/v1/inference/pow/generate` with `wait=true` (no callback
  receiver, no SSH tunnel). After the artifact lands, re-run the
  original `validate.py` command.

## Failure modes the agent surfaces verbatim

- Download phase: `model download did not complete in <N>s` or
  `model download failed (status went to NOT_FOUND)`. Likely
  HF-rate-limited, no internet, or wrong `hf_repo`. Stop.
- Deploy phase: `vLLM did not become ready within <N>s` or
  `vLLM startup failed: {status: failed, error: ...}`. The deploy
  config does not match the GPU (OOM, FP8 on non-FP8 capable GPU,
  wrong tp-size, kernel mismatch). Surface the last status object;
  do not silently retry with different flags.
- Throughput phase reports `backends=0` or `agg_status: NO_BACKENDS` --
  the proxy never registered healthy vLLM ports. The deploy probably
  crashed; check the MLNode container logs. Stop.
- Validation `fraud_detected: true` -- the deployment under test
  produces different vectors than the reference at a rate the binomial
  test rejects. Report `n_mismatch`, `mismatch_nonces`, `p_value`, and
  the `stat_test` block. Do not guess the cause.
- Validation `n_mismatch > 0` with `fraud_detected: false` -- this is
  a PASS, not a failure. Mention the count and `mismatch_nonces` so
  the caller is aware, but do not treat it as an error condition.

## Notes

- Throughput is the sum of per-replica `nonces_per_second`. With N vLLM
  replicas (e.g. 8 H100 + tp_size=4 -> 2 replicas), each replica
  processes a different nonce group, so the system rate for the model
  is the sum.
- No callback receiver, SSH tunnel, or open inbound port is required at
  any phase. Throughput uses
  `GET /api/v1/inference/pow/status` (server-side counters); validation
  and artifact baking use `POST /api/v1/inference/pow/generate` with
  `wait=true`, which returns artifacts inline.
- Both `validate.py` and `make_artifact.py` accept `--help` for the
  full flag surface. This file lists only the inputs the skill exposes
  to the caller.
