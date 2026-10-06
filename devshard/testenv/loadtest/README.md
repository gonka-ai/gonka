# DevShard load testing

This directory contains declarative, reproducible load scenarios for the local
Docker DevShard test environment. A run starts an isolated stack, sends traffic
through the Gateway, evaluates the assertions declared by the scenario, and
writes diagnostic artifacts for investigation.

The load framework is intended to exercise DevShard and its lifecycle under
load. It is not a benchmark of real model quality or production capacity.

## Quick start

From the repository root, run the default normal-load scenario:

```bash
make -C devshard/testenv loadtest SCENARIO=normal-load
```

The Make target accepts the scenario name without the `.yaml` suffix. It builds
the testenv DevShard binary and invokes the same runner as the direct command.

To run a scenario directly, change to `devshard/testenv`:

```bash
cd devshard/testenv
go run ./cmd/loadtest \
  -scenario loadtest/scenarios/normal-load.yaml
```

Run artifacts are written under
`devshard/testenv/loadtest/results/<timestamp>/`. Use `-output` to select a
different directory and `-keep-stack` to leave the Docker stack running for
post-run inspection.

## Memory and CPU comparison

`long-escrow` runs ten minutes at 8 RPS without escrow rotation. Use unique
synthetic requests for the first comparison; dataset replay can hit the response
cache and spread traffic across several model escrows.

Every scenario collects process metrics and gateway escrow state before load,
during load and drain, and once at the end. The default interval is one second;
use `-metrics-interval 2s` with the direct runner to change it (minimum 100ms).
Sampling rounds do not overlap; slow probes can reduce the achieved frequency.
Each sample records its timestamp and phase. Host probes run inside each
versiond container and scrape that replica's devshardd, including both HA
siblings. Gateway metrics describe devshardctl. Container-wide memory is not
used in place of process memory.

The console summary includes baseline/final RSS and Go heap, separate
workload/all-phase sampled peaks, process-lifetime RSS high-water mark from
Linux `/proc`, CPU seconds, allocated bytes, GC count and pause time, goroutines,
and escrow nonce/history growth.
CPU per 1,000 completed client requests includes the collection period through
drain. PID and process start time distinguish restarts; counter deltas never
cross process lifetimes. The Go heap samples do not force GC and are not a
post-GC live-heap measurement. RSS high-water mark may include startup; sampled
peaks may miss short spikes. Missing metrics are reported as unavailable, and
collection errors are displayed even when workload assertions pass.

`metrics.jsonl` contains the time series, process identities, and build-info
labels where exported. `metrics-summary.json` contains per-process and per-escrow
aggregates, collection errors, Git revision/dirty status, runner Go version,
configured versions, and the mounted host binary SHA-256 when readable.
`summary.json` also includes per-minute p50, p95, p99, completed RPS, failures
and drops, grouped by request start time.
Latency includes attempted requests, with generator drops excluded. Gateway
`diffs_bytes` measures retained serialized payload, not persisted journal size
or heap usage. All gateway escrow IDs are sampled, including rotated escrows.

## Scenario file

Each scenario is a self-contained YAML file. The main sections are:

| Section | Purpose |
| --- | --- |
| `schema_version`, `scenario`, `seed` | Scenario identity and reproducible random selection. |
| `topology` | Participants, storage mode, chain limits, and Mock ML nodes. |
| `workload` | Duration, concurrency, traffic model, and request template. |
| `gateway` | Gateway redundancy and optional escrow rotation settings. |
| `assertions` | Checks that determine whether the run passes. |
| `drain_timeout` | Deadline for post-workload DevShard cleanup checks. |

Minimal example:

```yaml
schema_version: v1
scenario: normal-load
seed: 42

topology:
  versiond_mode: multi
  storage: per_participant
  participants: 3
  chain:
    escrow_amount: 2000000000000000
    max_nonce: 100000
  mock_ml:
    allocator: round_robin
    nodes:
      - name: mock-openai-0
        profile: fast
      - name: mock-openai-1
        profile: fast

workload:
  max_in_flight: 2
  duration: 30s
  traffic:
    type: closed_loop
  request:
    model: test-model
    stream: false
    max_tokens: 64
    messages:
      - role: user
        content: "normal-load request"

assertions:
  requests:
    http_status: 200
    terminal_outcome: completed
    error_rate: 0
  devshard:
    no_orphaned_work: true
    max_ghost_rate: 0.02
  mock_ml:
    require_each_node_used: true

drain_timeout: 30s
```

