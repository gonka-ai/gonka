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
`mlnode_gpu_xid_events_total{gpu_index,xid}` (see D11),
host CPU busy/steal ratios, memory used/limit from the cgroup
(v2 with v1 fallback), HF-cache free disk space.
No sensor ⇒ no series (never a fake zero/N-A value).

**Gonka-specific**: `mlnode_config_info` (P0; labels `model_name`, `dtype`,
`replicas`, `max_num_seqs`, `max_model_len`, `tensor_parallel_size`,
`pipeline_parallel_size` — empty numeric value = argument not passed
explicitly, effective vLLM default unknown to mlnode),
`mlnode_version_info` (P2).

**Source timestamps**: `mlnode_source_scrape_timestamp_seconds{source}` is a
mandatory part of the schema (invariant 4: a hung vLLM is visible by data
age). The vLLM source carries an additional `replica` label so one hung
replica out of N is individually detectable.

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

- **Aggregated** endpoint `GET /v1/metrics` on the public Echo server
  (:9000, exposed through the existing nginx `/v1/*` location).
- Response: Prometheus text exposition; all ML-node series of this network
  node with an added `node_id` label and **per-sample timestamps** (ms) set
  to the collection time — a silent node is served with its old timestamp.
- A node answering 404/timeout/`off` is simply absent (no zero stubs).
- Rejected alternative: per-node endpoints (consumers would need discovery;
  one scrape per network node is the point).

### D6. Rate limit

Echo `RateLimiter` middleware (in-memory, per-IP — existing repo pattern):
**rate 1 req/s, burst 5, expires 3 min** ⇒ 429. Rationale: the useful
consumer frequency equals the buffer update cadence (45 s); 1 rps leaves
headroom for retries and several consumers behind one NAT. An optional
stricter nginx zone is a Phase 2 deliverable only if needed.

### D7. dapi buffer ceilings

- Only the **latest** snapshot per node_id is stored.
- Max **2 MiB** of filtered text per node_id (read limit; larger ⇒ snapshot
  rejected, error counter logged).
- Max **5 000 series** per node_id (protection from a malicious/broken node;
  honest maximum ≈ 1–2 k series per replica).
- Snapshot TTL **5 min** (evicted on timer and on read).
- Collector RSS budget: ≤ **64 MiB** at 16 nodes.
- Polling: **45 s** ticker, per-node `context.WithTimeout` **10 s**, an
  isolated worker goroutine (MLNodeBackgroundManager pattern) with a
  WaitGroup fan-out; never on the broker command queue (invariant 6).

### D8. Timestamps

- mlnode exporter: gauge `mlnode_source_scrape_timestamp_seconds{source}`
  (unix seconds, float) per source; `source="vllm"` also carries `replica`.
- dapi: per-sample timestamps (unix ms) in the exposition output = the
  moment of successful collection from the node.

### D9. Schema version and change rules

- Series `gonka_metrics_schema_info{version="1"} 1` in the exporter output.
- Any change to the allowlist or label semantics = version bump + an entry
  in the changelog section of `docs/observability.md` in the same PR.
- Adding a series is minor-compatible (still logged in the changelog);
  removing/renaming = major bump.

### D10. dapi libraries (Go)

- Parsing node responses: `github.com/prometheus/common/expfmt` (already in
  the dependency tree as indirect).
- Exposition: `expfmt` encoder (no client_golang registry — dapi relays
  foreign metrics).
- Rate limit: Echo middleware (already used in the repo).
- Kill switches: `ApiConfig` fields (koanf) ⇒
  `DAPI_API__METRICS_COLLECTOR_ENABLED`, `DAPI_API__METRICS_ENDPOINT_ENABLED`
  (default `true` from the Phase 5 release); gated in `main.go`.

### D11. XID mechanism (closes Open-2)

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
