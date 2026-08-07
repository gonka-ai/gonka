# Release-candidate GPU profile

This document captures the reproducible parts of an external hardware
campaign for
[`deepseek-ai/DeepSeek-V4-Flash-0731`](https://huggingface.co/deepseek-ai/DeepSeek-V4-Flash-0731)
at revision `7872f01b1d1fe23eabc4c98b48bffcef5a386062`.

The benchmark used gonka-poc with sequence length 1024 and `k_dim=12`. Nonce
rates are measurements, not admission limits or calibrated chain weights.

## Acknowledgements

Most of the underlying hardware-profile, throughput, speculative-decoding,
and validation experiments were conducted by the
[Kaitaku team](https://github.com/kaitakuai/experiments/tree/main/2026-08).
Thank you to Kaitaku for the research, development, and reproducible evidence
that made this integration possible. The four-instance H200 verification below
is a separate final-image rerun performed for this PR.

## Planned chain parameters

The candidate chain settings are a PoC weight scale factor of `0.21`,
PoC stat-test parameters `dist_threshold=0.41`, `p_mismatch=0.10`, and
`fraud_threshold=0.05`, and a provisional inference validation threshold
(`Model.validation_threshold`) of `0.90` with processed logprobs. The PoC and
inference settings passed the honest multi-instance checks below. The
`0.21` weight is chain-side arithmetic and is reported against measured
throughput, not passed to MLNode as a runtime flag. This MLNode PR records
release inputs and evidence but does not activate the model.

## Hardware profiles

| GPU profile | TP per vLLM instance | GPU memory | Batched tokens | PoC batch | Measured nonce/min | Tested driver |
|---|---:|---:|---:|---:|---:|---|
| 1× B300 | 1 | 0.90 | 32768 | 32 | 1728 | 580.126.09 |
| 2× B200 | 2 | 0.90 | 32768 | 32 | 2304 | 580.126.09 |
| 2× H200 | 2 | 0.90 | 32768 | 32 | 1215–1216 | 590.48.01 |
| 4× (2× H200), concurrent | 2 | 0.90 | 32768 | 32 | 1051.5–1055.7 each; 4205.7 aggregate | 580.173.02 |
| 4× H100 80 GB | 4 | 0.85 | 16384 | 16 | 1504 | 580.126.20 |

The rates above are per vLLM instance. The full batch sweeps below show
speculative decoding off/on in nonce/min:

| GPU profile | Batch 8 | Batch 16 | Batch 32 |
|---|---:|---:|---:|
| 1× B300 | 1504 / 1648 | 1696 / 1696 | 1728 / 1728 |
| 2× B200 | 2032 / 2192 | 2272 / 2272 | 2304 / 2304 |
| 2× H200 | 1120 / 1104 | 1184 / 1184 | 1216 / 1215 |
| 4× H100 80 GB | 1424 / 1408 | 1504 / 1504 | unavailable / unavailable |

The matching files are in
`deploy/join/node-config-deepseekv4flash0731-*.json`.
All profiles pin the Hugging Face model and revision above and set 400k
context and FP8 KV cache. MLNode injects the PoC worker extension into every
vLLM launch. Validation requests select their logprobs mode, so the profiles
do not set a server-wide `--logprobs-mode`. They also use
`--tokenizer-mode deepseek_v4`, `--enable-auto-tool-choice`,
`--tool-call-parser deepseek_v4`, and `--reasoning-parser deepseek_v4`, matching
the DeepSeek V4 encoding and parser implementations bundled with vLLM 0.25.1.
Do not force an attention backend; use the runtime-selected default.

Clients select reasoning with `chat_template_kwargs`: `thinking=true` and
`reasoning_effort=low|high|max`. DeepSeek recommends `temperature=1.0` and
`top_p=0.95` for agentic scenarios (`top_p=1.0` otherwise).

MLNode may start more than one vLLM instance on a node. It starts
`floor(visible GPUs / (TP × PP))`, capped by `INFERENCE_MAX_INSTANCES`. Two
B300s with TP=1 start two instances; four B200s with TP=2 also start two. The
configured `max_concurrent=500` is a routing queue ceiling, not a measured safe
number of simultaneously running GPU sequences.

## Four-instance H200 verification

The published MLNode image cold-started four independent TP2 vLLM instances
on eight H200s in 555.6 seconds. The model cache was mounted on tmpfs. All four
backends reached `/health`, remained healthy under simultaneous load, and
used the pinned model revision. Validation requests explicitly selected
processed logprobs.

Inference validation replayed every source against every validator, including
self-replay (16 directed pairs). All 16 similarities passed the provisional
strict `>0.90` comparison; the observed range was 0.9591–1.0. Tool calling
produced a valid parsed function call on all four backends. Reasoning was
separated into the OpenAI-compatible `message.reasoning` field on all four
backends.

PoC generation used `seq_len=1024`, `k_dim=12`, and batch 32. A simultaneous
640-nonce run measured 1051.5–1055.7 nonce/min per instance and 4205.7
nonce/min in aggregate. Applying the `0.21` weight gives 883.2 weighted
nonce/min in aggregate.

For each of the 16 directed replica pairs, 640 fixed nonces were replayed
three times (1920 L2 samples per pair). At `dist_threshold=0.41`, honest
mismatch rates were 0.68%–1.72%. Observed p95 was 0.315–0.333, p99 was
0.401–0.435, and isolated maxima reached 0.61–1.39. The live 64-nonce API
matrix had at most two mismatches per validation. With `p_mismatch=0.10` and
`fraud_threshold=0.05`, its smallest upper-tail p-value was approximately
0.9904, so all 16 honest validations passed the statistical test.

This agrees with the earlier B200/H200 campaign: normal routing accepted only
98.8%–99.2% of honest artifacts at 0.41, while honest and alternate
quantization distributions had AUC near 0.5. The intended statistical
parameters avoid false fraud in this run, but they do not create quantization
separability. Detection of a disallowed quantization therefore remains a
separate policy gate.

For H100, set `POC_BATCH_SIZE_DEFAULT=16`. The encoded 0.85/16384 profile is
the only tested H100 configuration that survived the campaign's long-prompt,
20-request scenario. The 0.90/32768 configuration supports artifact collection
at 400k but lost 32 of 40 requests under load. It must not be used for serving
until `--max-num-seqs` is calibrated; the campaign did not establish a value.

## Optional speculative decoding

Speculative decoding is deliberately not enabled by the node profiles. It was
neutral for PoC throughput and nonce compatibility, but its serving benefit
depends strongly on workload:

| Profile | 20k sequential | 2k, concurrency 30 | 45k sequential | 45k, concurrency 20 |
|---|---:|---:|---:|---:|
| B300 | 3.74× | 0.98× | 3.00× | 2.10× |
| 2× B200 | 3.87× | 0.58× | 3.29× | 2.14× |
| 2× H200 | 2.63× | 1.00× | 2.98× | 1.36× |
| 4× H100 | 2.97× | 1.32× | 3.39× | 1.80× |

It also reduced KV capacity by 12.2% on B200, 13.8% on H200, and 16.9% on
B300. Enable it only for a measured traffic profile; B200 short concurrent
throughput fell by 42%.

## Image and driver requirements

- Use the production org mirror
  `ghcr.io/gonka-ai/mlnode:3.0.14-post2-vllm0.25.1-rc2@sha256:a476dd28b79b81126b460a56e3f2c8e573bbddfda3e854776cb6fc0f60208f81`.
  It reports the canonical MLNode release `3.0.14-post2` through
  `/api/v1/state`, `/api/v1/versions`, and `mlnode_version_info`.
- Use the Gonka vLLM 0.25.1 build containing the current PoC replay support.
  Do not set the removed `VLLM_USE_V1` switch or pin
  `VLLM_USE_V2_MODEL_RUNNER=0`.
- The image must provide `/usr/local/cuda/lib64/libnvrtc.so`; Hopper fails on
  its first JIT link without the unversioned library name.
- Allow at least 3600 seconds for cold startup. The measured B200 starts took
  26.75 and 19.5 minutes; a 30-core B300 took 17 minutes cold.
- B200 driver 595.71.05 hung before weight loading in two attempts. The tested
  580.126.09 driver loaded successfully.

## Validation results and release gates

B300 execution across honest, alternate-quantization, and stale-checkpoint
arms replayed on H200 with zero token mismatches in 6000 replays. Speculation
on/off and mixed H100/H200 validation stayed within the honest noise floor.
That establishes compatibility, not a network threshold.

The alternate quantized checkpoint remains a policy blocker. It produced 2816
vs 1728 nonce/min on B300 (+63%) and 3200 vs 2304 on B200 (+38.9%). In
inference validation, raw logprobs reached only F1 0.742 with 32.9% false
positives; processed logprobs reached F1 0.664 with 97.2% false positives. A
stale checkpoint was easier to detect, but one threshold cannot cover both.

Before enabling the release model on chain:

1. Verify the pinned image's vLLM, gonka-poc, CUDA, driver, `libnvrtc.so`, and
   the pinned DeepSeek V4 Flash 0731 revision before activation.
2. Run cold start, `/health`, `/metrics`, inference, PoC generation, and PoC
   validation on every advertised card profile.
3. Reproduce the nonce/min rows with 5-second warmup and 30-second steady
   state; compare at least three fixed nonce sets across GPU families.
4. Run 400k single-request and long-prompt concurrent soak tests. Calibrate
   `--max-num-seqs` before advertising H100 beyond the tested profile.
5. Test speculation off and on against the expected serving mix; keep it off
   when it reduces aggregate throughput or KV headroom.
6. Measure the alternate quantization across GPU families and evaluate the
   provisional inference validation threshold `0.90`. This is separate from
   the planned PoC L2 threshold and remains subject to recalibration.
7. Keep PoC weight scale `0.21`, PoC stat-test parameters
   `0.41`/`0.10`/`0.05`, and provisional inference validation threshold `0.90`
   as activation candidates. Reproduce the PoC test with production sample
   sizing and keep alternate-quantization detection as a separate policy gate.
8. Rehearse mixed old and new nodes through a full epoch before activation.
