# Observability

Placeholder. The canonical spec for the observability stack (Alloy +
VictoriaMetrics + cAdvisor + Node Exporter + Loki + Grafana) lives in
[`../docs/testenv.md`](../docs/testenv.md) §Phase 13.

This file will be populated once Phase 13 is implemented. Target content:

- Stack overview with service endpoints and IPs.
- `make obs-up` / `make obs-down` lifecycle.
- Grafana URLs and default dashboards.
- macOS caveats (Node Exporter `/proc` mount, cAdvisor overlay).
- Notes on metric naming and how `devshardd` exposes `/metrics`.
