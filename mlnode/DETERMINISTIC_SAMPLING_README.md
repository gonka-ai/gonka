# Deterministic Sampling Integration

## Changes

Updated Docker images to vLLM v0.9.2 with deterministic hash-based sampling:
- `mlnode/packages/api/Dockerfile`
- `mlnode/packages/pow/Dockerfile`
- `mlnode/packages/train/Dockerfile`

## Usage

Add `use_deterministic_hash: true` to API requests:

```python
import requests

response = requests.post(
    "http://vllm:8000/v1/chat/completions",
    json={
        "model": "meta-llama/Llama-3.2-3B-Instruct",
        "messages": [{"role": "user", "content": "Hello"}],
        "temperature": 1.0,
        "seed": 42,
        "use_deterministic_hash": True
    }
)
```

Same seed + prompt = identical output every time.

## Testing

```bash
cd mlnode/packages/api
export SERVER_URL=http://localhost:8000
pytest tests/integration/test_deterministic_sampling.py -v
```
