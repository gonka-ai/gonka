# ADR: ML-node monitoring — metrics and interface contract v1

Status: **accepted** (team-approved 2026-07-10)
Related: [operator guide](../observability.md), [decision log](../decision-log.md)

## Context

ML-node monitoring architecture (v0.5): an exporter inside mlnode → a
collector + public endpoint in dapi → consumers store data on their side.
This ADR fixes the Phase 0 contract: the final allowlist, formats,
off-behavior, numeric limits and the schema versioning rules.

## Decisions

### D1. Allowlist v1 (verified against live vLLM 0.20.0 and v0.23.0 sources)

Verification performed 2026-07-08:

- live `/metrics` of vLLM **0.20.0** (production ML node, Kimi-K2.6): every
  family below present;
- vLLM sources **v0.20.0 vs v0.23.0** (`vllm/v1/metrics/*.py`): metric name
  sets identical (42/42), label names identical — no version skew for the
  allowlist. A live 0.23 run is deferred to the Phase 4 E2E stand (Open-1).

**vLLM P0** (labels: `model_name`, `replica`): `vllm:num_requests_waiting`,
`vllm:num_requests_running`, `vllm:kv_cache_usage_perc`,
`vllm:num_preemptions_total`, `vllm:time_to_first_token_seconds`,
`vllm:request_queue_time_seconds`, `vllm:prompt_tokens_total`,
`vllm:generation_tokens_total`, `vllm:request_success_total`
(`finished_reason`: stop|length|abort|error|repetition — captured live).

**vLLM P1**: `vllm:inter_token_latency_seconds`,
`vllm:e2e_request_latency_seconds`, `vllm:prefix_cache_queries_total`,
`vllm:prefix_cache_hits_total`, `vllm:request_prompt_tokens`,
`vllm:request_generation_tokens`, `vllm:iteration_tokens_total`,
`vllm:cache_config_info`.

Version-gated families (`*_by_reason`, `*_by_source`, `mm_cache_*`,
`external_prefix_cache_*`, `estimated_*`) are **out of v1** (present in the
0.20 fork, no upstream guarantee).

**GPU/host P0** (prefix `mlnode_`, labels `gpu_index`, `gpu_model`):
core + HBM temperature (sensor-gated), power draw + enforced limit,
SM clock + max SM clock, clocks-event-reasons bitmask, VRAM used/free,
ECC DBE aggregate, PCIe replay counter,
`mlnode_gpu_xid_events_total{gpu_index,xid}` (see D9),
host CPU busy/steal ratios, memory used/limit from the cgroup
(v2 with v1 fallback), HF-cache free disk space.
No sensor ⇒ no series (never a fake zero/N-A value).

**Gonka-specific**: `mlnode_config_info` (P0; labels `model_name`, `dtype`,
`replicas`, `max_num_seqs`, `max_model_len`, `tensor_parallel_size`,
`pipeline_parallel_size` — empty numeric value = argument not passed
explicitly, effective vLLM default unknown to mlnode),
`mlnode_version_info` (P2).

**Source timestamps**: `mlnode_source_scrape_timestamp_seconds{source}` is a
mandatory part of the schema. It records successful source collection, not
scheduler progress. The vLLM source carries an additional `replica` label so
one failed replica scrape out of N is individually detectable.

### D2. `model_name` normalization (added after live verification)

In production the vLLM `model_name` label contains the **local HF-cache
path** (`/root/.cache/huggingface/hub/models--org--name/snapshots/<sha>`) —
a placement detail (violates invariant 5). The exporter MUST rewrite
`model_name` to the served name (`org/name`). The internal vLLM `engine`
label is dropped; replica identity is the `replica="0..N-1"` label
(positional instance index). A Phase 1 unit test enforces the absence of
paths, hostnames, IPs and ports in label values.

### D3. mlnode exporter response format

- `GET /api/v1/metrics` on the mlnode API port (the dapi collector reaches
  it via `Node.PoCUrl()`).
- Format: **Prometheus text exposition v0.0.4**,
  `Content-Type: text/plain; version=0.0.4`.
- Content: exactly the allowlist (filter enforced in code); python-client
  `*_created` noise stripped; replicas fanned out over healthy proxy
  backends, each labeled `replica`.
- Implementation renders filtered text directly — no prometheus_client
  global registry (it is not a declared dependency and the exporter relays
  foreign metrics rather than owning them).

### D4. `GONKA_METRICS` behavior

