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

## Catch-up scenario

`catch-up` runs three participants at 16 RPS for five minutes. At 90 seconds
it freezes `versiond-2` for 60 seconds, then resumes it:

```bash
make -C devshard/testenv loadtest SCENARIO=catch-up
```

```yaml
topology:
  participants: 3

workload:
  host_faults:
    - host: versiond-2
      mode: pause
      start_after: 90s
      duration: 60s
```

Put this configuration alongside the other topology and workload fields in a
complete scenario. Three participants create four containers: `versiond-0/1` share one
identity and database; `versiond-2/3` are independent participants. The target
must be a generated versiond service and `participants` must be explicit.

The runner uses Compose `pause`/`unpause`, freezing every process in the target
container while traffic and the mock chain continue. This retains memory,
connections, and storage; it models a stalled host. Start times are relative to
the workload start. Pauses must finish within the workload. Faults for one host
must be ordered and not overlap; different hosts run independently.

Host faults only inject the scheduled pause and resume. Each fault produces a
`host_faults.<index>.<host>.applied` assertion checking that both commands
succeeded. Host catch-up validation is separate from fault injection and is not
performed here.

`host-faults.json` records configured windows, pause/resume timestamps, and
command failures. Cancellation and error paths attempt unpause with a separate
cleanup context, including when `-keep-stack` is set.

The trial scenario permits a 5% client error rate to account for requests that
hit the frozen participant. Adjust this assertion separately from the
fault schedule when testing stricter availability requirements.

The catch-up scenario collects concurrent CPU profiles of gateway and every
host at 45–75s (baseline), 85–205s (pause and resume), and 240–270s (later
steady state). The middle profile starts before the 90s pause so the target's
profiler is already running when it freezes and can capture work immediately
after unpause at approximately 150s. A frozen process produces no CPU samples;
its middle profile combines roughly five seconds before pause and fifty-five
seconds after resume. Other targets remain active throughout that window.
Compare hot functions and sample proportions across windows; total CPU sample
counts cover different amounts of active time. The middle window includes
both the pause and resumed activity rather than isolating catch-up alone.

## Container logs

Heap profiles and goroutine stack dumps can be scheduled independently of CPU
windows, with offsets measured from workload start:

```yaml
diagnostics:
  heap_profiles:
    - start_after: 152s
      targets: [gateway, versiond-2, versiond-1]
  goroutine_profiles:
    - start_after: 152s
      targets: [gateway, versiond-2, versiond-1]
```

Omitting `targets` captures Gateway and all configured hosts. Each list must
have distinct increasing offsets within the workload. Hosts use their child's
private admin listener. A paused container cannot be sampled; errors are saved
in `profile-snapshots.json` along with actual start/finish times and file paths.
Each snapshot request has a 15-second deadline and runs independently of other
scheduled snapshots. Collection errors are diagnostic and do not fail workload
assertions.

Heap files in `profiles/` use binary pprof format; goroutine files are text
stacks (`debug=2`). Heap capture does not force GC or change the allocation
sampling rate. Use `go tool pprof -sample_index=inuse_space FILE` for retained
memory, or `-sample_index=alloc_space -base BASELINE FILE` for allocation
differences. Heap data is sampled and may lag the live memory counters until
GC. Goroutine dumps reveal active and waiting stacks at the snapshot time.

To collect lock-contention diagnostics together with every CPU window, enable
the explicit scenario flag:

```yaml
diagnostics:
  contention_profiles: true
  cpu_profiles:
    - start_after: 0s
      duration: 30s
```

The flag requires at least one CPU window. At the end of each window the
runner captures a goroutine dump (`debug=1`) and the cumulative mutex and
block pprof profiles for the gateway and every configured host. Runtime
sampling is enabled only for that scenario, so the default (`false` or
omitted) keeps ordinary performance runs unchanged. Captures are written to
`profiles/` and indexed by `contention-profiles.json`; collection failures are
diagnostic artifacts and do not fail workload assertions.

`catch-up` captures heap at 75s (baseline), 152s and 160s (resumed backlog),
180s (after burst), 225s (after quarantine), and 270s (steady workload). Stacks
are captured at 152s and 160s. Targets are Gateway, `versiond-2`, and healthy
`versiond-1`. These offsets assume the configured pause at 90–150s; quarantine
expiration is error-driven and may shift between runs.

