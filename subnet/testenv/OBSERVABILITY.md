# Observability Stack — Subnet Testenv

## Overview

The observability stack runs alongside the subnet participants inside the same
Docker Compose network (`testenv`). It is designed in phases:

| Phase | Status | What it covers |
|---|---|---|
| **1 — Metrics** | **Current** | Container + host resource metrics (CPU, memory, network, disk) |
| **2 — Logs** | Planned | Docker container logs → Loki |
| **3 — Traces** | Planned | OTel traces from subnet hosts → Tempo |
| **4 — Dashboards** | Planned | Grafana dashboards wiring everything together |

---

## Architecture (Phase 1)

```
┌──────────────────────────────────────────────────────────────┐
│                    testenv Docker network                    │
│                                                              │
│  ┌─────────────┐   GET /metrics    ┌──────────────────────┐  │
│  │  cAdvisor   │◄──────────────────│                      │  │
│  │  :8080      │  every 15s        │    Grafana Alloy     │  │
│  └─────────────┘                   │    :12345            │  │
│                                    │                      │  │
│  ┌─────────────┐   GET /metrics    │  prometheus.scrape   │  │
│  │Node Exporter│◄──────────────────│  prometheus.remote   │  │
│  │  :9100      │  every 15s        │  _write              │  │
│  └─────────────┘                   └──────────┬───────────┘  │
│                                               │              │
│                                    POST /api/v1/write        │
│                                               │              │
│                                    ┌──────────▼───────────┐  │
│                                    │  VictoriaMetrics     │  │
│                                    │  :8428               │  │
│                                    │  (storage + query)   │  │
│                                    └──────────────────────┘  │
└──────────────────────────────────────────────────────────────┘

Host machine access:
  http://localhost:8428/vmui   ← MetricsQL query UI
  http://localhost:12345       ← Alloy pipeline graph + debug UI
  http://localhost:18080       ← cAdvisor raw metrics
  http://localhost:9100        ← Node Exporter raw metrics
```

---

## Services

### VictoriaMetrics (`172.30.0.100:8428`)

Time-series storage and query engine. A drop-in replacement for Prometheus with
a more efficient storage engine and MetricsQL (a superset of PromQL).

- Receives metrics via Prometheus `remote_write` from Alloy
- Retention: 7 days (configurable via `--retentionPeriod`)
- **Query UI (vmui):** http://localhost:8428/vmui
- **API:** http://localhost:8428/api/v1/query (PromQL-compatible)

**Does VictoriaMetrics need authorization?**

No — not in this testenv setup, and it requires deliberate configuration to
enable. By default VictoriaMetrics runs with **open HTTP endpoints**. This is
fine here because:

1. The `remote_write` endpoint (`:8428/api/v1/write`) is only reachable from
   within the `testenv` Docker network — Alloy is the only writer.
2. The query port is exposed on `localhost:8428` only (not `0.0.0.0`), so it is
   not accessible from the network unless you explicitly port-forward.
3. The testenv runs on a developer machine, not in a shared environment.

If you ever expose VictoriaMetrics on a shared host, add `vmauth` (the
VictoriaMetrics auth proxy) in front of it, or put Nginx with basic auth as a
reverse proxy. Do not enable auth inside VictoriaMetrics itself — it does not
support it natively.

---

### cAdvisor (`172.30.0.102:8080`)

Google's Container Advisor. It hooks into the **Docker daemon socket** and reads
**cgroup** accounting data for every running container. No changes to subnet
hosts are needed.

**What it measures (per container):**

| Metric family | Examples |
|---|---|
| CPU | `container_cpu_usage_seconds_total`, `container_cpu_throttled_seconds_total` |
| Memory | `container_memory_usage_bytes`, `container_memory_working_set_bytes` |
| Network | `container_network_receive_bytes_total`, `container_network_transmit_bytes_total` |
| Disk I/O | `container_fs_reads_bytes_total`, `container_fs_writes_bytes_total` |

Every metric carries a `name` label with the container name (e.g.
`testenv-participant-0`), so you can instantly filter to a single participant.

**How Alloy scrapes it:**

