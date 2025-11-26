# Testing Deterministic Sampling

## Run Integration Tests

```bash
cd mlnode/packages/api
export SERVER_URL=http://localhost:8000
pytest tests/integration/test_deterministic_sampling.py -v
```

## Tests

- `test_deterministic_parameter_accepted` - Parameter is accepted by API
- `test_deterministic_sampling_reproducibility` - Same seed produces identical outputs
- `test_different_seeds_produce_different_outputs` - Different seeds work correctly
- `test_deterministic_vs_regular_sampling` - Both modes function
- `test_deterministic_with_various_temperatures` - Works with different temperatures
- `test_backward_compatibility_without_parameter` - Backward compatible
- `test_deterministic_sampling_with_logprobs` - Logprobs are reproducible
- `test_deterministic_sampling_with_longer_output` - Works with longer sequences
