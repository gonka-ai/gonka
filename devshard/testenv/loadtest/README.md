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

## Traffic profiles

Traffic configuration is kept inline in each scenario under `workload.traffic`.
TODO: Document `closed_loop`, `constant`, `ramp`, `sine`, `spikes`, and
`stages`.

## Mock ML profiles

TODO: Document latency, capacity, queue, and failure behavior for Mock ML
nodes. The available profiles are `fast`, `slow`, `failing`, and `timeout`.

## Artifacts

TODO: Document `summary.json`, `requests.jsonl`, `assertions.json`,
`gateway-inferences.json`, `gateway-state.json`, and `ml-stats.json`.
`compose-pre-drain.log` contains the snapshot used to identify ghost probes;
`compose.log` is collected after drain and contains the final container logs
for diagnosis.