Alloy sends a plain HTTP GET to `http://cadvisor:8080/metrics` every 15 seconds.
cAdvisor responds with a Prometheus text-format exposition of all container
metrics collected since the last scrape. Alloy then forwards the batch to
VictoriaMetrics via `remote_write`.

```
Alloy                      cAdvisor
  │──GET /metrics──────────►│  (every 15s)
  │◄── text/plain metrics ──│
  │                          │
  │──POST /api/v1/write ────►VictoriaMetrics
```

cAdvisor works correctly on **all platforms** including macOS/Docker Desktop
because it reads cgroup data from the Docker daemon API, which is always a Linux
kernel (the Docker Desktop VM on macOS).

---

### Node Exporter (`172.30.0.103:9100`)

Prometheus-native exporter for **host-level** hardware and OS metrics: CPU load,
memory, filesystem, network interfaces, disk I/O at the kernel level.

**What it measures:**

| Metric family | Examples |
|---|---|
| CPU | `node_cpu_seconds_total` (per core, per mode) |
| Memory | `node_memory_MemAvailable_bytes`, `node_memory_Buffers_bytes` |
| Filesystem | `node_filesystem_avail_bytes`, `node_filesystem_size_bytes` |
| Network | `node_network_receive_bytes_total` (per interface) |
| Load | `node_load1`, `node_load5`, `node_load15` |
| Disk | `node_disk_read_bytes_total`, `node_disk_io_time_seconds_total` |

**How Alloy scrapes it:**

Same pull model as cAdvisor. Alloy GETs `http://node-exporter:9100/metrics`
every 15 seconds and remote_writes the result to VictoriaMetrics.

#### macOS / Docker Desktop note

On macOS, Docker containers do not run on the macOS kernel directly. Docker
Desktop runs a **hidden Linux VM** using Apple's Hypervisor framework (HVF).
The Docker daemon — and everything inside it, including Node Exporter — lives
inside that VM.

```
macOS Darwin kernel          ← what you use
└─ Docker Desktop (HVF VM)  ← Linux kernel, hidden
    └─ node-exporter
        ├─ /host/proc  → VM's /proc   ← NOT macOS /proc
        ├─ /host/sys   → VM's /sys    ← NOT macOS /sys
        └─ pid: host   → VM's PID 1   ← NOT macOS launchd
```

As a result Node Exporter on macOS reports metrics for the Docker Desktop Linux
VM (typically 4–8 vCPUs, 2–8 GB RAM depending on your Docker Desktop settings),
**not** your physical Mac hardware.

**What this means in practice:**

- `node_memory_MemAvailable_bytes` reflects the VM's memory, not your 32GB Mac RAM.
- `node_cpu_seconds_total` reflects VM vCPUs, not Apple Silicon cores.
- `node_filesystem_avail_bytes` reflects the VM's virtual disk, not your SSD.

**What still works correctly:**

The VM-level metrics are still useful for the testenv's primary goal. When 10
participants are gossipping heavily, you will see:

- VM CPU pressure when all participants are computing diffs simultaneously
- VM memory pressure if participants leak memory
- Docker socket I/O from cAdvisor showing per-container resource consumption

If you need real macOS host metrics, there is no official Prometheus exporter
for macOS. Your options are:

1. **Accept VM metrics** (recommended) — sufficient for subnet load testing.
2. **Run the testenv on Linux** — native metrics, no caveats.
3. **Disable Node Exporter on macOS** — use a `docker-compose.override.yml`
   (see below) and rely on cAdvisor container metrics alone.

**Docker Compose override to skip Node Exporter on macOS:**

Create a local `docker-compose.override.yml` (already gitignored) with:

```yaml
services:
  node-exporter:
    profiles: ["linux-only"]
```

Then start without it:
```bash
docker compose up -d  # node-exporter won't start (wrong profile)
```

---

### Grafana Alloy (`172.30.0.101:12345`)

Alloy is an OpenTelemetry Collector distribution from Grafana with a
programmable pipeline language (River syntax, `.alloy` files). It is the single
collector that will eventually handle metrics, logs, and traces.

Configuration: [`observability/alloy/config.alloy`](./observability/alloy/config.alloy)