There is no separate `thresholds` or fail-fast configuration. A run evaluates
all enabled assertions and prints every result before returning a failure.
`assertions.requests.error_rate` is the maximum permitted client error rate.

## Traffic profiles

Traffic is configured inline under `workload.traffic`. If `type` is omitted,
the runner uses `closed_loop` for compatibility with the original scenarios.

| Type | Required fields | Behavior |
| --- | --- | --- |
| `closed_loop` | None | Starts a new request only after a worker has received and fully read the previous response. `max_in_flight` controls the number of workers. |
| `constant` | `rps` | Sends at a constant target rate. |
| `ramp` | `from_rps`, `to_rps` | Increases or decreases the target rate linearly over the workload duration. |
| `sine` | `min_rps`, `max_rps`, `period` | Oscillates between the minimum and maximum rate. |
| `spikes` | `base_rps`, `spike_rps`, `spike_duration`, `interval` | Sends at `spike_rps` at the beginning of each interval and at `base_rps` otherwise. |
| `stages` | `stages[].duration`, `stages[].rps` | Runs a fixed sequence of rates. Stage durations must add up exactly to `workload.duration`. |

Examples:

```yaml
# Constant rate.
traffic:
  type: constant
  rps: 8
```

```yaml
# Linear ramp from 1 to 20 requests per second.
traffic:
  type: ramp
  from_rps: 1
  to_rps: 20
```

```yaml
# Ten seconds at 50 RPS every minute, otherwise 2 RPS.
traffic:
  type: spikes
  base_rps: 2
  spike_rps: 50
  spike_duration: 10s
  interval: 1m
```

For rate-based profiles, `max_in_flight` remains a concurrency cap. If all
slots are occupied when the scheduler wants to send a request, that request is
recorded as dropped by the generator. This is reflected in the client summary
and in the `requests.error_rate` assertion details.

## Mock ML profiles

Each Mock ML node refers to a YAML file in `profiles/` by its `profile` name.
The profile controls response timing and failure behavior:

| Profile | Behavior |
| --- | --- |
| `fast` | Short time to first token and no delay between later chunks. |
| `realistic` | Fixed moderate time to first token and token interval. |
| `slow` | Longer first-token and per-token delays. |
| `failing` | Delayed responses with a configured HTTP failure rate and status. |
| `timeout` | Requests are accepted but intentionally never complete. |

Profile fields currently supported:

```yaml
schema_version: v1
profile: realistic
ttft: 250ms
token_interval: 20ms
workers: 100
queue: 500
failure_rate: 0
http_status: 503
hang: false
```

`ttft` is applied before the first response chunk. `token_interval` controls
the delay between later chunks. `workers` and `queue` bound the Mock ML
capacity. `failure_rate` and `http_status` are used by failure profiles;
`hang: true` creates a timeout-style node.

To add a profile, create `profiles/<name>.yaml` with `schema_version: v1`, the
same value in `profile`, valid duration fields, a positive `workers` value, and
a non-negative `queue` value. Reference it from a scenario node:

```yaml
mock_ml:
  allocator: round_robin
  nodes:
    - name: mock-openai-0
      profile: realistic
```

## Captured request datasets

The optional `-load-dataset` flag accepts a JSONL capture containing both the
client request and the expected Mock ML response:

```bash
cd devshard/testenv
go run ./cmd/loadtest \
  -scenario loadtest/scenarios/normal-load.yaml \
  -load-dataset loadtest/resources/devshard-load-samples-250-sanitized-final.jsonl
```

Each line contains `model`, `stream`, `temperature`, `max_tokens`,
`messages`, and `response`. The runner loads the complete file into memory,
selects samples pseudo-randomly with replacement using `seed`, and registers
every unique captured model before starting the Gateway. There is no separate
model flag.

Mock ML does not choose an independent response. It looks up the response by
the selected request fingerprint, so concurrent and speculative requests stay
associated with the correct fixture. Request selections and fingerprints are
recorded in `requests.jsonl`; replay hits and misses are included in
`ml-stats.json` and the terminal summary.

