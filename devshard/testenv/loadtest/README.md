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

TODO: Document the scenario schema: topology, workload, assertions, and drain
behavior.

## Traffic profiles

Traffic configuration is kept inline in each scenario under `workload.traffic`.
TODO: Document `closed_loop`, `constant`, `ramp`, `sine`, `spikes`, and
`stages`.

## Mock ML profiles

TODO: Document latency, capacity, queue, and failure behavior for Mock ML
nodes. The initial profiles are `fast` and `slow`.

## Artifacts

TODO: Document `summary.json`, `requests.jsonl`, `assertions.json`,
`gateway-inferences.json`, and `compose.log`.
