# DevShard load testing

This directory contains declarative, reproducible load scenarios for the local
Docker DevShard test environment. Each run starts an isolated stack, generates
traffic through the gateway, checks the configured protocol invariants, and
writes diagnostic artifacts.

## Quick start

From the repository root:

```bash
make -C devshard/testenv loadtest SCENARIO=normal-load
```

Run artifacts are written under `loadtest/results/<timestamp>/`.

Run the escrow rotation scenario without a replay dataset:

```bash
go run ./cmd/loadtest \
  -scenario loadtest/scenarios/escrow-rotation.yaml
```

## Captured request replay

A JSONL capture can provide both the client workload and the response body used
by Mock ML nodes:

```bash
go run ./cmd/loadtest \
  -scenario loadtest/scenarios/normal-load.yaml \
  -load-dataset loadtest/resources/devshard-load-samples-250-sanitized-final.jsonl
```

Each request selects a sample pseudo-randomly with replacement using the
scenario seed. Mock ML nodes look up the matching response by `messages`, so
concurrent or speculative requests cannot consume each other's responses.
The captured model is preserved in each sample. When replay is enabled, the
runner registers every unique model found in the JSONL before starting the
Gateway, so no separate model override is needed. Replay selections and
fingerprints are recorded in `requests.jsonl`, while `replay_hits` and
`replay_misses` are included in `ml-stats.json` and the terminal summary.

## Layout

| Path | Purpose |
| --- | --- |
| `scenarios/` | Runnable YAML scenarios. |
| `profiles/` | Mock ML-node behavior profiles. |
| `traffic.go` | Traffic-profile validation and request scheduling. |
| `results/` | Local artifacts from completed and failed runs; not committed. |

## Scenario configuration

Scenarios may override the Gateway speculative-cleanup grace period:

```yaml
gateway:
  redundancy:
    secondary_wait_after_winner: 30s

drain_timeout: 60s
```

`secondary_wait_after_winner` defaults to `10m`. It controls how long the
Gateway lets speculative losers finish after a winner is selected. The
`drain_timeout` is the load-test harness deadline and should be longer than
that grace period when the scenario verifies cleanup.

When `assertions.devshard.no_orphaned_work` is enabled, the runner polls
`/v1/debug/inferences` until every non-ghost record has a terminal status
(`finished`, `validated`, `invalidated`, or `timed_out`). It does not require
the DevShard record count to equal the number of client requests: cache hits
and speculative attempts make those counts different. An empty successful
debug snapshot is also considered clean.

For rotation scenarios, `assertions.escrow_rotation.require_new_escrow` compares
the escrow IDs from the generated initial topology with the Gateway's
`/v1/admin/devshards` state and requires at least one additional escrow ID.

## Traffic profiles

Traffic configuration is kept inline in each scenario under `workload.traffic`.
TODO: Document `closed_loop`, `constant`, `ramp`, `sine`, `spikes`, and
`stages`.

## Mock ML profiles

Profiles define deterministic time to first token (`ttft`) and the delay
between later streaming chunks (`token_interval`). `fast` waits only before
the first chunk, `realistic` uses fixed moderate delays, and `slow` delays both
the first and later chunks. `failing` and `timeout` add failure behavior.

The available profiles are `fast`, `realistic`, `slow`, `failing`, and
`timeout`.

## Artifacts

TODO: Document `summary.json`, `requests.jsonl`, `assertions.json`,
`gateway-inferences.json`, `gateway-state.json`, and `ml-stats.json`.
`compose-pre-drain.log` contains the snapshot used to identify ghost probes;
`compose.log` is collected after drain and contains the final container logs
for diagnosis.
