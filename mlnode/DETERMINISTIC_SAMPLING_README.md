# Deterministic Sampling Integration - Quick Reference

## ✅ What's Done

All changes to sync the gonka repo with vLLM v0.9.2 deterministic sampling:

1. ✅ **Updated Docker Images**
   - `mlnode/packages/api/Dockerfile` → v0.9.2
   - `mlnode/packages/pow/Dockerfile` → v0.9.2
   - `mlnode/packages/train/Dockerfile` → v0.9.2

2. ✅ **Created Test Suite**
   - Integration tests: `tests/integration/test_deterministic_sampling.py`
   - Unit tests: `tests/unit/test_deterministic_sampling_params.py`

3. ✅ **Created Documentation**
   - `docs/deterministic_sampling_integration.md`

4. ✅ **Created Helper Scripts**
   - `scripts/build_vllm_image.sh` - Build vLLM v0.9.2 image
   - `scripts/test_vllm_image.sh` - Test the new image locally

## 🚀 Next Steps

### Step 1: Build vLLM Docker Image

```bash
cd mlnode
./scripts/build_vllm_image.sh
```

This will build `ghcr.io/gonka-ai/vllm:v0.9.2` from the vLLM repo.

### Step 2: Test the Image Locally

```bash
./scripts/test_vllm_image.sh
```

This runs automated tests to verify:
- ✅ `use_deterministic_hash` parameter is accepted
- ✅ Reproducibility (same seed → same output)
- ✅ Different seeds → different outputs
- ✅ Backward compatibility

### Step 3: Push to Registry

```bash
docker push ghcr.io/gonka-ai/vllm:v0.9.2
```

### Step 4: Run Integration Tests

```bash
cd mlnode/packages/api

# Set up test environment
export SERVER_URL=http://localhost:8000

# Run deterministic sampling tests
pytest tests/integration/test_deterministic_sampling.py -v
```

## 📝 Using Deterministic Sampling

### Simple Example

```python
import requests

response = requests.post(
    "http://vllm:8000/v1/chat/completions",
    json={
        "model": "meta-llama/Llama-3.2-3B-Instruct",
        "messages": [{"role": "user", "content": "Hello"}],
        "temperature": 1.0,
        "seed": 42,
        "use_deterministic_hash": True,  # 👈 NEW PARAMETER
        "logprobs": True,
        "top_logprobs": 5
    }
)
```

### Key Points

- ✅ **Backward Compatible**: Parameter is optional, defaults to `False`
- ✅ **100% Reproducible**: Same seed + prompt = identical output
- ✅ **Works with All Sampling**: Respects temperature, top-k, top-p
- ⚠️ **Performance**: ~10-20% slower than regular sampling

## 📊 Test Results Expected

When you run the tests, you should see:

```
✅ test_deterministic_parameter_accepted - Parameter is accepted
✅ test_deterministic_sampling_reproducibility - Same seed gives same output
✅ test_different_seeds_produce_different_outputs - Different seeds work
✅ test_deterministic_vs_regular_sampling - Both modes work
✅ test_deterministic_with_various_temperatures - All temperatures work
✅ test_backward_compatibility_without_parameter - Existing code works
✅ test_deterministic_sampling_with_logprobs - Logprobs are identical
✅ test_deterministic_sampling_with_longer_output - Longer sequences work
```

## 🔍 Quick Test (Manual)

```bash
# Test 1: Same seed, should give identical output
curl -X POST http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "meta-llama/Llama-3.2-1B-Instruct",
    "messages": [{"role": "user", "content": "Count to 5"}],
    "temperature": 1.0,
    "seed": 42,
    "use_deterministic_hash": true
  }'

# Run again - output should be IDENTICAL
curl -X POST http://localhost:8000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "model": "meta-llama/Llama-3.2-1B-Instruct",
    "messages": [{"role": "user", "content": "Count to 5"}],
    "temperature": 1.0,
    "seed": 42,
    "use_deterministic_hash": true
  }'
```

## 📁 Files Changed

### Dockerfiles (3 files)
- `mlnode/packages/api/Dockerfile`
- `mlnode/packages/pow/Dockerfile`  
- `mlnode/packages/train/Dockerfile`

### Tests (2 files)
- `mlnode/packages/api/tests/integration/test_deterministic_sampling.py`
- `mlnode/packages/api/tests/unit/test_deterministic_sampling_params.py`

### Documentation (1 file)
- `mlnode/docs/deterministic_sampling_integration.md`

### Scripts (2 files)
- `mlnode/scripts/build_vllm_image.sh`
- `mlnode/scripts/test_vllm_image.sh`

## ❓ Troubleshooting

### "Invalid parameter: use_deterministic_hash"
→ Make sure vLLM v0.9.2 image is built and used

### Outputs are different with same seed
→ Ensure both `seed` and `use_deterministic_hash` are set:
```json
{
  "seed": 42,
  "use_deterministic_hash": true
}
```

### Scripts won't run
→ Make them executable:
```bash
chmod +x mlnode/scripts/*.sh
```

## 📚 Documentation

For detailed information, see:
- **Integration Guide**: `mlnode/docs/deterministic_sampling_integration.md`
- **vLLM Quick Ref**: `/Users/katerynakuznetsova/Documents/zpoken/vllm/QUICK_REFERENCE.md`
- **vLLM Docker Guide**: `/Users/katerynakuznetsova/Documents/zpoken/vllm/docs/DOCKER_API_INTEGRATION.md`

## 🎯 Summary

**Status**: ✅ Ready to build and test

**Breaking Changes**: ❌ None (fully backward compatible)

**Action Required**:
1. Build vLLM v0.9.2 Docker image
2. Test locally
3. Push to registry
4. Deploy and enjoy deterministic sampling! 🎉
