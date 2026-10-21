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

`host_faults` is an alias for `pause` entries of `workload.events` (see
[Event timeline](#event-timeline)); entry N becomes event `host_faults-N`.
Each event produces an `events.<id>.<action>.<target>.applied` assertion
checking that the action and its revert succeeded. Host catch-up validation is
separate from fault injection and is not performed here.

`events.json` records planned windows, real start and end timestamps, and
command failures. Cancellation and error paths run the revert with a separate
cleanup context, including when `-keep-stack` is set.

Every step logs `stage=applied`, `stage=reverted` or `stage=error step=...`
when it happens. Docker steps (pause, throttle, kill, restart, tripfile) get
90 s; mock HTTP steps get 15 s. At the end the runner logs
`stage=events fired=N/M` and a `stage=not_fired` line per event that never
applied, and the run fails.

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

## Event timeline

`workload.events` schedules faults relative to the workload start:

```yaml
cases:
  - {id: receipt-then-empty-stream, title: receipt then empty stream}
  - {id: host-under-poc-challenge, title: host under PoC challenge, known_bug: "#1823"}

workload:
  events:
    - {id: receipt-then-empty-stream, case: receipt-then-empty-stream, action: ml_fault, target: mock-openai-3, at: 2m, until: 3m,
       params: {patch: {empty_stream: true}}}
    - {id: gateway-killed-mid-race, action: gateway_restart, at: 5m40s, params: {signal: KILL}}
```

| Action | Target | Apply / revert |
| --- | --- | --- |
| `pause` | versiond host | `docker compose pause` / `unpause`; `until` required |
| `throttle` | versiond host | read the container's `HostConfig.NanoCpus`, then `docker update --cpus <params.cpus>` / `docker update --cpus` back to the read limit, or to the Docker host's `NCPU` if it had none, since `docker update --cpus 0` leaves the limit unchanged and Docker offers no update that removes it; `until` required |
| `container_restart` | versiond host | `docker compose restart` / `start`; no `until` |
| `container_kill` | versiond host | `docker compose kill -s <params.signal, default KILL>` / `start` at `until` (or at once) |
| `gateway_restart` | `devshardctl` (implicit) | as `container_kill` on the gateway |
| `ml_fault` | mock_ml node | `POST /testenv/fault <params.patch>` / the same keys at zero values (lifting `pause_stream` also releases held streams) |
| `tripfile` | versiond host | `touch` / `rm -f` of `params.file` in the executor fault dir (`/tmp` by default) |
| `chain_participant` | versiond host | mock-chain `POST /testenv/participant {address, mode}` with `params.mode` `unavailable` or `absent` / mode `ok` |
| `epoch_advance` | `mock-chain` (implicit) | without `until`: mock-chain `POST /testenv/epoch {advance: true}`, no revert. With `until`: mock-dapi `POST /testenv/phase` with `params.phases` (`at=phase,...` from the window start; default PoCGenerate, then PoCValidate for the last third) / epoch advance, then phase back to Inference |
| `dapi_challenge` | versiond host | mock-dapi `POST /testenv/challenge {host, on: true}` / `on: false` |
| `marker` | versiond host | nothing; gives a fault caused by topology or traffic (such as a tight peer budget) a window and a case; `until` required |

Events on one host must not overlap; an `ml_fault` on a node bound to
a host (`topology.mock_ml.nodes[].host`) counts as that host. `gateway_restart`
and `epoch_advance` are global and must not overlap each other.

`events.json` holds one record per event: `id, case, action, target, params, planned_start, planned_end, started_at, ended_at, error`. Times are RFC 3339; `started_at`/`ended_at` are null when the step did not happen. `started_at` is when the apply step began, because the fault takes effect somewhere inside it (a `docker compose kill` of the gateway cuts connections up to two seconds before the command returns); `ended_at` is when the revert step returned.

Checks whose `case` has `known_bug` set are reported as `XFAIL` when they fail
and do not fail the run. A passing one is `XPASS` and logged as a warning;
`-strict-xfail` makes it fail the run.

The runner refuses to start while another `loadtest-*` Compose project is
running.

## Production-like 15-minute scenario

`prod-like-15m` runs five participants for 15 minutes and injects one
adversarial case after another, then checks the end-of-run invariants:

```bash
make -C devshard/testenv loadtest-prodlike   # same as SCENARIO=prod-like-15m
```

It runs from a clean worktree: without `config/config.yaml` the runner starts
from the `gencompose` defaults. Before `up` it checks that `build/devshardd`
matches the Docker server arch, because versiond cannot start a child of the
wrong arch. Each mix model gets its own escrow and gateway runtime
(`DEVSHARDS_JSON`).

P0 (`versiond-0/1`, the HA pair) and P1 (`versiond-2`) are never faulted and
are the honest controls. P2 = `versiond-3`, P3 = `versiond-4`, P4 =
`versiond-5`. Each container has its own `prod-like` mock ML node, so an ML
fault lands on one participant. Traffic is 1m@8, 1m@16, 8m@16, 2m at the
peak and 3m@16 rps over `model-a` (70 %, settles) and `model-b` (30 %,
`settlement_enabled: false`), half streamed and half with logprobs. All
quarantines are 45 s, so a host is back before its next case. P4 boots with
`rpc_chats_per_min: 300`: the runner sets `DEVSHARD_RPC_MSGS_PER_MIN` to 300 x
`transport.RPCWeightChat`, so the budget keeps biting if the Chat weight
changes. The peak is one knob, `workload.traffic.peak_rps` (default 32), used
by every stage marked `peak: true`.

| Window | Host | Fault | What happens |
| --- | --- | --- | --- |
| 02:00-03:00 | P2 | `receipt-then-empty-stream` | the host sends its receipt, then the ML node's stream ends with no content |
| 03:15-04:15 | P3 | `ml-server-returns-429` | the ML node answers 429 |
| 04:00-05:00 | P4 | `host-paused-then-catches-up` | the host is frozen, then catches up from its store |
| 05:00-06:30 | P2 | `host-hangs-after-first-token` | the ML node sends one token and holds the stream |
| 05:40 | gateway | `gateway-killed-mid-race` | the gateway gets SIGKILL and is started again, inside `host-hangs-after-first-token` |
| 07:30-08:30 | global | `epoch-change-with-poc` | PoCGenerate, then PoCValidate, then the next epoch |
| 08:45-09:30 | P2 | `host-withholds-payload` | the drop-payload trip file makes the host withhold payloads |
| 09:00-10:00 | P3 | `host-under-poc-challenge` | a PoC challenge: ML-node acquires answer ResourceExhausted |
| 00:00-15:00 | P4 | `host-over-peer-chat-budget` | the host's peer Chat budget is spent (background marker, see below) |
| 10:30-12:15 | P3 | `timeout-vote-fanout` | the ML node hangs during the 32 rps stage, so every host votes the nonces timed out |
| 12:30-13:15 | P2 | `host-public-port-down` | `devshard-fault-public-down`: the public port answers 503 while admin `/ready` answers 200 |
| 13:00-13:30 | P4 | `participant-unavailable-on-chain` | the chain reports the participant unavailable |
| 13:30-14:00 | P4 | `participant-absent-on-chain` | the chain no longer lists the participant |
| 13:40 | gateway | `gateway-restart-while-participant-absent` | the gateway restarts while P4 is absent (`no-escrow-falsely-marked-missing`) |

The two chain steps share the case `participant-unavailable-then-absent-on-chain`.
`escrow-rotation-per-model-settlement`, `logprobs-responses-compress`,
`sqlite-payloads-in-zstd-files`, `session-nonce-within-max-nonce` (the session
nonce never passes `max_nonce`) and `honest-hosts-get-no-http-5xx` cover the
whole run. Cases with known bugs on `devshard-0.2.x-v6` are XFAIL:
`host-under-poc-challenge` (#1823, not in the tip) and
`session-nonce-within-max-nonce` (`nonce-overrun`, not yet filed).
`host-over-peer-chat-budget`, `timeout-vote-fanout` and
`deactivated-escrow-runtime-retired` keep their #1988 marker until a run on
192318583, which merged #1988, shows XPASS; the fix for
`gateway-restart-while-participant-absent` is in the tip, so
`no-escrow-falsely-marked-missing` is a normal check. An XPASS means the fix
landed and the marker should go. `logprobs-responses-compress` relies on the
mock fix of #1987, merged into the tip. prod-like-15m keeps its faults and timing so old
reports stay comparable, except the execution timeout and the `timeout-vote-fanout` hang (below). Its split scenarios (scenarios/README.md) dropped the
`deactivated-escrow-runtime-retired` marker after
`no-runtime-for-deactivated-escrow` passed in every v6 run. prod-like-steady
gives P4 a budget low enough for `host-over-peer-chat-budget` to fire,
because 300 never does: P4 serves about 4 Chats a second at 16 rps.

prod-like-15m uses `execution_timeout: 90s`, as the split scenarios do
(scenarios/README.md, "Execution timeout"). The protocol timeout also cancels a
live model run (#2003), and the 2048-token class runs 45–63 s, so a 30 s timeout
cut honest hosts off at their deadline and charged them misses.
`timeout-vote-fanout` needs the timeout to fire inside its hang, so the hang
lasts 1m45s: a hung nonce times out 90–95 s after it starts. prod-like-15m pins
no grace, so its grace is the default 4m10s (5 s + 90 s + 2m5s + 30 s).

P4's budget is set at boot, so `host-over-peer-chat-budget` is a
`background: true` marker over the whole workload. A background window may
overlap other events on its host; misses and quarantines inside those events'
windows count for them, not for the marker. It is left out of the
per-event and quiet split of `error-rate-and-latency`, out of
`recovery-after-fault` and out of the catch-up exclusion of
`honest-hosts-agree-on-state`. `observe: host_rate_limited` makes
`faults-fired-and-ml-nodes-bound` require an
increase of P4's `devshard_request_terminal_total{terminal="no_receipt_interrupted",reason="rate_limited"}`.
The gateway's own `devshard_peer_rpc_budget_*` waits are not on its
`/metrics` at the tip (only devshardd serves that registry), so the runner
keeps those series from the hosts only.

P3 (`versiond-4`) runs `DEVSHARD_STORAGE_MODE=sqlite`, so its payloads go only
to zstd files under `/opt/versiond/data/<version>/payloads` (`--data-dir` from
versiond, plus `payloads`). The other hosts keep payloads in Postgres. After
drain, `payload-files-are-zstd` tars up to 256 of those files out with `docker compose exec` into
`payload-sample/versiond-4/` and passes when there are at least 20, each starts
with the zstd magic `28 b5 2f fd`, the total size is below the decompressed
size, and each decodes to `{"prompt_payload", "response_payload"}` with a JSON
prompt. An empty directory fails: the file path never ran. Adding
`DEVSHARD_PAYLOAD_ZSTD_ENABLED: "false"` to `versiond-4`'s env is the control,
and `payload-files-are-zstd` must fail.

sqlite hosts keep no validation leases (the sqlite lease store is a no-op,
`storage/leases.go`), so `no-skipped-validation-leases` excludes them by name, logs the exclusion and
lists it in the check's expected value and in the `no-skipped-validation-leases` table.
The check reads the lease table from every Postgres service of the stack: one `devshard-postgres-<host>` per on-chain participant with `topology.storage: per_participant`, the single `devshard-postgres` otherwise. It does not probe a service whose hosts are all excluded, since a sqlite host's Postgres never gets the lease table; a service shared with an included host is still read. A service it cannot read fails the check as `unavailable` and names the service in the details; the rows from the services it could read are still judged.

| Invariant | What it checks |
| --- | --- |
| `misses-only-in-fault-windows` | No protocol miss on honest participants; every miss lies in a fault window; waived windows charge none |
| `escrow-accounting-reconciles` | One settle per deactivated escrow; balance, settled nonce and cost reconcile; `final_nonce_le_max`: final session nonce <= chain `max_nonce` |
| `honest-hosts-agree-on-state` | Honest hosts agree on state roots; settles match; no fork detected |
| `no-nonce-left-pending` | Pending work stays younger than the sweep bound, burned nonces apart; clean end state, burned nonces and warm-up-only escrows apart |
| `no-runtime-for-deactivated-escrow` | No resident runtime for a deactivated escrow |
| `quarantines-match-faults` | Quarantines only on faulty participants, with the expected reason family; each single quarantine within its duration, and none outliving the fault it started in |
| `error-rate-and-latency` | Quiet-window latency and error rate; per-event error-rate bounds over the requests only that event touched; none of the escrow-rotation-gap answers (400 `unsupported model`, 503 `temporarily unavailable`) or 502 `nonce exceeds chain max_nonce limit` anywhere in the run |
| `recovery-after-fault` | Error rate recovers within 60 s (90 s after a gateway restart), judged before the next fault starts |
| `no-restarts-or-resource-growth` | No unplanned restarts; goroutine and live-heap trends, the gateway chat cache and each escrow's host runtime left out |
| `faults-fired-and-ml-nodes-bound` | Every event fired; ML nodes stay bound to their hosts |
| `no-skipped-validation-leases` | No skipped validation lease for honest hosts (Postgres hosts; sqlite hosts excluded) |
| `no-admission-during-poc` | No admission during PoC-blocking phases |
| `responses-compress` | No "did not compress" log line |
| `payload-files-are-zstd` | Payload files on sqlite hosts are zstd |
| `no-http-5xx-on-honest-hosts` | Honest participants get no new `http_503`/`http_error` attempt failures outside global event windows |
| `no-escrow-falsely-marked-missing` | No escrow is marked inactive as "escrow missing on chain" while mock-chain still has it |

`error-rate-and-latency` and `recovery-after-fault` count only requests the generator sent; a request dropped at `max_in_flight` is reported in the `dropped_by_generator` column, never as an error. `error-rate-and-latency` attributes each request to the faults active at any moment between its start and its end, and only when none was active, to the graced tails its start falls in. A gateway restart is active from its kill until the first request that starts after the gateway is back and succeeds, or its 90 s recovery budget if sooner, so a restarted gateway that cannot serve yet is charged to the restart. `recovery-after-fault` still measures recovery, and what cuts it short, from the moment the process is back. A request touched by one fault is judged against that fault's bound; a request touched by several is reported under their overlap (a table row named `host-hangs-after-first-token+gateway-killed-mid-race`, say) and judged by none of them, and the event's check names the overlaps it was judged without. `recovery-after-fault` scans 30 s buckets after an event only until another fault becomes active; when that comes before the event recovered and inside its budget, the check passes as `not judged` and names what cut it short. Events placed back to back therefore leave `recovery-after-fault` little to judge, and a fault judged in its own time needs some traffic no other fault touches.

The `devshard-fault-public-down` trip file is compiled only with the
`devshard_testenv` build tag (`cmd/devshardd/public_fault_testenv.go`); it is
read from `DEVSHARD_TESTENV_EXECUTOR_FAULT_DIR` (`/tmp` by default).

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
Each snapshot request has a 60-second deadline and runs independently of other
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
Full collection still depends on logs retained by Docker's logging driver.

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
  refusal_timeout: 10s
  execution_timeout: 30s

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
| `topology` | Participants, per-host env, storage mode, chain limits, and Mock ML nodes. |
| `workload` | Duration, concurrency, traffic model, event timeline, and request template or mix. |
| `gateway` | Gateway redundancy, quarantines, and optional escrow rotation settings. |
| `cases` | Case IDs for events and checks, with optional `known_bug` markers. |
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

### Request mix and per-host settings

`workload.request.mix` draws each request's parameters from weighted choices.
The draw depends only on `seed` and the request's sequence number, so a rerun
sends the same mix. Dimensions left out use the fixed `request` fields:

```yaml
workload:
  request:
    model: model-a
    timeout: 2m          # client timeout per request; default 30s
    messages: [{role: user, content: "fallback prompt"}]
    mix:
      stream_ratio: 0.5
      logprobs_ratio: 0.5
      top_logprobs: 5
      max_tokens: [{value: 32, weight: 40}, {value: 128, weight: 35}, {value: 512, weight: 20}, {value: 2048, weight: 5}]
      temperature: [{value: 0, weight: 1}, {value: 0.7, weight: 1}]
      prompts:
        - {value: [{role: user, content: "short prompt"}], weight: 1}
      models: [{value: model-a, weight: 70}, {value: model-b, weight: 30}]
```

With more than one mix model the runner creates one escrow and epoch group per
model, as for replay datasets. Streamed responses are classified per request in
`requests.jsonl` (`stream_outcome`) and counted in `summary.json`
(`stream_outcomes`): `completed` (content and `[DONE]`), `empty_stream`
(`[DONE]` without content), `partial` (cut or garbled before `[DONE]`), and
`error_envelope` (an in-stream error event). Only `completed` counts as a
successful request.

```yaml
topology:
  hosts:
    - id: versiond-5
      env: {DEVSHARD_RPC_MSGS_PER_MIN: "3000"}
  mock_ml:
    nodes:
      - {name: mock-openai-5, profile: fast, host: versiond-5}

gateway:
  participant_throttle:
    empty_stream_quarantine: 45s
    stalled_winner_quarantine: 45s
  escrow_rotation:
    models:
      - {model: model-b, settlement_enabled: false}
```

`topology.hosts[].env` adds variables to one versiond container or replaces a
`${VAR:-default}` setting of the same name; a key that collides with a fixed
setting fails compose generation. `mock_ml.nodes[].host` is passed to mock-dapi
as `MOCK_ML_NODE_HOSTS` (`node=host` pairs) with `MOCK_DAPI_HOST_ADDRS`
(`ip=host`). `escrow_rotation.models[]` overrides settlement for one rotated
model; other models keep the global `settlement_enabled`.

There is no separate `thresholds` or fail-fast configuration. A run evaluates
all enabled assertions and prints every result before returning a failure.
`assertions.requests.error_rate` is the maximum permitted error rate over the requests sent to the gateway. `assertions.requests.dropped_rate` bounds the share of planned requests the generator never sent, dropped at `max_in_flight` or skipped as `generator_behind`; when unset it takes the `error_rate` value.

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
| `stages` | `stages[].duration`, `stages[].rps` or `stages[].peak`, optional `peak_rps` | Runs a fixed sequence of rates. Stage durations must add up exactly to `workload.duration`. A `peak: true` stage runs at `peak_rps` (default 32). |

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

For rate-based profiles, each arrival is scheduled from the previous scheduled arrival, so a late wake-up sends the slots it missed at once. A slot more than 1 s behind, or still unsent when the workload ends, is recorded as `dropped_by_generator` with error `generator_behind` instead, which bounds a catch-up burst to one second's worth of requests and keeps the shortfall in `requests.dropped_rate`. `max_in_flight` remains a concurrency cap. If all slots are occupied when the scheduler wants to send a request, that request is recorded as dropped by the generator. A drop means requests in flight run long enough to fill every slot; it is a latency signal, not a gateway error, so it is counted by `requests.dropped_rate` and kept out of `requests.error_rate`.

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
does not validate that the host has caught up. `empty_stream_quarantine` and
`stalled_winner_quarantine` are set the same way.

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

When `assertions.devshard.no_orphaned_work` is enabled, the runner polls `/v1/debug/inferences` until every non-ghost record of an active escrow has a terminal status (`finished`, `validated`, `invalidated`, or `timed_out`). With escrow rotation it reads every runtime the gateway lists: an unfinished record of a deactivated escrow counts as `sealed`, because settlement closes it as it stands and `escrow-accounting-reconciles` judges that settlement, including escrows whose model has settlement off. With invariants enabled, an unfinished record of an active escrow younger than the pending-age bound of `no-nonce-left-pending` counts as `in_flight`: the timeout sweep has not reached it yet. The check fails only on `orphaned` records, those past that bound or, without invariants, any unfinished one at the drain deadline. The DevShard record count does not have to equal the number of client requests: cache hits, speculative attempts, and ghost probes can make those counts different. An empty successful debug snapshot is also considered clean.

A ghost is a nonce the gateway committed and never sent (it burned the nonce). The runner reads every `ghost_probe_skipped` line of `compose-pre-drain.log`, in the text or the JSON log format, with its escrow, nonce, host and reason; the host is the last 8 characters of the participant address. Ghosts are not orphans, and the ghost rate is ghosts over records. A burn with reason `participant_ejected_no_send` or `participant_cut_off_no_send` is left out of both while its host is inside one of its fault windows, grace included (the windows of `misses-only-in-fault-windows`): the fault made the gateway eject or cut off that host. A burn with reason `participant_throttled_no_send` or `participant_catching_up_no_send` is left out while its host is inside a window of a fault that observes `host_rate_limited`, background windows included: a host over its peer Chat budget refuses Chats, so the gateway throttles it and it falls behind (in `prod-like-steady` that is P4 for the whole run). Burns outside those windows, and burns for any other reason, still count. The check's actual value reports the left-out burns as `ghost_excluded=N`.

## Assertions

Assertions are declared in the scenario and are evaluated after the workload:

| Assertion | Purpose |
| --- | --- |
| `requests.http_status` | Requires the configured HTTP status for successful client requests. |
| `requests.terminal_outcome` | Requires the configured client outcome, usually `completed`. |
| `requests.error_rate` | Limits failures among the requests sent to the gateway. |
| `requests.dropped_rate` | Limits requests the generator never sent, at `max_in_flight` or as `generator_behind`; defaults to `error_rate`. |
| `mock_ml.require_each_node_used` | Requires every configured Mock ML node to receive an allocation. |
| `devshard.no_orphaned_work` | Checks that active escrows leave no record unfinished past its deadline, and the configured ghost-rate limit. |
| `escrow_rotation.require_new_escrow` | Requires a new escrow after rotation. |
| `invariants.enabled` | Runs the end-of-run invariants and writes `invariants.json`. |

With `invariants.enabled`, the runner polls gateway throttle snapshots (5 s),
pending work (10 s), host `/stats/*` (30 s), and gateway accounting at every
event boundary. After drain it reads the mock-chain settle txs, the gateway
devshard list and inferences, the lease table of each Postgres service, and a payload-file
sample from each `DEVSHARD_STORAGE_MODE=sqlite` host, then evaluates
each invariant per event window from `events.json`:

```yaml
assertions:
  invariants:
    enabled: true
    baseline: {from: 1m, to: 2m}       # reference for quiet-window latency
    trend_early: {from: 3m, to: 5m}    # goroutine comparison of no-restarts-or-resource-growth
    trend_late: {from: 13m30s, to: 15m}
    grace: 3m10s                       # default refusal + execution + 2m5s sweep grace + 30 s sweep tick
    max_pending_age: 3m40s             # default is the grace formula plus 30 s
    # ties a run-level invariant, or one check such as
    # escrow-accounting-reconciles.final_nonce_le_max, to a known_bug case
    cases: {no-runtime-for-deactivated-escrow: deactivated-escrow-runtime-retired}
```

Two bounds in detail:

- `no-nonce-left-pending.max_pending_age` leaves out the nonces the gateway burned. A burned nonce is never sent, so no timeout is voted on it and it stays pending until its escrow closes. The check reports burned nonces apart, as `burned_max` and `burned_samples_over` in its actual value and `burned_max_age_seconds` and `burned_samples_over_limit` in its table. A `pending.jsonl` written before samples kept `open_started` keeps its old ages, with a note in the table.
- `no-nonce-left-pending.clean_end_state` leaves out the same burned nonces, and an escrow whose session nonce is still 1 at the final poll: the gateway created it during the drain and sent only its warm-up probe, whose finish waits for the next request's diff. Both are listed apart in the details (`burned_open=N`, `warm-up only`) and counted in `apart=N`.
- `error-rate-and-latency.rotation_gap_errors` counts requests answered 400 `unsupported model` (503 `model ... is temporarily unavailable` when the model is in `model_limits`) and `error-rate-and-latency.max_nonce_errors` those answered 502 `nonce exceeds chain max_nonce limit`; each allows none, anywhere in the run, and is a FIX, because both are the gateway's own behaviour that the error-rate bounds would otherwise absorb. The first happens in the gap between an escrow's deactivation and its replacement: routing skips an escrow at its nonce limit, the gateway deactivates it before it creates the replacement on chain, and with one escrow per model (`target_count: 1`) nothing serves the model until the replacement is resident, which took 22 ms to 1.06 s in the saved runs while the chain transaction ran. A client retrying after that gap succeeds, but a 400 tells it not to. The second comes from escrows that still hold more routed requests than the 200 nonces the gateway keeps short of the hosts' active cap. A scenario can tie either check to a `known_bug` case through `invariants.cases`.
- `quarantines-match-faults.durations` judges each single quarantine: its `quarantine_until` less the poll that first showed it. A quarantine the gateway renews as it ends starts a new one, so a fault that keeps failing a host is not read as one long quarantine. An episode still fails when it outlives the latest fault it can have started in by more than the duration and one tick; an earlier fault whose grace it also falls in is not blamed.
- `no-restarts-or-resource-growth.heap_slope` fits the slope to the live heap: the lowest `go_memstats_heap_alloc_bytes` of each 30 s, since heap in use also holds garbage up to the GC goal. It subtracts `devshard_gateway_cache_bytes` in each sample. The gateway's chat cache keeps every response for an hour up to 256 MiB, so in a 10 to 18 minute run it fills all run; when any steady sample lacks the cache series, as on devshardctl, which does not export it, the gateway's row is not judged and the check is `NOT_JUDGED` unless a host fails. `heap_minus_cache` in the row says the cache was subtracted. On a host it also subtracts 3.36 MiB, the 20 000-slot validation queue, for every escrow created since the first steady sample. The gateway row stays unjudged without that series: the cache size can be rebuilt from the gateway's `gateway_cache_stored` lines (it matches `chatResponseCache.Set` in the heap profiles), but the gateway's heap less that rebuilt cache moves between -34 and +5 MiB/min with the load and with when its GCs fall, against a 1 MiB/min bound, and the cache fills only 7 to 12 minutes in, leaving too short a window after it. The gateway row needs devshardctl to export `devshard_gateway_cache_bytes` (the byte total of `chatResponseCache.Stats()`), and its live heap after GC (runtime/metrics `/gc/heap/live:bytes`) to judge growth this small under changing load.
- `no-restarts-or-resource-growth.goroutines` allows a host 20 more goroutines for every escrow created between the early and the late window. A host keeps a runtime with 20 validation workers per escrow until the epoch is evicted, and the scenarios rotate an escrow every few minutes, so this is growth by design; the row reports `escrows_created_between_windows`.
- Both trends start a host's steady samples 2 minutes after its first finished request (`devshard_request_terminal_total` above 0), and move its early window as far, when that is later than the run's own warm-up: the second replica of P0's HA pair can get no traffic for the first minutes. The row reports `steady_from` then.

Event params can state what a fault should cause: `expect_quarantine` (`none`
or a comma list of `http`, `transport`, `empty_stream`, `stalled_winner`,
`sender_rate`),
`waive_misses: true`, and `max_error_rate`. A `dapi_challenge` event waives
misses and forbids an `empty_stream` quarantine by default. An `ml_fault` that
hangs or holds the stream (`hang`, `pause_stream`) allows `stalled_winner`,
`transport` and `empty_stream`: the host has sent its receipt but no content
when the gateway gives up on the attempt, and the gateway reports that as an
empty stream. That is the only reason it can give: it counts any attempt with a
receipt and no content chunk as an empty stream, cancelled or not; a stalled
winner must have won and sent content; and its own cancel of a losing attempt
is never a transport failure. Under `pause_stream` only the requests that hold
one of the mock's workers get the first token; the rest wait in its queue. `faults-fired-and-ml-nodes-bound` checks that
each event fired; it never becomes XFAIL, because a fault that did not fire
makes the run invalid.

The final report prints each check with its expected value, actual value, and
diagnostic details. Failed checks are also listed in `assertions.json`.

### Run report

Every run ends with `report.md` in its directory, also printed to the console, built from `summary.json`, `assertions.json`, `events.json` and `invariants.json` only. Its template does not change between runs: a verdict line counting every status, the run's stalled share (`harness_health.invalid_fraction`) right under it, then (1) client traffic with the error and drop bounds and the quiet window, (2) one row per fault with its own requests, errors, bound and recovery, (3) the overlaps several faults touched at once, (4) the end state (orphans, settlement, pending age, resident runtimes), and (5) every check sorted by status. Each check carries one status:

| Status | Meaning |
| --- | --- |
| `FIX` | A bound was broken; the gateway or the host needs a fix. |
| `XPASS` | A `known_bug` case passed; drop its marker if the fix landed. |
| `INVALID` | A harness stall covers every window the check judged. |
| `XFAIL` | A `known_bug` case failed as expected. |
| `NOT_JUDGED` | The run left no data for the check, such as a fault every request of which another fault also touched. |
| `PASS` | The bound held. |

`go run ./cmd/loadtest -report <run directory>` rebuilds the report of an earlier run without starting a stack.

`go run ./cmd/loadtest -recheck <run directory>` judges every check of an earlier run again without a stack, from the files in its directory, and rewrites `assertions.json`, `invariants.json` and `report.md` there; copy the directory first to keep the run's own result. It exits 1 when a check fails. A live run judges its checks the same way: it writes what it read from the stack after the workload to `run-inputs.json`, then evaluates from the directory with the same code, so `-recheck` on a run made by the same binary gives the same `assertions.json`. A run saved before `run-inputs.json` existed is judged from what its other files hold. Its host-to-participant map comes from `host-stats.jsonl`, its allocations from `ml-stats.json` and its burns from `compose-pre-drain.log` (decompress `compose-pre-drain.log.gz` first). The drain check, the escrow-rotation check and the checks that need the gateway devshard list, the chain ledger or the lease table report `unavailable`, and the runner logs a `recheck:` line for each of these gaps.

#### Old codes

Reports written before checks, cases and events got descriptive names use short codes. A check named `invariants.I7.c1-empty-stream.error_rate` there is `invariants.error-rate-and-latency.receipt-then-empty-stream.error_rate` now; the bounds and the logic behind each name are unchanged. `-report` looks checks up by the new names, so rebuild an older run's report with the binary that wrote it.

| Old code | Kind | Name |
| --- | --- | --- |
| `I1` | invariant | `misses-only-in-fault-windows` |
| `I2` | invariant | `escrow-accounting-reconciles` |
| `I3` | invariant | `honest-hosts-agree-on-state` |
| `I4` | invariant | `no-nonce-left-pending` |
| `I5` | invariant | `no-runtime-for-deactivated-escrow` |
| `I6` | invariant | `quarantines-match-faults` |
| `I7` | invariant | `error-rate-and-latency` |
| `I8` | invariant | `recovery-after-fault` |
| `I9` | invariant | `no-restarts-or-resource-growth` |
| `I10` | invariant | `faults-fired-and-ml-nodes-bound` |
| `I11` | invariant | `no-skipped-validation-leases` |
| `I12` | invariant | `no-admission-during-poc` |
| `I13` | invariant | `responses-compress` |
| `I14` | invariant | `payload-files-are-zstd` |
| `I15` | invariant | `no-http-5xx-on-honest-hosts` |
| `I16` | invariant | `no-escrow-falsely-marked-missing` |
| `C1` | case | `receipt-then-empty-stream` |
| `C2a` | case | `ml-server-returns-429` |
| `C2b` | case | `host-over-peer-chat-budget` |
| `C3` | case | `host-hangs-after-first-token` |
| `C4` | case | `host-under-poc-challenge` |
| `C5` | case | `host-paused-then-catches-up` |
| `C6` | case | `gateway-killed-mid-race` |
| `C7` | case | `escrow-rotation-per-model-settlement` |
| `C7-I5` | case | `deactivated-escrow-runtime-retired` |
| `C8` | case | `epoch-change-with-poc` |
| `C9` | case | `host-public-port-down` |
| `C10` | case | `participant-unavailable-then-absent-on-chain` |
| `C11` | case | `timeout-vote-fanout` |
| `C12` | case | `logprobs-responses-compress` |
| `C13` | case | `host-withholds-payload` |
| `C14` | case | `sqlite-payloads-in-zstd-files` |
| `N1` | case | `session-nonce-within-max-nonce` |
| `N7` | case | `gateway-restart-while-participant-absent` |
| `B22` | case | `honest-hosts-get-no-http-5xx` |
| `c1-empty-stream` | event | `receipt-then-empty-stream` |
| `c2a-ml-429` | event | `ml-server-returns-429` |
| `c2b-budget` | event | `host-over-peer-chat-budget` |
| `c3-hang` | event | `host-hangs-after-first-token` |
| `c4-challenge` | event | `host-under-poc-challenge` |
| `c5-pause` | event | `host-paused-then-catches-up` |
| `c6-gateway-kill` | event | `gateway-killed-mid-race` |
| `c8-epoch` | event | `epoch-change-with-poc` |
| `c9-public-down` | event | `host-public-port-down` |
| `c10-unavailable` | event | `participant-unavailable-on-chain` |
| `c10-absent` | event | `participant-absent-on-chain` |
| `c11-vote-fanout` | event | `timeout-vote-fanout` |
| `c13-drop-payload` | event | `host-withholds-payload` |
| `n7-restart-absent` | event | `gateway-restart-while-participant-absent` |

## Artifacts

Each run writes a timestamped directory containing the scenario copy, request
records, summaries, state snapshots, and logs:

| Artifact | Contents |
| --- | --- |
| `run.yaml` | Exact scenario used for the run. |
| `summary.json` | Client workload counts, latency, duration, and per-minute windows. |
| `metrics.jsonl` | Process memory/CPU/GC and per-escrow state time series. |
| `metrics-summary.json` | Sampled peaks, counter deltas, initial/final state, and collection errors. |
| `events.json` | Planned and real windows of every scheduled event, and command errors. |
| `invariants.json` | Invariant results, honest set, fault windows, and per-invariant tables. |
| `payload-sample/<host>/` | `payload-files-are-zstd` copy of up to 256 payload files from each sqlite host. |
| `accounting-snapshots.jsonl`, `throttle.jsonl`, `pending.jsonl`, `host-stats.jsonl`, `dapi-host-stats.jsonl` | Invariant inputs sampled during the run. Each `pending.jsonl` sample keeps every open nonce's start time (`open_started`). |
| `run-inputs.json` | What the checks read from the stack after the workload: the drain check's last read, burned nonces, allocations, escrow IDs, accounting, the gateway devshard list and inferences, the chain ledger, leases, payload sample stats, log-derived counts, and how much of `metrics.jsonl` was judged. `-recheck` reads it. |
| `cpu-profiles.json`, `contention-profiles.json`, `profiles/` | Optional CPU, goroutine, mutex, and block profile manifests and captures. |
| `requests.jsonl` | One record per generated request and response outcome. |
| `assertions.json` | All evaluated assertions and `failed_assertions`. |
| `report.md` | The run report in its fixed template; see [Run report](#run-report). |
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
