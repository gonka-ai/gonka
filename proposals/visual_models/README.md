# Models Proposal

This is a proposal to add the [Qwen/Qwen3-VL-235B-A22B-Instruct-FP8](https://huggingface.co/Qwen/Qwen3-VL-235B-A22B-Instruct-FP8) multimodal model to the Gonka inference network. 

Validation thresholds for all the models were computed using the standard procedure described in the [inference validation scripts README](../../mlnode/packages/benchmarks/scripts/inference_validation/README.md).

There is respective notebook with the details of experiments and [gdrive folder](https://drive.google.com/drive/folders/1Yk-ORRE51jv2Apv5x2yCic8w6QBdU7_r?usp=share_link) with raw inference-validation data.

Also for the comparison the results of "small" models tests [Qwen/Qwen2-VL-2B-Instruct-GPTQ-INT8](https://huggingface.co/Qwen/Qwen2-VL-2B-Instruct-GPTQ-Int8)

For the inference the test split (1000 images) of the [Flickr8K dataset](https://huggingface.co/datasets/jxie/flickr8k) was used.


| Parameter | [Qwen/Qwen3-VL-235B-A22B-Instruct-FP8](https://huggingface.co/Qwen/Qwen3-VL-235B-A22B-Instruct-FP8) | [Qwen/Qwen2-VL-2B-Instruct-GPTQ-INT8](https://huggingface.co/Qwen/Qwen2-VL-2B-Instruct-GPTQ-Int8) |
|-----------|-----------|-----------|
| Notebook | [qwen3-VL-235B_thresholds.ipynb](../../mlnode/packages/benchmarks/notebooks/qwen3-VL-235B_thresholds.ipynb) | [qwen2-2B-VL_thresholds.ipynb](../../mlnode/packages/benchmarks/notebooks/qwen2-2B-VL_thresholds.ipynb) |
| Validation Data | [link](https://drive.google.com/drive/folders/1Yk-ORRE51jv2Apv5x2yCic8w6QBdU7_r?usp=share_link) | [link](https://drive.google.com/drive/folders/12qjW2eW5_R9KOb4mBCVZU-LkIAqw2kel?usp=share_link)|
| Model Len |128000| 32768 |
| Top-K Logprobs | 20 / 10 / 5 | 20 / 10 / 5 |
| Validation Thresholds |  (0,0169;0,0179) / (0.0232;0,0242) / (0,0323;0,0333) | (0,0033;0,0044) / (0,0047;0,0057) / (0,0062;0,0072) |
| Fraud Accuracy | 100% / 100% / 100%  | 100% / 100% / 100% |
| Tested Against | [Qwen3-VL-235B-A22B-Instruct-AWQ](https://huggingface.co/QuantTrio/Qwen3-VL-235B-A22B-Instruct-AWQ) |  [Qwen/Qwen2-VL-2B-Instruct-GPTQ-INT4](https://huggingface.co/Qwen/Qwen2-VL-2B-Instruct-GPTQ-Int4)|
| VRAM (example setup) |~320GB (4xA100 or 4xH100 GPUs)| ~20GB (0,5xA100 GPU) |


All experiments were conducted using MLNode v0.1.0

**Qwen3-VL-235B-Instruct** is suggested to be deployed with with the following parameters:
```python
additional_args=[
    '--max-model-len', '128000',
    '--gpu-memory-utilization', '0.95'
]
```

**Qwen2-VL-2B-Instruct** is suggested to be deployed with with the following parameters:
```python
additional_args=[
    '--gpu-memory-utilization', '0.5'
]
```

Script for preparing test set is available [here](../../mlnode/packages/benchmarks/scripts/inference_validation/download_test_set.py).

An example of the inference

```bash
python vlm_inference.py \
  --url http://localhost:8801 \
  --exp-dir /root/inference \
  --prompt "Describe the image." \
  --images-dir /root/flickr8k_images/test/ \
  --top-logprobs 20 \
  --temperature 0.99

```

An example of the validation

```bash
python vlm_validation.py \
  --validation-url http://localhost:8001 \
  --inference-artifact /root/inference/inference_results.jsonl \
  --exp-dir /root/validation \
  --images-dir /root/flickr8k_images/test/
```

