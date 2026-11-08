# Gateway load simulation

`cmd/devshardctl/loadsim_test.go` runs the pooled gateway as `mustBuildGateway` and `buildGatewayHandler` wire it — `Gateway`, `Proxy`, `Redundancy`, `PerfTracker`, a SQLite-backed `user.Session` per escrow — against real `host.Host` instances living in the same process, and drives streaming chat traffic at it over HTTP while it profiles. It is behind the `loadsim` build tag, so `go test ./...` never runs it: it is a measurement, and it loads the machine it runs on.

```bash
cd devshard/cmd/devshardctl
LOADSIM_ESCROWS=4 LOADSIM_RPS=30 LOADSIM_DURATION=150s LOADSIM_PROFILE_DIR=/tmp/loadsim \
  go test -tags loadsim -run TestLoadSimulation -count=1 -timeout 8m -v . > /tmp/loadsim/run.log 2>&1
grep loadsim_test.go /tmp/loadsim/run.log
go tool pprof -sample_index=alloc_space -ignore='devshard/host\.' -top -cum -nodecount=40 /tmp/loadsim/allocs.pprof
```

| Variable | Default | What it sets |
| --- | --- | --- |
| `LOADSIM_ESCROWS` | 4 | escrows served at once, one in-process group each |
| `LOADSIM_GROUP_SIZE` | 4 | hosts per escrow |
| `LOADSIM_RPS` | 30 | requests per second, sent whatever the gateway answers |
| `LOADSIM_WARMUP` | 5s | traffic before measuring |
| `LOADSIM_DURATION` | 30s | the measured window |
| `LOADSIM_INFERENCE_LATENCY` | 2s | how long each host takes to answer, which is what fills the in-flight windows |
| `LOADSIM_MAX_IN_FLIGHT` | 512 | requests open at once; an arrival past it is counted as shed, not sent |
| `LOADSIM_REPORT_EVERY` | 30s | how often the run logs the last interval's cost beside the mean nonce, live-inference count and live heap |
| `LOADSIM_PROFILE_DIR` | a temporary directory | where `cpu`, `mutex`, `block`, `heap` and `allocs` profiles land |

The run reports answered requests and their statuses, latency quantiles, process CPU and allocations per answered request, GC cycles and live heap. The interval lines are what show a cost growing with the escrow's history: nothing seals within a run (a finished inference seals once the escrow's clock has moved 3600 s past it), so the live window grows with every nonce the way it does during the first hour of a production escrow.

What it does not measure:

- **Hosts and gateway share one process.** Their CPU and allocations land in one profile; filter the hosts out with `-ignore='devshard/host\.'`. The host-side transport is not there at all — `user.InProcessClient` calls the host directly, so HTTP to the hosts, TLS and signature transport cost nothing here.
- **No GPU.** The hosts answer from `stub` after a fixed delay.
- **No heartbeat.** The heartbeat loop needs a height, and the only source is the anchors hosts return through the HTTP transport, which `InProcessClient` skips, so the loop is not started.
- **No chain.** No runtime params, PoC phase gate, escrow rotation or settlement; every escrow is funded far beyond any run.
- **Logging goes to the test's stdout.** `logInferenceStage` writes synchronously, so its CPU share depends on where stdout goes; redirect it to a file as above, not to a terminal.

## Startup simulation

`cmd/devshardctl/loadsim_startup_test.go`, `TestStartupSimulation`, under the same tag, fills a `perf.db` the way serving does — each request's host samples, request log row and accounting rows written in turn, so the tables' pages interleave through the file as they do on a node — and then opens it the way `mustBuildGateway` does, timing `NewPerfStore` and `NewPerfTracker`. With `STARTUPSIM_PRUNE=1` it then runs one prune pass at production pacing beside a writer that inserts a sample every 10 ms, and reports what the pass deleted, how long it took and the writer's latency.

| Variable | Default | What it sets |
| --- | --- | --- |
| `STARTUPSIM_REQUESTS` | 200000 | requests written into a new `perf.db` (about 2.2 KB each across the tables) |
| `STARTUPSIM_SAMPLES_PER_REQUEST` | 3 | host samples per request |
| `STARTUPSIM_DAYS` | 120 | the span the requests' times cover |
| `STARTUPSIM_DIR` | a temporary directory | where `perf.db` lives; an existing one is reused, not refilled |
| `STARTUPSIM_PRUNE` | unset | `1` runs the prune pass |

A warm page cache hides the cost, so measure cold and on a throttled disk. On Docker Desktop the database has to live on a volume inside the VM, and the test binary is built for Linux:

```bash
cd devshard
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go test -c -tags loadsim -o /tmp/startupsim.test ./cmd/devshardctl
docker volume create startupsim-data
docker run --rm -v startupsim-data:/data -v /tmp:/simulation:ro -e STARTUPSIM_REQUESTS=1000000 -e STARTUPSIM_DIR=/data \
  alpine /simulation/startupsim.test -test.run TestStartupSimulation -test.v -test.timeout 30m
docker run --rm --privileged alpine sh -c 'sync; echo 3 > /proc/sys/vm/drop_caches'
docker run --rm --device-read-iops /dev/vda:1000 -v startupsim-data:/data -v /tmp:/simulation:ro -e STARTUPSIM_DIR=/data \
  alpine /simulation/startupsim.test -test.run TestStartupSimulation -test.v -test.timeout 30m
docker volume rm startupsim-data && rm /tmp/startupsim.test
```

What startup reads from `perf.db` and what the background pruner deletes is described in [host-health.md](./host-health.md), "perf.db: what startup reads and what is pruned", together with the measurements this simulation produced.
