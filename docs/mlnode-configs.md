# MLNode PoC configuration

The presets are in `deploy/join/`. Choose one for your model, GPUs, and PoC
scheme. Changing a preset requires an MLNode restart. Set the environment
variables below in the `.env` used by `deploy/join/docker-compose.mlnode.yml`.

## MiniMax PREFILL preset

MiniMax-M2.7 `prefill-max` requires:

```dotenv
MLNODE_IMAGE=ghcr.io/gonka-ai/mlnode:4.0.0-sigma1-minimaxxer@sha256:60e6c6ff9786663b78a94d9e112880068395296144ba7e44c63aff09b5e8703b
POC_WORKER_MINING=1
POC_CALLBACK_INTERVAL_SEC=1
```

The B200/H100/H200 presets use pipeline parallelism. DECODE PoC does not support
pipeline parallelism: the engine stalls and MLNode shuts down. Switch away from
these presets before mining DECODE.

## PREFILL batch size

`POC_BATCH_SIZE_DEFAULT` sets the number of nonces per PREFILL batch for both
mining and validation. It defaults to 32. With the current 1024-token input,
`batch × 1024` must fit `--max-num-batched-tokens`. An oversized batch can fail
with zero nonces.

These values apply to PREFILL work, including when it runs on a `decode-max` preset:

- GLM-5.3-Flash: 16 on 8×H100 (8 with `decode-max`), 16 on H200, 32 on B200/B300
  (16 with `decode-max`). A batch of 64 crashes the model.
- DeepSeek-V4-Flash: 16 on 4×H100; 64 with the NVFP4 `prefill-max` preset.
- MiniMax-M2.7 `prefill-max`: 96 on B200/B300; 32 on H100/H200.
- Other configurations: 32.

## DECODE batch size

DECODE does not use `POC_BATCH_SIZE_DEFAULT`. DAPI omits `batch_size`; MLNode
sends `batch_size=0` to request automatic sizing. The backend uses
`poc_max_batch_size` from `--additional-config` when set, otherwise
`--max-num-seqs`. vLLM schedules those nonces within its sequence and KV-cache
limits.

The current DECODE recipe uses 256 input tokens and 256 decode steps per nonce,
producing 257 artifact points.

## GLM-5.3-Flash FlashKDA buffer

GLM-5.3-Flash prefill uses a FlashKDA buffer sized from `--max-num-seqs` and
`--max-num-batched-tokens`. With the chain's `--max-model-len 400000`, vLLM locks its
workspace at about 2015 MiB on TP2 before that buffer is reserved, so a larger buffer
fails the first prefill (`Workspace is locked`) and the node mines no nonces. On TP2,
256 sequences with 32768 tokens and 384 sequences with 16384 tokens fit; 256 with
65536 and 1024 with 32768 do not. TP4 halves the buffer: on H200, 768 sequences with
16384 tokens fit. Check this before raising either value.

## DeepSeek-V4-Flash on 2×H200

Set `VLLM_MEMORY_PROFILER_ESTIMATE_CUDAGRAPHS=0`. Otherwise, vLLM 0.30 subtracts its
6.7 GiB CUDA-graph estimate from the KV cache, leaving insufficient room for
1024 DECODE PoC requests of 512 tokens each. Requests are then preempted, reducing
mining throughput from approximately 640 to 383 nonces/minute. This setting must
be an integer.