Container log collection is configured separately from CPU profiling:

```yaml
diagnostics:
  logs:
    tail: all
```

`diagnostics.logs.tail` defaults to `5000` lines **per container**. Set a positive
integer to choose another limit, or `all` to collect all available container
logs. The setting applies to `compose-pre-drain.log`, final `compose.log`, and
log collection on error paths. Logs stream directly to files. `catch-up` uses
`all` to retain quarantine and recovery events from the beginning of the run.
Full collection still depends on logs retained by Docker's logging driver; the
runner gives every container of a load-test stack `json-file` with `1g` x 3, so
a busy service keeps the start of the run.

## Miss-rate and catch-up scenario

`missrate-catch-up` pauses `versiond-2` for 30 seconds during a 90-second,
4 RPS workload. It uses short refusal and execution timeouts so that the
outage can produce protocol-level miss and timeout outcomes during the run:

```bash
make -C devshard/testenv loadtest SCENARIO=missrate-catch-up
```

The scenario checks both recovery and accounting results. In addition to the
client error rate and DevShard orphan/ghost checks, it requires at least one
protocol miss, at least one applied timeout outcome, and consistent accounting
cross-checks for the paused participant:

```yaml
protocol:
  refusal_timeout: 2s
  execution_timeout: 5s

workload:
  host_faults:
    - host: versiond-2
      mode: pause
      start_after: 10s
      duration: 30s

assertions:
  accounting:
    target_host: versiond-2
    min_protocol_misses: 1
    min_timeouts_applied: 1
    require_cross_checks: true
```

The runner reads protocol accounting from the DevShard stats endpoint and
writes `accounting.json` in the result directory. The final summary includes
assigned nonces, protocol misses, miss rate, applied timeouts, host-missed
counts, and cross-check errors. CPU, heap, and goroutine profiles cover the
healthy period, the pause/backlog period, and the recovery period so the
performance impact of catch-up can be compared with the protocol outcome.

## Memory and CPU comparison

`long-escrow` runs ten minutes at 8 RPS without escrow rotation. Use unique
synthetic requests for the first comparison; dataset replay can hit the response
cache and spread traffic across several model escrows.

`long-escrow-64rps-60m` offers 64 RPS for one hour, keeps the concurrency cap at
64, and raises both chain and gateway admission nonce limits to 1,000,000. Its target is 230,400 requests;
generator drops are reported when concurrency is exhausted. Run it with
`make -C devshard/testenv loadtest SCENARIO=long-escrow-64rps-60m` from the
repository root. The runner's automatic overall timeout is workload + drain +
ten minutes, with a minimum of twenty minutes. The direct runner accepts
`-timeout 80m` to override it.

`fresh-escrow-64rps-2m` starts an isolated stack with fresh storage, offers
64 RPS for two minutes, and keeps the concurrency cap at 64. It reports latency
in ten-second windows and collects concurrent CPU profiles of gateway and every
host replica during 5–35s and 80–110s of the workload. Capture all replicas so
the active HA sibling is included. Host profiles use the child's private admin
listener inside its container; the versiond session proxy does not serve pprof.
Run from the repository root:

```bash
make -C devshard/testenv loadtest SCENARIO=fresh-escrow-64rps-2m
```

`fresh-escrow-16rps-2m` uses the same fresh-stack configuration, concurrency
cap, ten-second report windows, and CPU profile windows, offering 16 RPS for
two minutes (1,920 scheduled requests). Use it to compare early degradation
against the 64 RPS run:

```bash
make -C devshard/testenv loadtest SCENARIO=fresh-escrow-16rps-2m
```

`fresh-escrow-32rps-2m` offers 32 RPS for two minutes (3,840 scheduled
requests), using the same configuration and diagnostics as the 16 RPS scenario.
Run from the repository root:

```bash
make -C devshard/testenv loadtest SCENARIO=fresh-escrow-32rps-2m
```

`long-escrow-32rps-30m` starts a fresh isolated stack and offers 32 RPS for
30 minutes (57,600 scheduled requests), with `max_in_flight: 64` and escrow
rotation disabled. Both nonce limits remain 1,000,000. Latency is reported in
one-minute windows; process metrics are sampled every second. CPU profiles of
gateway and all host replicas cover 0–30s, 10m–10m30s, 20m–20m30s, and
29m30s–30m. This provides a baseline plus snapshots at approximately 10, 20,
and 30 minutes while keeping every profile window inside the workload. Drain is
two minutes; the automatic overall timeout is 42 minutes. Run from the
repository root:

