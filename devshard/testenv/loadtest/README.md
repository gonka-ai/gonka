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

## Real DAPI Bootstrap

The migration target is a single integration environment backed by a real local
`inferenced` chain and `decentralized-api`. Every invocation uses a scenario;
the `environment` block selects its backing chain and DAPI. The first migration
step can be verified with:

```bash
cd devshard/testenv && go run ./cmd/loadtest -scenario loadtest/scenarios/real-dapi-bootstrap.yaml
```

It starts an isolated chain+DAPI pair, verifies DAPI's real `/versions` endpoint
and NodeManager gRPC API, and saves the rendered Compose file, NodeManager
capacity response, and logs as artifacts. It does not yet provision an escrow,
DevShard hosts, or ML nodes; those are the next migration steps.

`normal-load-real-dapi.yaml` already records the intended healthy load
configuration. It uses the local testnet's governance model
`Qwen/Qwen2.5-7B-Instruct`. It will become runnable when those provisioning steps are wired;
the runner currently fails it explicitly instead of silently reducing it to a
bootstrap-only run.

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
