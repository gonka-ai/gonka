# Testing Guide - Deterministic Sampling Integration

## Quick Start Testing

Follow these steps to test the new deterministic sampling feature:

---

## Option 1: Quick Test with Standalone vLLM Server (Recommended First)

### Step 1: Start vLLM Server

```bash
docker run -d --rm \
  --gpus all \
  -p 8000:8000 \
  -v ~/.cache/huggingface:/root/.cache/huggingface \
  --name vllm-test \
  ghcr.io/ekaterynakuznetsova/vllm:v0.9.2-deterministic \
  --model meta-llama/Llama-3.2-1B-Instruct \
  --host 0.0.0.0 \
  --port 8000
```

**Wait for the model to load** (check logs):
```bash
docker logs -f vllm-test
```

Wait until you see something like: `INFO: Uvicorn running on http://0.0.0.0:8000`

### Step 2: Run Quick Python Test

```bash
cd /Users/katerynakuznetsova/Documents/zpoken/gonka_ai/gonka/mlnode/scripts
python3 test_deterministic_sampling.py http://localhost:8000
```

**Expected Output:**
```
============================================================
Testing vLLM Deterministic Sampling
============================================================

Test 1: Checking if server is accessible...
✅ Server is up! Available models: [...]

Test 2: Testing use_deterministic_hash parameter...
✅ Parameter accepted! Response: ...

Test 3: Testing reproducibility (same seed should give same output)...
   Output 1: ...
   Output 2: ...
✅ REPRODUCIBLE! Same seed produces identical output

Test 4: Testing different seeds produce different outputs...
   Seed 42:  ...
   Seed 123: ...
✅ Different seeds produce different outputs

Test 5: Testing backward compatibility (without use_deterministic_hash)...
✅ Backward compatible - works without the parameter

============================================================
✅ ALL TESTS PASSED!
============================================================
```

### Step 3: Manual cURL Test (Optional)

```bash
# Test 1: With deterministic sampling
curl -X POST http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "meta-llama/Llama-3.2-1B-Instruct",
    "messages": [{"role": "user", "content": "Count to 3"}],
    "temperature": 1.0,
    "seed": 42,
    "use_deterministic_hash": true,
    "max_tokens": 20
  }'

# Run the same request again - output should be IDENTICAL
curl -X POST http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "meta-llama/Llama-3.2-1B-Instruct",
    "messages": [{"role": "user", "content": "Count to 3"}],
    "temperature": 1.0,
    "seed": 42,
    "use_deterministic_hash": true,
    "max_tokens": 20
  }'
```

### Step 4: Clean Up

```bash
docker stop vllm-test
```

---

## Option 2: Full Bash Test Script

This runs comprehensive automated tests:

```bash
cd /Users/katerynakuznetsova/Documents/zpoken/gonka_ai/gonka/mlnode/scripts
./test_vllm_image.sh
```

This will:
1. Check if the Docker image exists
2. Start a vLLM container
3. Run 4 automated tests
4. Ask if you want to keep the server running

---

## Option 3: Test with Gonka Application Integration

### Step 1: Check Docker Compose Files

Find where you configure the vLLM service. Common locations:
```bash
# Search for docker-compose files
find /Users/katerynakuznetsova/Documents/zpoken/gonka_ai/gonka -name "docker-compose*.yml" -type f
```

### Step 2: Update Docker Compose

Edit your docker-compose file(s) to use the new image:

```yaml
services:
  vllm:
    image: ghcr.io/ekaterynakuznetsova/vllm:v0.9.2-deterministic
    # ... rest of your config
```

### Step 3: Update API Calls in Your Code

Find where you make vLLM API calls and add the parameter:

```python
# BEFORE
response = requests.post(
    "http://vllm:8000/v1/chat/completions",
    json={
        "model": "your-model",
        "messages": messages,
        "temperature": 0.7,
        "seed": 42,
        "logprobs": True,
        "top_logprobs": 5
    }
)

# AFTER - Add use_deterministic_hash
response = requests.post(
    "http://vllm:8000/v1/chat/completions",
    json={
        "model": "your-model",
        "messages": messages,
        "temperature": 0.7,
        "seed": 42,
        "use_deterministic_hash": True,  # ← ADD THIS
        "logprobs": True,
        "top_logprobs": 5
    }
)
```

### Step 4: Rebuild and Test

```bash
# Rebuild gonka services with new vLLM base image
cd /Users/katerynakuznetsova/Documents/zpoken/gonka_ai/gonka
docker-compose build

# Start services
docker-compose up -d

# Run integration tests
cd mlnode/packages/api
pytest tests/integration/test_deterministic_sampling.py -v
```

---

## Troubleshooting

### Issue: "Image not found"

**Solution:** Pull the image first:
```bash
docker pull ghcr.io/ekaterynakuznetsova/vllm:v0.9.2-deterministic
```

### Issue: "CUDA out of memory"

**Solution:** Use a smaller model for testing:
```bash
docker run -d --rm \
  --gpus all \
  -p 8000:8000 \
  -v ~/.cache/huggingface:/root/.cache/huggingface \
  ghcr.io/ekaterynakuznetsova/vllm:v0.9.2-deterministic \
  --model TinyLlama/TinyLlama-1.1B-Chat-v1.0 \
  --host 0.0.0.0 \
  --port 8000
```

### Issue: "Connection refused"

**Check if server is running:**
```bash
docker ps | grep vllm
docker logs vllm-test
```

### Issue: "Outputs are not identical"

**Verify both parameters are set:**
```json
{
  "seed": 42,  // Must be set
  "use_deterministic_hash": true  // Must be true
}
```

---

## What to Test

✅ **Must Test:**
1. Server accepts `use_deterministic_hash` parameter without errors
2. Same seed produces identical outputs (reproducibility)
3. Different seeds produce different outputs
4. Backward compatibility (works without the parameter)

✅ **Should Test:**
5. Works with different temperature values (0.5, 0.7, 1.0, 1.5)
6. Works with longer sequences (100+ tokens)
7. Logprobs are identical across runs
8. Works with your actual inference validation workflow

---

## Next Steps After Testing

Once tests pass:

1. ✅ **Update production configs** to use the new image
2. ✅ **Update API calls** in inference validation code to use `use_deterministic_hash: true`
3. ✅ **Monitor** first few production runs for any issues
4. ✅ **Document** the change for your team

---

## Quick Reference

**Docker Image:** `ghcr.io/ekaterynakuznetsova/vllm:v0.9.2-deterministic`

**New Parameter:** `use_deterministic_hash: true`

**Required with:** `seed: <any_integer>`

**Test Script:** `mlnode/scripts/test_deterministic_sampling.py`

**Documentation:** `mlnode/docs/deterministic_sampling_integration.md`