```bash
make -C devshard/testenv loadtest SCENARIO=long-escrow-32rps-30m
```

`long-escrow-32rps-30m-no-snapshots` is the diagnostic A/B variant of the
30-minute run. It keeps the same `32 RPS` and `max_in_flight: 64`, but disables
runtime DevShard host snapshots through a test-only switch. Use it only to
measure whether snapshot preparation contributes to late latency growth; it
does not represent production recovery or durability behavior. Run it with:

```bash
make -C devshard/testenv loadtest SCENARIO=long-escrow-32rps-30m-no-snapshots
```

`diagnostics.cpu_profiles` defines ordered, non-overlapping `start_after` and
`duration` windows within the workload (profile durations are whole seconds).
Profiles are saved under `profiles/`, with capture times and errors in
`cpu-profiles.json` and console output. Profiling errors are diagnostic and do
not change workload assertions. CPU sampling introduces some overhead; use
matching profile windows when comparing runs. To inspect a captured profile:

```bash
go tool pprof -top <results-directory>/profiles/gateway-window-01.cpu.pprof
```

`workload.report_interval` changes the latency window size; its default remains
one minute. Windows group requests by start time, so they include the eventual
response latency even when that response arrives after the window ends.

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
drain. PID and Linux process start ticks distinguish restarts; counter deltas never
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
| `workload` | Duration, concurrency, traffic model, host faults, and request template. |
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

`topology.chain.validation_threshold` sets the replay similarity bar the chain
hands the hosts for every model, as a chain decimal (`{value: 958, exponent: -3}`
is mainnet's 0.958); it must lie between 0 and 1. When omitted, the testenv
default 0.50 stays, which no logprob mismatch can fail.

`topology.chain.escrows` is how many escrows of the request model the Gateway
holds during the load, as on the network: the first is the configured one, the
runner creates the rest through `POST /v1/admin/escrows` before the load and
waits up to three minutes for `/v1/admin/devshards` to list them. The Gateway
spreads the requests over them by load. 0 and 1 keep the single escrow; at most 16.

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
| `overclaim` | One execution in five reports usage of 3n+7 tokens for n sent. |
| `wrong-answer` | One execution in five answers with logprobs the replay does not confirm. |
| `empty-stream` | Every execution answers with no content. |

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

`chaos` makes the node a dishonest executor. Each execution gets one answer
shape drawn by `weights` from the mock's chaos actions (`ok`, `odd_usage`,
`wrong_answer`, `empty` and others), seeded by `seed`; a validator's replay is
always answered honestly, so any shape other than `ok` is one the protocol
should catch:

```yaml
chaos:
  seed: 7
  weights:
    ok: 4
    odd_usage: 1
```

Every Mock ML node of a load-test stack reports `usage.completion_tokens` as the
tokens it sent, as vLLM does, so a host's Finish can be compared with it.

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

A node may also set `host` and `max_concurrent`:

```yaml
    - name: mock-openai-3
      profile: overclaim
      host: versiond-3
      max_concurrent: 4
```

`host` binds the node to one versiond container, as a participant's dapi owns
its nodes: mock-dapi hands that container only its own nodes, and the other
containers share the unbound ones (or every node, if all are bound). It must be
a `versiond-N` of an explicit `topology.participants`. Bind a node to every
container that executes, or that container draws from the others' nodes.

`max_concurrent` caps the AcquireMLNode locks mock-dapi hands out on the node
at once, as the dapi broker refuses a node holding its `MaxConcurrent` locks; a
release frees a slot. 0 (the default) is unlimited.

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

Participant quarantine durations can be shortened for recovery tests:

```yaml
gateway:
  participant_throttle:
    transport_failure_quarantine: 70s
    http_quarantine: 70s
```