**Current pipeline (Phase 1):**

```
prometheus.scrape "cadvisor"      ─┐
prometheus.scrape "node_exporter"  ├──► prometheus.remote_write "victoriametrics"
prometheus.scrape "alloy_self"    ─┘         └─► VictoriaMetrics :8428
```

**How the scrape works internally:**

Each `prometheus.scrape` block is an active pull loop. Alloy:
1. Resolves the `targets` list (static IPs in Phase 1, service-discovery in Phase 2)
2. GETs `http://<target>/metrics` at `scrape_interval`
3. Parses the Prometheus text exposition format
4. Applies relabeling rules (if any)
5. Buffers samples into the `forward_to` receiver's queue
6. The `remote_write` component drains the queue in batches
   (`max_samples_per_send = 1000`) and POSTs to VictoriaMetrics

The **Alloy UI** at http://localhost:12345 shows a live pipeline graph with
component health, last scrape timestamps, and sample counts — useful for
debugging whether metrics are actually flowing.

---

## Phase 2 — Docker log scraping (planned)

When Loki is added, Alloy will collect logs from all Docker containers using the
`loki.source.docker` component. This reads from the **Docker daemon's log API**
(same socket that `docker logs` uses) — no changes to the subnet hosts are
needed.

```alloy
// How Docker log scraping will work (Phase 2, not yet active):
loki.source.docker "containers" {
  host       = "unix:///var/run/docker.sock"
  targets    = discovery.docker.all.targets

  // Each log line is labeled with container metadata automatically:
  // container_name, image, compose_service, compose_project
  forward_to = [loki.write.loki.receiver]

  relabel_rules = loki.relabel.docker_labels.rules
}

loki.relabel "docker_labels" {
  rule {
    source_labels = ["__meta_docker_container_name"]
    target_label  = "container"
  }
  rule {
    source_labels = ["__meta_docker_container_label_com_docker_compose_service"]
    target_label  = "service"
  }
  forward_to = []
}

discovery.docker "all" {
  host = "unix:///var/run/docker.sock"
}

loki.write "loki" {
  endpoint {
    url = "http://loki:3100/loki/api/v1/push"
  }
}
```

The log flow:
```
Docker daemon log buffer (per container)
    └─► loki.source.docker (Alloy polls Docker API)
        └─► loki.write → Loki :3100
            └─► Grafana (LogQL queries)
```

Labels automatically attached to every log line:
- `container` — e.g. `testenv-participant-3`
- `service` — e.g. `participant-3` (compose service name)
- `image` — e.g. `subnet-host:latest`

This means in Grafana you can do:
```logql
{service="participant-3"} |= "gossip"
```
and see all gossip-related log lines from participant-3 alongside its CPU and
memory graphs in the same panel.

---

## Quick start queries (MetricsQL)

After `make up`, open http://localhost:8428/vmui and try:

```promql
# CPU usage per container (rate over 1 minute)
rate(container_cpu_usage_seconds_total{name=~"testenv-participant.*"}[1m])

# Memory working set per participant
container_memory_working_set_bytes{name=~"testenv-participant.*"}

# Network traffic per participant (bytes received/s)
rate(container_network_receive_bytes_total{name=~"testenv-participant.*"}[1m])

# VM-level free memory (from Node Exporter)
node_memory_MemAvailable_bytes

# VM CPU load
node_load1
```

---

## IP address map (observability range: 172.30.0.100-119)

| IP | Service | Port | Purpose |
|---|---|---|---|
| `172.30.0.100` | `victoria-metrics` | `8428` | Metrics storage + MetricsQL query |
| `172.30.0.101` | `alloy` | `12345` | Pipeline UI + OTel collector |
| `172.30.0.102` | `cadvisor` | `8080` | Container metrics exposition |
| `172.30.0.103` | `node-exporter` | `9100` | Host/VM metrics exposition |
| `172.30.0.104` | *(reserved)* | — | Loki (Phase 2) |
| `172.30.0.105` | *(reserved)* | — | Tempo (Phase 2) |
| `172.30.0.106` | *(reserved)* | — | Grafana (Phase 2) |
