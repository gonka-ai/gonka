# GLM-5.3-Flash GPU profile

This document records the release inputs for
[`zai-org/GLM-5.3-Flash`](https://huggingface.co/zai-org/GLM-5.3-Flash) at revision
`04c4e9e95c5da8862dced7e5056455116f83a7e0`.

The model is `Glm5NextForConditionalGeneration` — a KDA linear-attention and NoPE sparse-MLA
hybrid, FP8 `e4m3` with dynamic activation scaling, 305.8 GiB over 62 shards. It requires
vLLM 0.28; no released 0.25.x build can load it.

Nonce rates are measurements, not admission limits or calibrated chain weights. This PR
records release inputs and evidence; it does not activate the model.

## Acknowledgements

The hardware-profile, cross-hardware L2 and inference-validation experiments were conducted by
the [Kaitaku team](https://github.com/kaitakuai/experiments/tree/main/2026-09). Thank you for
the research and the reproducible evidence that made this integration possible.

## Planned chain parameters

| parameter | value |
|---|---|
| `PoCModelConfig.seq_len` | 1024 |
| `PoCModelConfig.weight_scale_factor` | **0.62** |
| `stat_test.dist_threshold` | **0.44** |
| `stat_test.p_mismatch` | 0.10 |
| `stat_test.p_value_threshold` | 0.05 |
| `Model.validation_threshold` | **0.951** |
| `ValidationParams.logprobs_mode` | `processed_logprobs` — unchanged |

## PoC gate: why 0.44

Six honest arms and three fraud arms were measured pairwise on three shared seeds
(1000 nonces per seed, so 3000 L2 samples per pair, 63 000 in total). At a 0.44 gate:

| comparison | past the gate |
|---|---:|
| honest vs itself, same box | 0 % (B200, H100) · 0.1 % (H200) |
| **honest vs honest, different GPU generation** | **up to 11.7 %** |
| **fraud NVFP4 vs honest** | **31.7 % – 97 %** |
| **fraud REAP50 vs honest** | **90 %+** |

The worst honest pair sits at 11.7 % and the mildest fraud pair at 31.7 % — a 2.7× margin with
nothing in between. At the 0.40 gate the honest pairs reach 16–17 %, which does not clear
`p_mismatch = 0.10` and convicts a healthy mixed fleet; 0.44 is the smallest round threshold
that restores headroom without letting any fraud arm through.

Running the one-sided binomial test `P(X ≥ k | n, p_mismatch = 0.10)` against `α = 0.05`:

| validation sample `n` | honest pairs clean | fraud pairs caught |
|---:|---|---:|
| 64 | yes | 15 / 15 |
| 250 | yes | 15 / 15 |
| 640 | yes | 15 / 15 |
| 800 | yes | 15 / 15 |
| 1000 | **no — 2 of 6 convicted at p ≈ 0.043** | 15 / 15 |

**Constraint to carry into the parameter proposal:** with `p_mismatch = 0.10` this gate is safe
for validation samples up to about 800 nonces. If `validation_sample_size` is ever raised to
1000, `p_mismatch` must go to 0.12 or above; the fraud side is caught 15/15 at every value
tested up to 0.20, so the tolerance can be widened without weakening detection.

## Inference gate: why 0.951

1000 multilingual prompts were generated on 2×B300 and replayed on 4×H200 for both the honest
model and `LibertAIDAI/GLM-5.3-Flash-NVFP4`, in both logprobs modes — 4000 generations, 4000
replays, 0 length mismatches. Measured `distance2`:

| mode | honest mean | NVFP4 mean | × floor | best F1 | TP @ FP 5 % |
|---|---:|---:|---:|---:|---:|
| processed | 0.024969 | 0.054328 | 2.18× | 0.918 | 88.5 % |
| raw | 0.029738 | 0.076863 | 2.58× | 0.998 | 99.8 % |

A `validation_threshold` of τ rejects an answer whose distance reaches 1 − τ. At τ = 0.951
(cut 0.049), over those same 1000-answer distributions:

| mode | honest rejected | fraud caught |
|---|---:|---:|
| **processed** | **0.5 %** | **64.2 %** |
| raw | 0.0 % | 99.5 % |

The chain runs `processed_logprobs` and this proposal keeps it there. The operating point is
therefore 0.5 % honest loss for 64.2 % detection of an NVFP4 substitution on a single replay.
Detection compounds across replays — a node serving the quantised build is sampled repeatedly,
not once — while the 0.5 % honest loss is the cost paid per validation, so the asymmetry is
the right way round for a single-replay gate.

For reference if the threshold is ever revisited: within `processed`, τ = 0.955 gives 1.5 % /
75.1 % and τ = 0.960 gives 4.5 % / 85.6 %. Honest loss climbs faster than detection past that.

### Logprobs mode is a chain parameter, not a node flag

The node profiles below deliberately do not pass a server-wide `--logprobs-mode`. Validation
requests carry the mode per request, and vLLM auto-detects it from `enforced_tokens` when they
do not (`vllm/entrypoints/openai/chat_completion/serving.py`). What decides is
`ValidationParams.logprobs_mode` in `inference-chain/x/inference/types/params.go`, which is
`processed_logprobs` and stays that way here.

## Hardware profiles

| GPU profile | TP | GPU memory | Batched tokens | PoC batch | Measured nonce/min |
|---|---:|---:|---:|---:|---:|
| 2× B300 | 2 | 0.90 | 65536 | 32 | 2030 |
| 4× B200 | 4 | 0.90 | 65536 | 32 | 2727 |
| 4× H200 | 4 | 0.90 | 16384 | 16 | 1439 |
| 8× H100 | 8 | 0.95 | 16384 | 8 | 448 (PCIe, this image); 1612 (Kaitaku, SXM, batch 16 — see below) |

The matching files are `deploy/join/node-config-glm53flash-*.json`. The PoC batch is not in
those files — it is the MLNode environment variable `POC_BATCH_SIZE_DEFAULT`, set in the join
`.env`: **8 on 8×H100, 16 on H200 (both profiles), 32 on B200 and B300.** Mining and
validation both use it. All of them pin the model
revision above, set FP8 KV cache, `--block-size 2304` and `--max-num-seqs 256`, and disable
FlashInfer autotune. MLNode injects the PoC worker extension into every vLLM launch.

`node-config-glm53flash-8xH200.json` is the two-container layout for a single 8×GPU H200 host:
two MLNodes at TP=4 rather than one at TP=8.

### Two limits found while verifying 3.1.0

* **Do not run GLM with a PoC batch below ~12.** The validation KV lease is sized as
  `nonces × max over cache groups of ceil(seq_len / block_size)`, and GLM's sparse-MLA indexer
  group (`KpoolTailSpec`) has a 4-token block, so a lease needs 256 pool blocks per nonce. At
  batch 16 or 32 that exceeds every pool measured and validation falls back to the in-place
  path, which is fine. At batch 8 on 4×H200 the lease succeeds, the borrowed layout is wrong
  for that group, and every GPU faults with XID 31. The H200 and H100 profiles use 16; keep
  `POC_BATCH_SIZE_DEFAULT` there. The lease sizing and the borrowed layout for small-block
  groups are open plugin bugs, separate from #10. gonka-poc 0.1.6 (gonka-ai/gonka-vllm-plugins#11)
  therefore reports `poc_validation_inference: false` for `model_type glm5_next` and never
  attempts a lease there: GLM nodes abort live inference during validation, and the gateway
  routes accordingly. Other models are unchanged.
* **8×H100 TP=8: batch 8 and `--max-num-batched-tokens 16384`, not 16 / 65536.** At TP=8 a
  16 384-token forward faults in the FlashInfer sparse-MLA sm90 kernel
  (`BatchMLAPagedAttentionSM90Run`, XID 31 on every GPU) — PoC at batch 16 and, on the SXM
  host, plain inference with sixteen 1.1k-token prompts alike; an 8 192-token forward does not.
  Reproduced on an 8×H100 SXM VM (drivers 580.173 and 595.71, fp8 and bf16 KV, custom
  all-reduce on and off, a fresh FlashInfer JIT cache, Kaitaku's own image) and on 8×H100 PCIe
  (driver 595.84). The profile therefore runs PoC at batch 8 and caps prefill steps at 16 384
  tokens: on 8×H100 PCIe with this image that gives 448 nonce/min, two PoC start/stop cycles
  with a correct control prompt after each, and sixteen concurrent 1.1k-token prompts served
  without a fault. Kaitaku's 1612 nonce/min at batch 16 on their SXM host is not reproduced
  here; the difference is still open. PCIe all-reduce is the throughput bottleneck at TP=8, so
  an SXM host with the same settings should land well above 448.
* **NVSwitch hosts inside VMs need `NCCL_NVLS_ENABLE=0`.** On the 8×H200 VM the engine failed
  to start with `NCCL error: unhandled cuda error` — NVLS multicast allocation returned CUDA
  401 after `NV_ERR_FABRIC_STATE_OUT_OF_SYNC` in `dmesg`. `NCCL_NVLS_ENABLE=0` (with
  `NCCL_CUMEM_ENABLE=0`) in the MLNode environment fixes it; Kaitaku record the same for
  4×B200 in a container.

### The batched-tokens constraint is load-bearing

**`--max-num-batched-tokens` must be at least `PoC batch × 1024`.** The PoC forward builds
`batch × 1024` tokens in one pass. Below that budget the run does not degrade — it yields zero
nonces, and on some arms it takes the engine with it:

* 4×B200 at batch 32 with the shipped 16384: `RuntimeError: The size of tensor a (16384) must
  match the size of tensor b (32768)`. The plugin catches it, deactivates the gate and resets
  the prefix cache, so the engine survives. Raising the budget to 65536 makes batch 32 work.
* 4×H200 at batch 24 with 16384: `CUDA_ERROR_ILLEGAL_ADDRESS` on all four GPUs and XID 31 —
  the engine is dead and needs a restart. This is why the H200 profile keeps batch 16 and the
  16384 budget rather than raising both; the larger batch was never shown to be safe here, and
  PoC is compute-bound at this size anyway (batch 8 and batch 16 both give 1439 nonce/min).
* Batch 48 is a genuine kernel limit, not a budget one: it collapses on 2×B300 even at 131072,
  and dies in the Triton sparse-MLA indexer on 4×B200.
* 8×H100 needs `--gpu-memory-utilization 0.95` to start at all, and batch 32 there is an OOM,
  not a kernel fault.

### First-in-batch instability — found and fixed in gonka-poc 0.1.5

On 4×H200 and 4×B200, two honest runs of the same seed differed in 63 of 1000 nonces — exactly
the positions where `index % 16 == 0`, the first sequence of each collection batch — and in the
chain data every validator disagreed with every miner on 100 % of those positions, at L2 1.0–1.9.

The cause is in the plugin, not the model. The legacy in-place layout put the first PoC
sequence of every batch on KV block 0, which vLLM reserves as the null block
(`BlockPool.null_block`, `NULL_BLOCK_ID == 0`) and hands out as padding. The `causal_conv1d`
kernel in front of every KDA layer treats state index 0 as padding and returns without computing
that sequence, so it went through the linear-attention layers with whatever the output buffer
held: NaN on some hosts (dropped silently by the NaN filter — 120 of 128 nonces came back per
run on 4×H200), garbage on others. Leased validation never receives block 0, so the miner's
artifact never reproduced. DeepSeek-V4-Flash has no conv state and shows none of this.

gonka-poc 0.1.5 (gonka-ai/gonka-vllm-plugins#10) starts the in-place layout at block 1 and
also drops the KV-scratch input buffer that aliased PoC inputs onto KV-cache memory on
bf16-KV nodes. Measured on 4×H200 TP=4 with the H200 profile, 128 nonces at batch 16:

| run | 0.1.4 | 0.1.5 |
|---|---:|---:|
| nonces returned | 120 / 128, `NaN in 1/16 hidden states` every batch | 128 / 128 |
| repeat, same batches | — | 0 of 128 differ (bit-exact) |
| same nonces, batches shifted by 1 / 5 / 9 | — | 2.4 % / 4.9 % / 3.4 % past the 0.44 gate, no position special |

What remains is batch-composition dependence: the same nonce computed in a different batch
differs by a median L2 of 0.23 at every position (MoE and KDA kernels tile by batch), which is
the flat 5–7 % floor seen between honest nodes in the chain data. Under the chain's random
200-nonce sample that gives k ≈ 14 against a conviction line of 28, so the parameters above
stay as proposed. Before the fix the first-in-batch garbage added 1/16 of positions on top and
pushed honest GLM nodes to ≈15 %.

The golden reference artifact
(`mlnode/packages/benchmarks/scripts/poc_validation/artifacts/zai-org-glm-5.3-flash.json`) is
re-baked with 0.1.5 on 4×H200 TP=4 at batch 16, same seed and public key. A reference is tied
to the topology and batch it was baked with: on the same topology at the node's default batch
it reproduces bit-exactly (0/200 on 8×H100 PCIe against a batch-8 bake on the same host), on
another topology it shows the honest floor (5.5 %, p = 0.99, 8×H100 PCIe against the H200
reference).
`make_artifact.py` and `validate.py` therefore leave the batch unset so the node's
`POC_BATCH_SIZE_DEFAULT` is used, and the bake batch is recorded in the artifact. Against the previous 8×H100
0.1.4 set: the 63 first-in-batch positions differ 100 % (median L2 1.15 — the garbage), the
other 937 differ 5.7 % (median 0.24 — the honest cross-topology floor); the previously
documented "11.7 % honest cross-generation floor" was 6.3 points of that garbage plus 5.4
points of real drift.

## Fraud arms and what the gates do to them

| arm | what it is | PoC at 0.44 | inference at τ 0.951, processed |
|---|---|---:|---:|
| `LibertAIDAI/GLM-5.3-Flash-NVFP4` | NVFP4 quantisation, 181.3 GiB | 31.7 % – 97 % past gate | 64.2 % caught |
| `patrickbdevaney/GLM-5.3-Flash-REAP50-FP8` | 50 % expert pruning | 90 %+ past gate | not measured |

The economic shape is the familiar one: the honest checkpoint needs 305.8 GiB and does not fit
a single B300 (~242 GiB usable at `gmu 0.90`), while the NVFP4 build needs 181.3 GiB and does.
The PoC gate is the stronger of the two against this substitution and catches it on every arm
measured; the inference gate is the corroborating signal.

## MLNode image

```
ghcr.io/gonka-ai/mlnode:3.1.0-vllm-0.28.0
ghcr.io/gonka-ai/mlnode@sha256:25cccf7d9954678550e47a1f09f12d3db140803e9cd6c289f3af25d34ceabda0
```

3.1.0 is 3.0.17 with gonka-poc `0.1.6` (gonka-ai/gonka-vllm-plugins#10 and #11): the same layers with
one added on top, so every layer of 3.0.17 is reused verbatim. Its vLLM base is likewise
`ghcr.io/gonka-ai/vllm:v0.28.0-glm53-poc-v2-cu13-hopper-blackwell`
(`sha256:e2cd12021cfcac12b0b9db4c18914d80dd34794289b4729e75e00847f3c20830`) — the
`v0.28.0-glm53-poc-cu13-hopper-blackwell` base plus the 0.1.6 plugin layer; gonka-ai/vllm#109
pins that version in `docker/Dockerfile.gonka-poc` so a from-scratch build produces the same
tree. Building `mlnode/packages/api/Dockerfile` against the v2 base with
`MLNODE_RELEASE_VERSION=3.1.0` and `EXPECTED_VLLM_VERSION=0.28.0` is equivalent; the one MLNode
API change in this PR (the `/versions` capability cache is keyed by loaded model, since
gonka-poc 0.1.6 reports it per model) is carried as a layer on top. The image
carries vLLM `0.28.0.dev0+glm53.gonka.sampler1`, gonka-poc `0.1.6`, FlashInfer `0.6.18` and
torch `2.13.0+cu130`. The previous image, `3.0.17-vllm-0.28.0`
(`sha256:6772abdf736bbe8cad27d8c305e1fa32b54c82f783286d405fc5171d06419081`), differs only by
the plugin and must not be used for GLM: its first-in-batch artifacts never validate.

`Glm5NextProcessor.from_pretrained` read `processor_config.json` with a bare `open()`, so a
launch with the Hugging Face id — the path MLNode takes — failed before the engine started.
Fixed in gonka-ai/vllm#108 and included in the image above.

**Pin the upstream base by digest.** `vllm/vllm-openai:glm53-flash` is a mutable tag and moved
from `0.1.dev20051+g487ecf187` to `0.28.1rc1.dev580+g385dce36b` within two days, breaking the
build with no change on our side. The version guard in `docker/Dockerfile.gonka-poc` caught it
rather than silently overlaying our residual patches onto a different upstream tree. This
image was built against
`vllm/vllm-openai@sha256:2c6da6c6f16ed15c91e412d896dba13701f25fe1861eaec9ddaa4db34d1d21c4`.

Three changes to `mlnode/packages/api/Dockerfile` were required for the 0.28 base and are
included here. The base image ships `wheel` as a distro package with no RECORD file, so the
combined `pip install --upgrade` could not uninstall it; it is now two calls, with
`--ignore-installed` for `wheel`. The vLLM version guard was hardcoded to `0.25.1` and is now
`ARG EXPECTED_VLLM_VERSION`, defaulting to `0.25.1` so existing builds are unaffected. The
OpenSSL fetch now retries and accepts `ARG OPENSSL_URL`, which allows a local mirror on hosts
with an unreliable link. Cloning the repository for a build also requires
`git submodule update --init --recursive`, otherwise Poetry cannot resolve the `gorilla` path
dependency.

### Verified on this image

All three currently integrated models were started concurrently on disjoint GPUs and taken
through a full cycle of PoC start, PoC stop, and inference — twice — on both a Blackwell and a
Hopper host. Hopper is not redundant here: the sm90 kernels, FlashAttention 3 among them, are
never exercised on Blackwell.

| Model | TP | 8×B200, cycle 1 / 2 | 8×H200, cycle 1 / 2 |
|---|---:|---:|---:|
| DeepSeek-V4-Flash-0731 | 2 | 2572.6 / 2534.2 | 998.3 / 998.3 |
| MiniMax-M2.7 | 2 | 1651.1 / 1651.1 | 1459.0 / 1459.0 |
| GLM-5.3-Flash | 4 | 2726.2 / 2726.2 | 1497.4 / 1497.4 |

Nonce rates are per instance in nonce/min at PoC batch 16. Every cycle answered the control
prompt correctly and separated reasoning into `message.reasoning`. The second PoC run matches
the first almost exactly, so stopping PoC leaves no state that slows the next start.

Startup through the stock `entrypoint.sh` was checked separately, since that is the path the
join compose uses and the one that failed before #1751: the container reaches
`/api/v1/state`, which reports the release version, with no `useradd` in the logs.

The table above was recorded on 3.0.17. On 3.1.0 the same cycle — PoC start, 45–90 s warm-up,
120 s measured, PoC stop, control prompt, twice — was run on one 8×H200 host with each model's
H200 profile (MLNode fans out to 8/TP instances):

| Model | TP × inst. | cycle 1 / 2, nonce/min per instance | prompt after each stop |
|---|---:|---:|---|
| DeepSeek-V4-Flash-0731 | 2 × 4 | 1038 / 1042 | correct, reasoning separated |
| MiniMax-M2.7 | 2 × 4 | 1106 / 1478 | correct, reasoning separated |
| GLM-5.3-Flash | 4 × 2 | 1524 / 1528 | correct, reasoning separated |

First-cycle figures below the second are cold JIT after model load. Every stop returned the
backends to `IDLE`, chat during PoC was refused with 503, and no XID or NaN was logged. GLM
additionally returns 128/128 nonces at batch 16, reproduces a repeated run bit-exactly and
shows no batch-position artifact under shifted batches — the numbers in *First-in-batch
instability* above — and reports `poc_validation_inference: false`. The plugin suite passes
inside the image (`pytest tests/unit tests/contract`: 141 passed, 1 skipped). `/api/v1/state`
reports `"version":"3.1.0"`.

### MiniMax reasoning parser

`--reasoning-parser minimax_m2_append_think` does not separate reasoning on vLLM 0.28: the
whole `<think>…</think>` block arrives in `message.content` and `message.reasoning` stays
empty. `minimax_m2` is correct on this version, confirmed on both hosts above. The MiniMax
profiles are updated accordingly. This was verified on 0.28 only; if 3.0.16 nodes on vLLM
0.25.1 stay in service, re-check the flag there before rolling the change out to them.

## Operational notes

* `NCCL_NVLS_ENABLE=0` is required on the Blackwell hosts tested. Without it NCCL aborts with
  `Failed to bind NVLink SHARP (NVLS) Multicast memory … CUDA error 401`, or hangs silently on
  the first collective.
* `ulimit -n 524288`: several rented hosts ship a soft limit of 1024, below what NCCL needs.
* `--model` must resolve to a local snapshot path for the processor to load; `Glm5NextProcessor`
  does not accept a bare HF id.
