# Gateway documentation

This directory holds the documents that describe behaviour no single package owns. Each package's own `README.md` describes that package; the list of packages is in [`../README.md`](../README.md), "Packages".

## Reference

| Document | What it holds |
| --- | --- |
| [operations.md](./operations.md) | the routes and who may call them, the kill switch, the boot and shutdown order, what is kept on disk |
| [configuration.md](./configuration.md) | the configuration layers, every environment variable and admin override, the per-model filter settings |
| [observability.md](./observability.md) | log lines, metrics, traces and the admin routes that read the gateway's state |

## How-to

| Document | What it holds |
| --- | --- |
| [runbook.md](./runbook.md) | procedures: release and rollback, backup and restore, settling and deactivating escrows, troubleshooting by symptom |
| [`../deploy/README.md`](../deploy/README.md) | bringing up the compose stack |
| [`../CONTRIBUTING.md`](../CONTRIBUTING.md) | building, testing and the rules every code change is reviewed against |

## Explanation

| Document | What it holds |
| --- | --- |
| [architecture.md](./architecture.md) | why the gateway has this shape, the import graph, a nonce's life |
| [request.md](./request.md) | one request from the socket to the settled nonce, step by step |
| [race.md](./race.md) | the speculative race: escalation, the crown, deadlines, the drain and the timeout votes |
| [routing.md](./routing.md) | picking an escrow and a host, the per-escrow dispatcher, every burn reason |
| [escrows.md](./escrows.md) | an escrow's life: creation, rotation, depletion, settlement, retirement |
| [capacity.md](./capacity.md) | admission, the per-host congestion windows, outlier ejection, buffered replies |
| [accounting.md](./accounting.md) | the nonce ledger: its vocabulary, its surface, every finding and its storage |
| [rules.md](./rules.md) | the invariants that must stay true, the non-goals and what the tests do not verify |
| [`devshard/gatewayprotocol`](../../../gatewayprotocol/README.md) | the protocol rules a host, a verifier or the chain would judge: the settle message and its root, the claim-or-vote choice, the probe budget, the evidence of an error stream; [`gateway-protocol-library.md`](../../../docs/gateway-protocol-library.md) explains the boundary |
