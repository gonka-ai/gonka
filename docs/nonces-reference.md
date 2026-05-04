# Nonces Performance Reference

Quick-reference for GPU hosts to find optimal batch sizes and expected nonces/min for their hardware setup.

## All Results

Sorted alphabetically by model, then by hardware tier (low → high).

| Model | Hardware | Quantization | Batch Size | Nonces/min | Source |
|---|---|---|---|---|---|
| Kimi-K2.5 | 8×H200 | INT4 | 32 | 1216 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/kimi-k25-int4-8xh200) |
| Kimi-K2.5 | 4×B200 | INT4 | 32/64 | 1024 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/kimi-k25-int4-4xb200) |
| Kimi-K2.5 | 4×B200 | NVFP4 | 32 | 1816 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/kimi-k25-nvfp4-4xb200) |
| MiniMax-M2.7 | 2×A100 | AWQ-4bit | 16 | 593 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-awq4bit-2xa100) |
| MiniMax-M2.7 | 4×A100 | FP8 | 16 | 864 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-fp8-4xa100) |
| MiniMax-M2.7 | 4×RTX PRO 6000 | FP8 | 8 | 848 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-fp8-4xrtxpro6000) |
| MiniMax-M2.7 | 2×H100 | AWQ-4bit | 16 | 1095 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-awq4bit-2xh100) |
| MiniMax-M2.7 | 4×H100 | FP8 | 32 | 1664 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-fp8-4xh100) |
| MiniMax-M2.7 | 2×H200 | AWQ-4bit | 16 | 1053 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-awq4bit-2xh200) |
| MiniMax-M2.7 | 2×H200 | FP8 | 16/32 | 1279 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-fp8-2xh200) |
| MiniMax-M2.7 | 2×B200 | AWQ-4bit | — | 1181 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-awq4bit-2xb200) |
| MiniMax-M2.7 | 2×B200 | FP8 | 32 | 2367 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-fp8-2xb200) |
| Qwen3-235B-A22B | 4×A100 | FP8 | 8/16 | 480 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/qwen235b-fp8-4xa100) |
| Qwen3-235B-A22B | 4×RTX PRO 6000 | FP8 | 8 | 848 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/qwen235b-fp8-4xrtx6000se-vllm020-sm120-fi-attn) |
| Qwen3-235B-A22B | 4×H100 | FP8 | 16 | 960 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/qwen235b-fp8-4xh100-vllm019) |
| Qwen3-235B-A22B | 2×B200 | FP8 | 16-64 | 1536 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/qwen235b-fp8-2xb200) |
| Qwen3-235B-A22B | 1×B300 | FP8 | 64 | 1280 | [experiment](https://github.com/kaitakuai/experiments/tree/main/2026-04/qwen235b-fp8-1xb300-vllm020-stockcompile) |

## By Hardware

### A100

| Model | Quantization | GPUs | Batch Size | Nonces/min |
|---|---|---|---|---|
| MiniMax-M2.7 | AWQ-4bit | 2× | 16 | 593 |
| MiniMax-M2.7 | FP8 | 4× | 16 | 864 |
| Qwen3-235B-A22B | FP8 | 4× | 8/16 | 480 |

### RTX PRO 6000

| Model | Quantization | GPUs | Batch Size | Nonces/min |
|---|---|---|---|---|
| MiniMax-M2.7 | FP8 | 4× | 8 | 848 |
| Qwen3-235B-A22B | FP8 | 4× | 8 | 848 |

### H100

| Model | Quantization | GPUs | Batch Size | Nonces/min |
|---|---|---|---|---|
| MiniMax-M2.7 | AWQ-4bit | 2× | 16 | 1095 |
| MiniMax-M2.7 | FP8 | 4× | 32 | 1664 |
| Qwen3-235B-A22B | FP8 | 4× | 16 | 960 |

### H200

| Model | Quantization | GPUs | Batch Size | Nonces/min |
|---|---|---|---|---|
| Kimi-K2.5 | INT4 | 8× | 32 | 1216 |
| MiniMax-M2.7 | AWQ-4bit | 2× | 16 | 1053 |
| MiniMax-M2.7 | FP8 | 2× | 16/32 | 1279 |

### B200

| Model | Quantization | GPUs | Batch Size | Nonces/min |
|---|---|---|---|---|
| Kimi-K2.5 | INT4 | 4× | 32/64 | 1024 |
| Kimi-K2.5 | NVFP4 | 4× | 32 | 1816 |
| MiniMax-M2.7 | AWQ-4bit | 2× | — | 1181 |
| MiniMax-M2.7 | FP8 | 2× | 32 | 2367 |
| Qwen3-235B-A22B | FP8 | 2× | 16-64 | 1536 |

### B300

| Model | Quantization | GPUs | Batch Size | Nonces/min |
|---|---|---|---|---|
| Qwen3-235B-A22B | FP8 | 1× | 64 | 1280 |

## By Model

### Kimi-K2.5

| Hardware | Quantization | Batch Size | Nonces/min |
|---|---|---|---|
| 8×H200 | INT4 | 32 | 1216 |
| 4×B200 | INT4 | 32/64 | 1024 |
| 4×B200 | NVFP4 | 32 | 1816 |

### MiniMax-M2.7

| Hardware | Quantization | Batch Size | Nonces/min |
|---|---|---|---|
| 2×A100 | AWQ-4bit | 16 | 593 |
| 4×A100 | FP8 | 16 | 864 |
| 4×RTX PRO 6000 | FP8 | 8 | 848 |
| 2×H100 | AWQ-4bit | 16 | 1095 |
| 4×H100 | FP8 | 32 | 1664 |
| 2×H200 | AWQ-4bit | 16 | 1053 |
| 2×H200 | FP8 | 16/32 | 1279 |
| 2×B200 | AWQ-4bit | — | 1181 |
| 2×B200 | FP8 | 32 | 2367 |

### Qwen3-235B-A22B

| Hardware | Quantization | Batch Size | Nonces/min |
|---|---|---|---|
| 4×A100 | FP8 | 8/16 | 480 |
| 4×RTX PRO 6000 | FP8 | 8 | 848 |
| 4×H100 | FP8 | 16 | 960 |
| 2×B200 | FP8 | 16-64 | 1536 |
| 1×B300 | FP8 | 64 | 1280 |

## Sources

All data from [kaitakuai/experiments](https://github.com/kaitakuai/experiments/tree/main/2026-04/).
