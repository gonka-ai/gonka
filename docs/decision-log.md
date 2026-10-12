# Decision Log

| Date | Decision | Details |
| --- | --- | --- |
| 2026-07-10 | ML-node metrics schema v1: allowlist contract, `GONKA_METRICS=full\|off` (default `full`, `off` => 404), public pull via dapi, no central collection | [ADR](adr/mlnode-monitoring-metrics-v1.md), [operator guide](observability.md) |
| 2026-07-10 | XID surfaced via NVML event API (works in unprivileged containers); dmesg rejected (host-only) | [ADR](adr/mlnode-monitoring-metrics-v1.md) Open-2 |
