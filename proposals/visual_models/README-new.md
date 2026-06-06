# Models Proposal

This is a proposal to add the [Qwen/Qwen3-VL-235B-A22B-Instruct-FP8](https://huggingface.co/Qwen/Qwen3-VL-235B-A22B-Instruct-FP8) multimodal model to the Gonka inference network. 

Validation thresholds for all the models were computed using the standard procedure described in the [inference validation scripts README](../../mlnode/packages/benchmarks/scripts/inference_validation/README.md).

There is respective notebook with the details of experiments and [gdrive folder](https://drive.google.com/file/d/1aIi0RQDArmaP_68I_yG_X5l-BJUfCXmI/view?usp=share_link) with raw inference-validation data.


For the inference the test split (1000 images) of the [Flickr8K dataset](https://huggingface.co/datasets/jxie/flickr8k) was used.


| Parameter | [Qwen/Qwen3-VL-235B-A22B-Instruct-FP8](https://huggingface.co/Qwen/Qwen3-VL-235B-A22B-Instruct-FP8) |
|-----------|-----------|
| Notebook | [qwen3-VL-235B_thresholds-new.ipynb](../../mlnode/packages/benchmarks/notebooks/qwen3-VL-235B_thresholds.ipynb) |
| Validation Data | [link](https://drive.google.com/file/d/1aIi0RQDArmaP_68I_yG_X5l-BJUfCXmI/view?usp=share_link) |
| Model Len |128000|
| Top-K Logprobs | 5 |
| Validation Threshold (Lower) |  0,0214 |
| Fraud Accuracy | 99%  |
| Tested Against | [Qwen3-VL-235B-A22B-Instruct-AWQ](https://huggingface.co/QuantTrio/Qwen3-VL-235B-A22B-Instruct-AWQ) | 
| VRAM (example setup) |~320GB (4xA100 or 4xH100 GPUs)|


All experiments were conducted using MLNode v0.1.0 and vLLM version `0.8.2.dev8106+g9a6d76e05`

**Important Note**: The validation threshold provided was obtained on the flikr8 dataset for the image description task, meaning the generated sequences have a length threshold. Other tasks, such as text recognition and others that generate long sequences, may require recalibration.

**Important Note**: The results were obtained for the value `top_k = 5`. A different value will require recalibration.

**Qwen3-VL-235B-Instruct** is suggested to be deployed with with the following parameters:
```python
additional_args=[
    '--max-model-len', '128000',
    '--gpu-memory-utilization', '0.95'
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
  --top-logprobs 5 \
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