- Values: `full` (default) | `off`; anything else is treated as `full` with
  a startup warning. Read via `os.getenv` in the handler (repo idiom).
- `off` ⇒ **HTTP 404**. The route is always registered; a disabled node is
  indistinguishable from a pre-metrics image, which keeps the dapi
  upgrade path trivial (Phase 2 AC).
- Honesty mechanics: the allowlist lives in the repository; the mlnode
  startup log states the export mode; vLLM replicas run with
  `VLLM_NO_USAGE_STATS=1`.

### D5. dapi public endpoint format

- **Aggregated** endpoint `GET /v1/mlnodes/metrics` on the public server,
  exposed through dedicated exact-match nginx locations rather than the generic
  `/v1/*` one (see D6).
- Response: Prometheus text exposition; all ML-node series of this network node
  with an added `node_id` label. Scrape freshness is conveyed by the exporter's
  own `mlnode_source_scrape_timestamp_seconds` plus `mlnode_up`; dapi does not
  stamp individual samples.
- Three states: `mlnode_up{node_id} 1` scraped fine; `0` reachable but the
  scrape failed or exceeded a ceiling; a node answering 404 (metrics off, or an
  image predating the exporter) is absent entirely, with no zero stubs and no
  up-series. The last case is what makes a dapi-first rollout safe.

**As-built deviation.** This ADR specified a background poller (45 s cadence,
≤5 min buffer, per-sample timestamps). The implementation is a pull-through
cache instead: a merged snapshot is cached 10 s and rebuilds are
single-flighted, so N external scrapers still cost the ML nodes one fan-out per
TTL, and there is no polling while nobody is looking.
- Rejected alternative: per-node endpoints (consumers would need discovery;
  one scrape per network node is the point).

### D6. Rate limit

A dedicated nginx zone (`metrics_zone`, per-IP) rather than application
middleware ⇒ 429 over the limit. Defaults are set in the proxy entrypoint and
tunable via `METRICS_RATE_LIMIT_RPM` and `METRICS_BURST`.

**As-built deviation.** This ADR specified Echo `RateLimiter` middleware at
1 req/s burst 5. Enforcing it in the proxy instead keeps scraping consumers out
of the `api_zone` budget shared with inference and API clients, so exhausting
one cannot affect the other — and leaves a single source of truth for limits.
The in-app limiter was dropped accordingly. Residual risk: a deployment that
strips the shipped proxy runs the endpoint unlimited; compute stays bounded
regardless by the cached single-flight fan-out.

### D7. Schema version and change rules

- Series `gonka_metrics_schema_info{version="1"} 1` in the exporter output.
- Any change to the allowlist or label semantics = version bump + an entry
  in the changelog section of `docs/observability.md` in the same PR.
- Adding a series is minor-compatible (still logged in the changelog);
  removing/renaming = major bump.

### D8. dapi libraries (Go)

- Parsing node responses: `github.com/prometheus/common/expfmt` (already in
  the dependency tree as indirect).
- Exposition: `expfmt` encoder (no client_golang registry — dapi relays
  foreign metrics).
- Rate limit: nginx `metrics_zone` in the proxy (see D6).
- Kill switch: `ApiConfig` field (koanf) ⇒
  `DAPI_API__MLNODE_METRICS_DISABLED=true`, evaluated per request so it takes
  effect without a restart. One switch, not two: as built, the collector and
  the endpoint are one component.

### D9. XID mechanism (closes Open-2)

XID critical errors are captured via the **NVML event API**
(`nvmlDeviceRegisterEvents` + a listener thread) and exported as
`mlnode_gpu_xid_events_total{gpu_index,xid}` (no events ⇒ no series).
Verified working in all five target environments, including unprivileged
rented containers; `dmesg` is readable only on bare hosts and was rejected.
The listener is joined and its event set freed before `nvmlShutdown()`.

## Open questions

- **Open-1**: a live allowlist check against vLLM 0.23 — on the Phase 4
  stand (source-level parity already verified; low risk).
- **Open-3** (= architecture question 2): an optional
  node_exporter + dcgm-exporter profile for sidecar-capable environments —
  out of v1.

## Verification artifacts

Live 0.20 snapshot, v0.20/v0.23 source diffs, the production B300 exporter
response, the 5/5 NVML environment matrix and the differential overhead
measurement (0.205% of a core vs the <1% AC) are attached to the
introducing PR.
