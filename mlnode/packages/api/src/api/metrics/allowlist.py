"""Metrics schema v1: the allowlist is the contract.

Changing this set (or label semantics) requires a schema version bump and a
changelog entry. See docs/observability.md.
"""

SCHEMA_VERSION = "1"

# vLLM metric families exported as-is (after label rewriting).
# Series names map to a family by stripping _bucket/_sum/_count suffixes.
VLLM_ALLOWLIST = frozenset({
    # P0
    "vllm:num_requests_waiting",
    "vllm:num_requests_running",
    "vllm:kv_cache_usage_perc",
    "vllm:num_preemptions_total",
    "vllm:time_to_first_token_seconds",
    "vllm:request_queue_time_seconds",
    "vllm:prompt_tokens_total",
    "vllm:generation_tokens_total",
    "vllm:request_success_total",
    # P1
    "vllm:inter_token_latency_seconds",
    "vllm:e2e_request_latency_seconds",
    "vllm:prefix_cache_queries_total",
    "vllm:prefix_cache_hits_total",
    "vllm:request_prompt_tokens",
    "vllm:request_generation_tokens",
    "vllm:iteration_tokens_total",
    "vllm:cache_config_info",
})

# Labels stripped from vLLM series: `engine` is an internal index (replica
# identity is carried by the `replica` label we add ourselves).
VLLM_DROPPED_LABELS = frozenset({"engine"})
