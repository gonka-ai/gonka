# Gonka ML-node observability (schema v1)

## What this is

Every Gonka network node publishes fresh (≤ 5 min) operational metrics of its
ML nodes through a public endpoint. There is no central collection: any
network participant pulls the metrics into their own storage
(Prometheus/VictoriaMetrics) and builds their own dashboards.

## For ML-node operators

- Export is enabled by default: `GONKA_METRICS=full`.
- To disable: `GONKA_METRICS=off` — the endpoint answers 404, the node
  disappears from the network node's public output, inference is unaffected.
  Values other than `full`/`off` are treated as `full` with a startup warning.
- What is exported: strictly the fixed schema v1 allowlist below. No request
  or prompt data, no hostnames/IPs/ports/provider details/local paths. The
  set changes only with a schema version bump and a changelog entry.
- The mlnode startup log states the export mode.
- vLLM vendor telemetry is disabled (`VLLM_NO_USAGE_STATS=1`).

## For network-node operators

- dapi federates the ML nodes of its inventory on the public
  `GET /v1/mlnodes/metrics`. It scrapes them on demand rather than on a timer:
  a merged snapshot is cached for 10 s and rebuilds are single-flighted, so any
  number of external scrapers costs the ML nodes at most one fan-out per TTL.
- Per-node ceilings: 2 MiB of body and 5000 series. A node over either is
  rejected and reported down rather than allowed to inflate the output.
- Rate limiting lives in the proxy's dedicated nginx `metrics_zone`, not in the
  application, so scrapers never compete with inference clients for the same
  budget. Defaults are per-IP and tunable via `METRICS_RATE_LIMIT_RPM` and
  `METRICS_BURST`; over the limit is a 429.
- Kill switch without a rebuild: `DAPI_API__MLNODE_METRICS_DISABLED=true`.
- Three-state node semantics: `mlnode_up{node_id} 1` scraped fine,
  `mlnode_up{node_id} 0` reachable but the scrape failed or was over a ceiling,
  and a node answering 404 — metrics off, or an image predating the exporter —
  is absent from the output entirely, with no zeros and no up-series. That is
  what makes the dapi-first upgrade order safe.

## For consumers

- `GET https://<network-node>/v1/mlnodes/metrics` — Prometheus text exposition,
  all ML nodes of that network node, label `node_id`. A source timestamp stops
  when that source can no longer be scraped; `mlnode_up` drops to 0 when the
  node exporter itself stops answering. Scheduler progress is represented by
  `vllm:iteration_tokens_total` together with the running/waiting gauges.
- Use `rate()`/`increase()` on counters: a vLLM restart (model switch)
  resets them.
- Aggregate request-length histograms per model only (buckets depend on
  `max_model_len`).
- The network phase (inference/PoC) is not part of the metrics — join it
  from chain data (reference example: Phase 3 deliverable).

## Schema v1 (allowlist)

The schema version is announced by the series
`gonka_metrics_schema_info{version="1"}`.

### vLLM (labels: `model_name` — served name, `replica`)

P0: `vllm:num_requests_waiting`, `vllm:num_requests_running`,
`vllm:kv_cache_usage_perc`, `vllm:num_preemptions_total`,
`vllm:time_to_first_token_seconds`, `vllm:request_queue_time_seconds`,
`vllm:prompt_tokens_total`, `vllm:generation_tokens_total`,
`vllm:request_success_total{finished_reason}`.

P1: `vllm:inter_token_latency_seconds`, `vllm:e2e_request_latency_seconds`,
`vllm:prefix_cache_queries_total`, `vllm:prefix_cache_hits_total`,
`vllm:request_prompt_tokens`, `vllm:request_generation_tokens`,
`vllm:iteration_tokens_total`, `vllm:cache_config_info`.

`replica` is the positional index of the vLLM instance on the node. The
internal vLLM `engine` label is dropped; `model_name` is normalized to the
served model name (never a local cache path).

### GPU / host (labels: `gpu_index`, `gpu_model`; no sensor ⇒ no series)

`mlnode_gpu_temp_core_celsius`, `mlnode_gpu_temp_hbm_celsius`,
`mlnode_gpu_power_draw_watts`, `mlnode_gpu_power_limit_watts`,
`mlnode_gpu_sm_clock_hertz`, `mlnode_gpu_sm_clock_max_hertz`,
`mlnode_gpu_clocks_event_reasons`, `mlnode_gpu_memory_used_bytes`,
`mlnode_gpu_memory_free_bytes`, `mlnode_gpu_ecc_dbe_total`,
`mlnode_gpu_pcie_replay_total`,
`mlnode_gpu_xid_events_total{gpu_index,xid}` (counter of XID critical errors
observed via the NVML event API since process start; no events ⇒ no series),
`mlnode_host_cpu_busy_ratio`, `mlnode_host_cpu_steal_ratio`,
`mlnode_host_memory_used_bytes`, `mlnode_host_memory_limit_bytes` (cgroup
v2 with a v1 fallback; no limit configured ⇒ no series),
`mlnode_host_hf_cache_free_bytes`.

Note: `utilization.gpu` is deliberately NOT exported as a saturation metric
(memory-bound decode and PoC both show 100% with half-idle SMs).

### Service series

- `mlnode_config_info{model_name, dtype, replicas, max_num_seqs,
  max_model_len, tensor_parallel_size, pipeline_parallel_size}` — an empty
  numeric label value means the argument was not passed explicitly and the
  effective vLLM default is unknown to mlnode; consumers cannot compute the
  saturation ratio for such nodes.
- `mlnode_version_info{version}`.
- `mlnode_source_scrape_timestamp_seconds{source}` with
  `source="vllm"|"nvml"|"host"`; the vLLM source additionally carries a
  `replica` label so a failed replica scrape is individually visible.
- `gonka_metrics_schema_info{version}`.

## Changelog

- v1 (2026-07-10): initial schema.
- v1 (2026-07-14): exporter-owned families now carry `# HELP`/`# TYPE`
  metadata (counters typed as counters end-to-end); no series change.