When omitted, the runner leaves each Gateway setting unchanged. Defaults are
`30m` for `transport_failure_quarantine` and `60m` for `http_quarantine` (HTTP
429/503). Each duration must be at least `1ms`; the runner sends them to the
existing admin settings `participant_throttle.transport_failure_quarantine_ms`
and `participant_throttle.http_quarantine_ms` respectively.
`catch-up` uses `70s` for both so the host can become eligible during the run.
The timer starts when an error triggers quarantine, not when the
container resumes. Further failures can quarantine the host again. Expiration
does not validate that the host has caught up. `empty_stream_quarantine`
(Gateway default `30m`) goes to `participant_throttle.empty_stream_quarantine_ms`;
`usage-challenge-wave` cuts it to `1s` so hosts that served an empty stream keep
winning. The stalled-winner quarantine is configured separately in the Gateway.

For escrow rotation, configure the Gateway explicitly:

```yaml
gateway:
  escrow_rotation:
    enabled: true
    settlement_enabled: true
    pre_poc_blocks: 1
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

The assertion only passes if the run creates enough nonce activity to cross the
chain-derived active nonce limit. Set `topology.chain.max_nonce` low enough for
the workload to exercise rotation, while keeping enough headroom for protocol
finalization and settlement. This keeps the test aligned with the same
`DevshardEscrowParams.max_nonce` that hosts enforce.

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
| `promises` | Holds the exported run to the system properties below; requires `diagnostics.export` and `diagnostics.logs.tail: all`. |

With `promises: true` the runner checks the run after the export and writes
`report.json` and `report.md`:

| Property | Holds |
| --- | --- |
| `paid_output_within_produced` | A host's Finish claims no more output tokens than its Start allowed, nor a count other than the ML node produced for that execution. |
| `charge_only_the_guilty` | An executor the ML log shows answering in full is not charged a miss, an invalid or an invalidation by a usage challenge whose fault votes outweigh the session's vote threshold, a request the client spoiled charges no host, and an answer with forged logprobs is not voted valid. |

`report.md` opens with a one-line verdict, then a table with one row per
property: what it promises, held, violated or not measured, the number of
violations and what was judged. The first five violations of each property follow,
each naming the hosts, the escrow, the inference and the numbers; `report.json`
holds them all. Observations come last: what was not judged and why, how many
client requests were tied to inferences (through the prompt each host stores
with the request tag, or by model, seed and start time where no prompt was
exported), and what the Gateway logged of the usage challenges (outcomes,
reasons, the voters' answers and the time from a challenge's start to its
outcome).

A run whose generator dropped requests, whose dishonest node never answered
dishonestly, or whose export lost what the checks read (a host's diffs or
sessions, its SQLite files, the mock log) is reported as not valid and fails the
check; the verdict line says why. A gap elsewhere in the export, such as a table
over the file bound, is an observation. A finished
run can be checked again with `go run ./cmd/promisecheck -loadtest
loadtest/results/<timestamp>` in `devshard/testenv`, which knows no profiles and
so leaves out the dishonest-node check; it exits 1 on a violation, 2 when the run
cannot be read or the report not written, and 3 when the run is not valid.
`dishonest-executor-overclaim`, `dishonest-executor-wrong-answer` and
`usage-challenge-wave` use it; `dishonest-executor-honest` and
`usage-challenge-wave-honest` are the same runs with every node honest.

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
| `host-faults.json` | Optional host pause/resume timings and command errors. |
| `cpu-profiles.json`, `contention-profiles.json`, `profiles/` | Optional CPU, goroutine, mutex, and block profile manifests and captures. |
| `requests.jsonl` | One record per generated request and response outcome. |
| `assertions.json` | All evaluated assertions and `failed_assertions`. |
| `ml-stats.json` | Per-node allocations, responses, failures, timeouts, and replay counters. |
| `gateway-inferences.json` | Gateway inference records collected for diagnosis. |
| `gateway-state.json` | Gateway diff, nonce, pending transaction, and applied-key counts. |
| `compose-pre-drain.log` | Container logs captured before the drain check. |
| `compose.log` | Final container logs captured after the drain check. |
| `failures/` | Individual failed-request bundles, subject to the runner's artifact cap. |
| `export/` | With `diagnostics.export: true`: every Postgres database as CSV (`db-<service>/postgres/`), SQLite files of hosts that keep sessions in them, and the merged request log of every Mock ML node (`mock-log.json`), with `EXPORT-MANIFEST.json`. |
| `report.json`, `report.md` | With `assertions.promises: true`: the property checks of the run. |

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
