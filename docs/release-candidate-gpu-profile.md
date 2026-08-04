# Release-candidate GPU profile

This document captures the reproducible parts of an external hardware
campaign. The release model ID, revision, and experiment location are supplied
out of band until the model announcement.

The benchmark used gonka-poc with sequence length 1024 and `k_dim=12`. Nonce
rates are measurements, not admission limits or calibrated chain weights.

## Planned chain parameters

The pending release model uses a PoC weight scale factor of `0.32` and a PoC
L2 distance threshold (`PoCStatTestParams.dist_threshold`) of `0.41`. These are
release inputs for the separate chain activation; this MLNode PR records them
but does not activate the model on chain.

## Hardware profiles

| GPU profile | TP per vLLM instance | GPU memory | Batched tokens | PoC batch | Measured nonce/min | Tested driver |
|---|---:|---:|---:|---:|---:|---|
| 1× B300 | 1 | 0.90 | 32768 | 32 | 1728 | 580.126.09 |
| 2× B200 | 2 | 0.90 | 32768 | 32 | 2304 | 580.126.09 |
| 2× H200 | 2 | 0.90 | 32768 | 32 | 1215–1216 | 590.48.01 |
| 4× H100 80 GB | 4 | 0.85 | 16384 | 16 | 1504 | 580.126.20 |

The rates above are per vLLM instance. The full batch sweeps below show
speculative decoding off/on in nonce/min:

| GPU profile | Batch 8 | Batch 16 | Batch 32 |
|---|---:|---:|---:|
| 1× B300 | 1504 / 1648 | 1696 / 1696 | 1728 / 1728 |
| 2× B200 | 2032 / 2192 | 2272 / 2272 | 2304 / 2304 |
| 2× H200 | 1120 / 1104 | 1184 / 1184 | 1216 / 1215 |
| 4× H100 80 GB | 1424 / 1408 | 1504 / 1504 | unavailable / unavailable |

The matching files are in `deploy/join/node-config-release-candidate-*.json`.
Replace `REPLACE_WITH_RELEASE_MODEL_ID` and `REPLACE_WITH_MODEL_REVISION` from
the private release manifest before use. The profiles set 400k context, FP8 KV
cache, processed logprobs, and the PoC worker extension. Do not force an
attention backend; use the runtime-selected default.

MLNode may start more than one vLLM instance on a node. It starts
`floor(visible GPUs / (TP × PP))`, capped by `INFERENCE_MAX_INSTANCES`. Two
B300s with TP=1 start two instances; four B200s with TP=2 also start two. The
configured `max_concurrent=500` is a routing queue ceiling, not a measured safe
number of simultaneously running GPU sequences.

For H100, set `POC_BATCH_SIZE_DEFAULT=16`. The encoded 0.85/16384 profile is
the only tested H100 configuration that survived the campaign's long-prompt,
20-request scenario. The 0.90/32768 configuration supports artifact collection
at 400k but lost 32 of 40 requests under load. It must not be used for serving
until `--max-num-seqs` is calibrated; the campaign did not establish a value.

## Optional speculative decoding

Speculative decoding is deliberately not enabled by the public node profiles;
its exact configuration is part of the private release manifest. It was
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
  `ghcr.io/gonka-ai/mlnode:3.0.14-post2-vllm0.25.1-rc1@sha256:7ba43ce4ad98d0d34c7b8626b424fffc2857c8dd4a2de86831e1b522fa09042b`.
  The source image
  `ghcr.io/vbgd0/gonka-mlnode:3.0.14-post2-vllm0.25.1-rc1@sha256:7ba43ce4ad98d0d34c7b8626b424fffc2857c8dd4a2de86831e1b522fa09042b`
  resolves to the same OCI index digest.
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

1. Verify the pinned image's vLLM, gonka-poc, CUDA, driver, `libnvrtc.so`,
   model ID, and revision before activation.
2. Run cold start, `/health`, `/metrics`, inference, PoC generation, and PoC
   validation on every advertised card profile.
3. Reproduce the nonce/min rows with 5-second warmup and 30-second steady
   state; compare at least three fixed nonce sets across GPU families.
4. Run 400k single-request and long-prompt concurrent soak tests. Calibrate
   `--max-num-seqs` before advertising H100 beyond the tested profile.
5. Test speculation off and on against the expected serving mix; keep it off
   when it reduces aggregate throughput or KV headroom.
6. Measure the alternate quantization across GPU families and calibrate fraud
   thresholds. No inference fraud threshold from the campaign is safe to copy
   into chain parameters; this is separate from the planned PoC L2 threshold.
7. Add the private manifest values, PoC weight scale `0.32`, PoC L2 threshold
   `0.41`, and calibrated validation policy to the chain upgrade, then rehearse
   mixed old and new nodes through a full epoch.
