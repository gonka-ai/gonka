# Nonces Performance Reference

Quick-reference for GPU hosts to find optimal batch sizes and expected nonces/min for their hardware setup.

## All Results

Sorted alphabetically by model, then by hardware tier (low → high).

| Model | Hardware | Quantization | Batch Size | Nonces/min | Source |
|---|---|---|---|---|---|
| Kimi-K2.5 | 8×H200 | INT4 | 32 | 1216 | [2026-04](https://github.com/kaitakuai/experiments/tree/main/2026-04/kimi-k25-int4-8xh200) |
| Kimi-K2.5 | 4×B200 | INT4 | 32/64 | 1024 | [2026-04](https://github.com/kaitakuai/experiments/tree/main/2026-04/kimi-k25-int4-4xb200) |
| Kimi-K2.5 | 4×B200 | NVFP4 | 32 | 1816 | [2026-04](https://github.com/kaitakuai/experiments/tree/main/2026-04/kimi-k25-nvfp4-4xb200) |
| Kimi-K2.6 | 2×8×H100 | INT4 | 32 | 1389 | [2026-05](https://github.com/kaitakuai/experiments/tree/main/2026-05/kimi-k26-int4-2x8xh100) |
| Kimi-K2.6 | 4×B200 | INT4 | 32 | 2240 | [2026-05](https://github.com/kaitakuai/experiments/tree/main/2026-05/kimi_k26_4xb200_b200-k5-kimi-1) |
| Kimi-K2.6 | 8×B300 | INT4 | 64 | 5120 | [2026-05](https://github.com/kaitakuai/experiments/tree/main/2026-05/kimi_k26_b300_eager_flashinfer) |
| MiniMax-M2.7 | 2×A100 | AWQ-4bit | 16 | 593 | [2026-04](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-awq4bit-2xa100) |
| MiniMax-M2.7 | 4×A100 | FP8 | 16 | 896 | [2026-05](https://github.com/kaitakuai/experiments/tree/main/2026-05/minimax-m27-fp8-4xa100) |
| MiniMax-M2.7 | 4×RTX PRO 6000 | FP8 | 8 | 848 | [2026-04](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-fp8-4xrtxpro6000) |
| MiniMax-M2.7 | 2×H100 | AWQ-4bit | 16 | 1095 | [2026-04](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-awq4bit-2xh100) |
| MiniMax-M2.7 | 4×H100 | FP8 | 32 | 2368 | [2026-05](https://github.com/kaitakuai/experiments/tree/main/2026-05/minimax-m27-fp8-4xh100) |
| MiniMax-M2.7 | 2×H200 | AWQ-4bit | 16 | 1053 | [2026-04](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-awq4bit-2xh200) |
| MiniMax-M2.7 | 2×H200 | FP8 | 32 | 1728 | [2026-05](https://github.com/kaitakuai/experiments/tree/main/2026-05/minimax-m27-fp8-2xh200) |
| MiniMax-M2.7 | 2×B200 | AWQ-4bit | — | 1181 | [2026-04](https://github.com/kaitakuai/experiments/tree/main/2026-04/minimax-m27-awq4bit-2xb200) |
| MiniMax-M2.7 | 2×B200 | FP8 | 32 | 2624 | [2026-05](https://github.com/kaitakuai/experiments/tree/main/2026-05/minimax-m27-fp8-2xb200) |
| Qwen3-235B-A22B | 4×A100 | FP8 | 8/16 | 480 | [2026-04](https://github.com/kaitakuai/experiments/tree/main/2026-04/qwen235b-fp8-4xa100) |
| Qwen3-235B-A22B | 4×RTX PRO 6000 | FP8 | 8 | 848 | [2026-04](https://github.com/kaitakuai/experiments/tree/main/2026-04/qwen235b-fp8-4xrtx6000se-vllm020-sm120-fi-attn) |
| Qwen3-235B-A22B | 4×H100 | FP8 | 16 | 1248 | [2026-05](https://github.com/kaitakuai/experiments/tree/main/2026-05/qwen235b-fp8-4xh100) |
| Qwen3-235B-A22B | 4×H200 | FP8 | 32/64 | 1408 | [2026-05](https://github.com/kaitakuai/experiments/tree/main/2026-05/qwen235b-fp8-4xh200) |
| Qwen3-235B-A22B | 2×B200 | FP8 | 64 | 1984 | [2026-05](https://github.com/kaitakuai/experiments/tree/main/2026-05/qwen235b-fp8-2xb200) |
| Qwen3-235B-A22B | 1×B300 | FP8 | 64 | 1280 | [2026-04](https://github.com/kaitakuai/experiments/tree/main/2026-04/qwen235b-fp8-1xb300-vllm020-stockcompile) |

## By Hardware

### A100

| Model | Quantization | GPUs | Batch Size | Nonces/min |
|---|---|---|---|---|
| MiniMax-M2.7 | AWQ-4bit | 2× | 16 | 593 |
| MiniMax-M2.7 | FP8 | 4× | 16 | 896 |
| Qwen3-235B-A22B | FP8 | 4× | 8/16 | 480 |

### RTX PRO 6000

| Model | Quantization | GPUs | Batch Size | Nonces/min |
|---|---|---|---|---|
| MiniMax-M2.7 | FP8 | 4× | 8 | 848 |
| Qwen3-235B-A22B | FP8 | 4× | 8 | 848 |

### H100

| Model | Quantization | GPUs | Batch Size | Nonces/min |
|---|---|---|---|---|
| Kimi-K2.6 | INT4 | 2×8× | 32 | 1389 |
| MiniMax-M2.7 | AWQ-4bit | 2× | 16 | 1095 |
| MiniMax-M2.7 | FP8 | 4× | 32 | 2368 |
| Qwen3-235B-A22B | FP8 | 4× | 16 | 1248 |

### H200

| Model | Quantization | GPUs | Batch Size | Nonces/min |
|---|---|---|---|---|
| Kimi-K2.5 | INT4 | 8× | 32 | 1216 |
| MiniMax-M2.7 | AWQ-4bit | 2× | 16 | 1053 |
| MiniMax-M2.7 | FP8 | 2× | 32 | 1728 |
| Qwen3-235B-A22B | FP8 | 4× | 32/64 | 1408 |

### B200

| Model | Quantization | GPUs | Batch Size | Nonces/min |
|---|---|---|---|---|
| Kimi-K2.5 | INT4 | 4× | 32/64 | 1024 |
| Kimi-K2.5 | NVFP4 | 4× | 32 | 1816 |
| Kimi-K2.6 | INT4 | 4× | 32 | 2240 |
| MiniMax-M2.7 | AWQ-4bit | 2× | — | 1181 |
| MiniMax-M2.7 | FP8 | 2× | 32 | 2624 |
| Qwen3-235B-A22B | FP8 | 2× | 64 | 1984 |

### B300

| Model | Quantization | GPUs | Batch Size | Nonces/min |
|---|---|---|---|---|
| Kimi-K2.6 | INT4 | 8× | 64 | 5120 |
| Qwen3-235B-A22B | FP8 | 1× | 64 | 1280 |

## By Model

### Kimi-K2.5

| Hardware | Quantization | Batch Size | Nonces/min |
|---|---|---|---|
| 8×H200 | INT4 | 32 | 1216 |
| 4×B200 | INT4 | 32/64 | 1024 |
| 4×B200 | NVFP4 | 32 | 1816 |

### Kimi-K2.6

| Hardware | Quantization | Batch Size | Nonces/min |
|---|---|---|---|
| 2×8×H100 | INT4 | 32 | 1389 |
| 4×B200 | INT4 | 32 | 2240 |
| 8×B300 | INT4 | 64 | 5120 |

### MiniMax-M2.7

| Hardware | Quantization | Batch Size | Nonces/min |
|---|---|---|---|
| 2×A100 | AWQ-4bit | 16 | 593 |
| 4×A100 | FP8 | 16 | 896 |
| 4×RTX PRO 6000 | FP8 | 8 | 848 |
| 2×H100 | AWQ-4bit | 16 | 1095 |
| 4×H100 | FP8 | 32 | 2368 |
| 2×H200 | AWQ-4bit | 16 | 1053 |
| 2×H200 | FP8 | 32 | 1728 |
| 2×B200 | AWQ-4bit | — | 1181 |
| 2×B200 | FP8 | 32 | 2624 |

### Qwen3-235B-A22B

| Hardware | Quantization | Batch Size | Nonces/min |
|---|---|---|---|
| 4×A100 | FP8 | 8/16 | 480 |
| 4×RTX PRO 6000 | FP8 | 8 | 848 |
| 4×H100 | FP8 | 16 | 1248 |
| 4×H200 | FP8 | 32/64 | 1408 |
| 2×B200 | FP8 | 64 | 1984 |
| 1×B300 | FP8 | 64 | 1280 |
