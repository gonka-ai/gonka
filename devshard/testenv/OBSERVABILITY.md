# Observability

This stack is specified in [devshard/docs/testenv.md](../docs/testenv.md) §Phase 13. The
`docker-compose.yml` fragment lives under
[`observability/`](observability/compose-fragment.yaml) and is spliced in by
`go run ./testenv/cmd/gencompose` (see the `-obs-fragment` flag).

## Services (testenv `172.30.0.100`–`105`)

Grafana **Alloy** and the cAdvisor / node-exporter / VictoriaMetrics wiring
match the tuned **`subnet-testenv`** stack (`subnet/testenv/observability/`
on that branch) so the River in `alloy/config.alloy` and container labels
stay in sync. Devshard **adds** `prometheus.scrape` for
`devshardd-testenv-*:9600` and uses Loki **2.9** + the local `loki/config.yaml`
until a Loki 3 port lands.

| Service            | Image tag (pinned in compose)               | Port (localhost) | Role                 |
|--------------------|---------------------------------------------|------------------|----------------------|
| victoria-metrics   | `victoriametrics/victoria-metrics:v1.138.0`  | 8428             | Metrics store + VMUI  |
| alloy              | `grafana/alloy:v1.15.0`                        | 12345            | UI + pipeline         |
| cadvisor           | `gcr.io/cadvisor/cadvisor:v0.55.1`            | 18080→8080        | Container metrics     |
| node-exporter      | `prom/node-exporter:v1.10.2`                    | 9100              | Host/VM “node” stats  |
| loki               | `grafana/loki:2.9.3`                            | 3100              | Logs                  |
| grafana            | `grafana/grafana:11.0.0`                        | 3000              | Dashboards            |
| (reserved)         | Tempo (future)                                 | —                 | 172.30.0.106          |

On macOS with Docker **Desktop**, node-exporter and `/proc` mounts still
describe the **Linux VM** the engine runs, not the macOS host — same as
subnet-testenv. cAdvisor and container series remain the main per-service view.
If `/dev/kmsg` or `/run/containerd/containerd.sock` fails to mount on a host,
trim those lines locally (rare on Docker Engine for Linux; Desktop varies by version).

**VictoriaMetrics** has no authentication in this test stack. Binds in the
compose fragment are `127.0.0.1` on the host for VMUI and write paths, and
the `testenv` bridge keeps services isolated from the LAN.

**devshardd-testenv** exposes `EXPORT_METRICS=1` and `METRICS_PORT=9600` by
default in generated compose, serving Prometheus text at `http://<service>:9600/metrics`
(Alloy scrapes the four `devshardd-testenv-*` services).

## Make targets (see [Makefile](Makefile))

- `make obs-up` — alias for `make up` (full testenv, including observability).
- `make obs-up-linux` — same as `up` (kept for old scripts; node-exporter is in the default stack now).
- `make obs-down` — stop observability service containers.
- `make obs-logs` — follow `victoria-metrics`, `alloy`, `grafana`, and `loki`.
- `make obs-grafana-open` / `make obs-query-open` — on macOS, `open(1)` the
  Grafana and VM UIs; elsewhere prints URLs.
- `make obs-reset` — recreates obs containers; see `make help` for the exact
  behavior. To delete persistent metrics/log data, remove the host directory
  `./obs-data/` under `testenv/` (default `TESTENV_OBS_REL_SUBDIR` in
  `observability/compose-fragment.yaml`) then `up -d` again. Citest uses
  `./.citest-obs-data/` instead (see `scripts/run-stack-citest.sh`).

## Grafana

Default entry (anonymous, Editor): `http://localhost:3000`.

Provisioned providers and dashboards: `observability/grafana/`. The overview
board is a scaffold; cAdvisor and node-exporter JSON definitions are **bundled
** in-repo — refresh instructions and upstream pins are in
[observability/README.md](observability/README.md).

## cAdvisor and Docker Desktop

cAdvisor uses cgroup data through the engine; on Docker Desktop (Mac and
Windows) it is still Linux-side container stats. Mounts in the fragment match
the usual cAdvisor + Compose examples; if your engine reports missing paths
on a host OS, adjust the `cadvisor` `volumes` block locally (do not commit
machine-specific mounts unless the project standardises them).