Dataset replay can create one initial escrow per unique model. Keep this in
mind when interpreting escrow IDs and when designing an escrow-rotation run.

## Gateway and drain settings

The Gateway speculative-cleanup grace period can be overridden per scenario:

```yaml
gateway:
  redundancy:
    secondary_wait_after_winner: 30s

drain_timeout: 60s
```

`secondary_wait_after_winner` defaults to `10m`. It controls how long the
Gateway lets speculative losers finish after a winner is selected.
`drain_timeout` is the load-test deadline for the DevShard orphan check and
should be long enough for the configured Gateway grace period when cleanup is
part of the scenario.

For escrow rotation, configure the Gateway explicitly:

```yaml
gateway:
  escrow_rotation:
    enabled: true
    settlement_enabled: true
    pre_poc_blocks: 1
    nonce_deactivation_limit: 200
    temp_count: 1
    target_count: 1
    amount: 5000000000
    private_key_env: DEVSHARD_PRIVATE_KEY
```

The matching assertion compares the initial escrow IDs with the Gateway's
`/v1/admin/devshards` state and requires an additional escrow ID:

```yaml
assertions:
  escrow_rotation:
    require_new_escrow: true
```

The assertion only passes if the run creates enough nonce activity to cross
`nonce_deactivation_limit`. A ten-minute run at 1 RPS may complete normally
without rotating an escrow, so a rotation test must choose its rate and limit
together.

When `assertions.devshard.no_orphaned_work` is enabled, the runner polls
`/v1/debug/inferences` until every non-ghost record has a terminal status
(`finished`, `validated`, `invalidated`, or `timed_out`). The DevShard record
count does not have to equal the number of client requests: cache hits,
speculative attempts, and ghost probes can make those counts different. An
empty successful debug snapshot is also considered clean.

## Assertions

Assertions are declared in the scenario and are evaluated after the workload:

| Assertion | Purpose |
| --- | --- |
| `requests.http_status` | Requires the configured HTTP status for successful client requests. |
| `requests.terminal_outcome` | Requires the configured client outcome, usually `completed`. |
| `requests.error_rate` | Limits HTTP failures and generator drops. |
| `mock_ml.require_each_node_used` | Requires every configured Mock ML node to receive an allocation. |
| `devshard.no_orphaned_work` | Checks terminal DevShard state and the configured ghost-rate limit. |
| `escrow_rotation.require_new_escrow` | Requires a new escrow after rotation. |

The final report prints each check with its expected value, actual value, and
diagnostic details. Failed checks are also listed in `assertions.json`.

## Artifacts

Each run writes a timestamped directory containing the scenario copy, request
records, summaries, state snapshots, and logs:

| Artifact | Contents |
| --- | --- |
| `run.yaml` | Exact scenario used for the run. |
| `summary.json` | Client workload counts, latency, duration, and per-minute windows. |
| `metrics.jsonl` | Process memory/CPU/GC and per-escrow state time series. |
| `metrics-summary.json` | Sampled peaks, counter deltas, initial/final state, and collection errors. |
| `requests.jsonl` | One record per generated request and response outcome. |
| `assertions.json` | All evaluated assertions and `failed_assertions`. |
| `ml-stats.json` | Per-node allocations, responses, failures, timeouts, and replay counters. |
| `gateway-inferences.json` | Gateway inference records collected for diagnosis. |
| `gateway-state.json` | Gateway diff, nonce, pending transaction, and applied-key counts. |
| `compose-pre-drain.log` | Container logs captured before the drain check. |
| `compose.log` | Final container logs captured after the drain check. |
| `failures/` | Individual failed-request bundles, subject to the runner's artifact cap. |

The two Compose log snapshots are intentional: the pre-drain snapshot shows
the state at the point where cleanup begins, while the final snapshot shows
what happened during and after the drain.

## Layout

| Path | Purpose |
| --- | --- |
| `scenarios/` | Runnable YAML scenarios. |
| `profiles/` | Mock ML behavior profiles. |
| `resources/` | Optional captured request datasets. |
| `traffic.go` | Traffic-profile validation and request scheduling. |
| `results/` | Local artifacts from completed and failed runs; not committed. |
