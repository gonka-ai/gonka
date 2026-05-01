# Smoke test — live-reload + multi-inference fan-out

End-to-end walkthrough for verifying a fresh `devshard/testenv` checkout.
The test has two parts:

1. **Live-reload sanity** — confirm `air` rebuilds and restarts every
   service when its Go source changes, and that the dlv listener is
   reachable for debug-enabled services.
2. **Multi-inference fan-out** — submit several inferences through the
   operator proxy (`devshardctl` on `:8081`): a short sequential loop
   (§4.2), then **20 concurrent** requests (§4.2b). Watch nonces spread
   across the 4 hosts (§4.3) and confirm each response has distinct
   (deterministic) content for a distinct inference id.

Both parts run against the default 4-host / 10-validator configuration
that `gencompose` generates. No production keys, no real mainnet.

## 0. Prerequisites

- Docker Desktop (or a Linux box with Docker Engine) and Docker
  Compose v2 (`docker compose …`, not `docker-compose`).
- `make`, `curl`, `jq`. Everything else is pulled by the dev image.
- Repo checked out at `gonka/` — this document lives under
  `devshard/testenv/spec/`; every path below is relative to
  `devshard/testenv/`.

Quick sanity:

```bash
cd devshard/testenv
docker compose version    # want v2.20+
make --version            # any modern GNU make
```

## 1. One-time setup

`gencompose` fills every missing private key, picks deterministic IPs,
and emits `docker-compose.yml`. It's idempotent, so re-running it after
edits to `config.yaml` is safe.

```bash
cd devshard/testenv
make gen-compose          # writes docker-compose.yml, rewrites config.yaml
```

Verify:

```bash
test -f docker-compose.yml                    && echo "compose OK"
grep -c '^  devshardd-testenv-' docker-compose.yml  # should print 4
grep -c 'private_key_hex: ""' config.yaml     # should print 0 (all filled)
```

## 2. Bring up the dev stack

The dev overlay layers live-reload + `dlv` on top of the base stack.
First run builds the shared toolchain image once (~60 s on M1).

```bash
make dev-build            # builds devshard-dev:latest
make dev-up               # starts mock-chain, height-sync, 4 devshardd-testenv, …
```

Wait for steady-state output in another pane:

```bash
make dev-logs             # follow every service
# or, per-host:
make dev-logs-0           # follow devshardd-testenv-0 only
```

Steady state is reached when every host logs a recurring `cPoC round`
tick and the height logged by height-sync matches what each host prints
as "latest block height".

## 3. Live-reload verification

### 3.1 Happy-path reload

Pick a cheap file with visible logging. `host/host.go` is a good
target because every host logs through it.

1. In one pane, tail host-0's logs:

   ```bash
   make dev-logs-0
   ```

2. In another pane, add a harmless `log` line to the `Host.Start`
   function in `devshard/host/host.go` (e.g.
   `logging.Logger.Info("live-reload probe")`). Save.

3. Within ~1 s you should see in pane 1:

   - `air` emit `building...` and then `running...`.
   - Host-0 re-log its startup banner (gossip peers, bridge URL, …).
   - The new `live-reload probe` line on the next `Host.Start` call,
     or immediately if the banner path emits it.

4. Remove the probe line and save again. The rebuild should kick off
   once more and converge.

### 3.2 Debug listener reachability

The overlay publishes three dlv ports to the host:

| Port   | Service               |
|--------|-----------------------|
| `2345` | `mock-chain`          |
| `2346` | `height-sync`         |
| `2347` | `devshardd-testenv-0` |

Probe each one is alive:

```bash
for p in 2345 2346 2347; do
  echo -n "dlv :$p → "
  curl -sS --max-time 1 http://127.0.0.1:$p >/dev/null 2>&1 && echo OK || echo "refused"
done
```

`curl` will receive no HTTP reply (dlv speaks its own protocol), but
the TCP connection *must* succeed. `refused` means either the container
is down or `SYS_PTRACE`/`seccomp:unconfined` was stripped.

Attach via VS Code: copy `vscode-launch.json` into `.vscode/launch.json`
and pick *Attach: devshardd-testenv-0*. Set a breakpoint in
`host/host.go` and trigger a request (see §4); the debugger should
halt on that line. After air rebuilds, the IDE drops the session —
click *Reconnect* to re-attach on the same port.

### 3.3 Cache behaviour

- `make dev-down` stops the containers **but keeps** the module /
  build caches (named volumes `gomodcache`, `gobuildcache`). Next
  `dev-up` rebuilds in seconds.
- `make dev-clean` drops those caches and next `dev-up` re-downloads
  the module graph (~30 s on a warm proxy).

## 4. Multi-inference fan-out test

### 4.1 Start the operator proxy

The base compose file gates `devshardctl` behind `profiles: ["tools"]`
so it doesn't come up by default. For the inference test we bring it
up explicitly so port `8081` is published:

```bash
docker compose --profile tools up -d devshardctl
docker compose --profile tools logs -f devshardctl &   # optional
```

Wait for a `devshardctl listening on :8081 …` line.

### 4.2 Send N inferences

The mock inference engine is deterministic: the same
`(InferenceID, EscrowID, Model)` triple always produces the same
response body and hash. Submitting several requests therefore gives us
distinct, predictable outputs we can diff against each other.

```bash
for i in $(seq 1 8); do
  echo "--- request #$i ---"
  curl -sS -X POST http://127.0.0.1:8081/v1/chat/completions \
    -H 'Content-Type: application/json' \
    -d "{
      \"model\": \"Qwen/Qwen2.5-7B-Instruct\",
      \"messages\": [{\"role\": \"user\", \"content\": \"ping $i\"}],
      \"max_tokens\": 128,
      \"stream\": false
    }" | jq -c '{id, choices: .choices[0].message.content | .[:40]}'
done
```

Expected behaviour:

- Each request returns a 200 with a non-empty
  `choices[0].message.content`.
- The per-request `id` (which is the inference nonce in hex) is
  strictly monotonically increasing.
- Content differs between requests — the engine seeds its output off
  the inference ID, so two pings never hash the same.

### 4.2b Twenty inferences, asynchronous (concurrent `curl`)

Same prerequisites as §4.1 (`devshardctl` on `127.0.0.1:8081`). This
fires **20** POSTs **at once** (background subshells), then waits for
all to finish. Good for catching races, proxy backlog, and host
contention that the sequential loop above does not stress.

```bash
outdir=$(mktemp -d)
trap 'rm -rf "$outdir"' EXIT
for i in $(seq 1 20); do
  (
    curl -sS -w "\n%{http_code}\n" -o "$outdir/body_$i.json" \
      -X POST http://127.0.0.1:8081/v1/chat/completions \
      -H 'Content-Type: application/json' \
      -d "{
        \"model\": \"Qwen/Qwen2.5-7B-Instruct\",
        \"messages\": [{\"role\": \"user\", \"content\": \"async ping $i\"}],
        \"max_tokens\": 128,
        \"stream\": false
      }" >"$outdir/meta_$i.txt" || echo "curl_failed" >"$outdir/meta_$i.txt"
  ) &
done
wait
# Every HTTP status line should be 200; bodies should be valid JSON.
bad=0
for i in $(seq 1 20); do
  code=$(tail -n1 "$outdir/meta_$i.txt")
  if [ "$code" != "200" ]; then echo "request $i: HTTP $code"; bad=$((bad+1)); fi
  jq -e '.choices[0].message.content|length>0' "$outdir/body_$i.json" >/dev/null 2>&1 || { echo "request $i: bad JSON or empty content"; bad=$((bad+1)); }
done
echo "failures: $bad (want 0)"
```

Optional — count distinct inference IDs (expect 20; order differs
from send order when concurrent):

```bash
jq -r '.id' "$outdir"/body_*.json | sort -u | wc -l
```

### 4.3 Verify fan-out across hosts

With 4 hosts and 16 slots (default config), nonce `k` routes to slot
`k mod 16`, and the base config assigns slots round-robin: slots
`{0,4,8,12}` to host-0, `{1,5,9,13}` to host-1, …

So 8 sequential inferences touch at least 3 of the 4 hosts; after
§4.2b you should see many more nonces across the hosts. Confirm by
scraping each host's logs for the nonces it executed:

```bash
for k in 0 1 2 3; do
  echo "--- devshardd-testenv-$k ---"
  docker compose logs --no-log-prefix devshardd-testenv-$k 2>&1 \
    | grep -E 'MsgStartInference|MsgConfirmStart|InferenceID' \
    | tail -10
done
```

You should see each host log at least one `MsgConfirmStart` for a
different nonce. If all 8 requests land on one host, either slot
assignment is broken (check `docker-compose.yml` `SLOT_INDEX` vars)
or the operator proxy is pinning every call to the same host (check
`DEVSHARDD_URL` / `--host` on `devshardctl`).

### 4.4 Finalize (optional)

Close the session and produce a signed settlement payload:

```bash
curl -sS -X POST http://127.0.0.1:8081/v1/finalize \
  -H 'Content-Type: application/json' \
  -d '{}' | jq '.'
```

The response is a `SettlementJSON`: escrow id, nonce, host stats,
and one `Signatures[]` entry per slot. Each signature's `slot_id`
must correspond to a slot that actually executed inferences above.
This round-trips `MsgSettleDevshardEscrow` through the `mock-chain`
gRPC bridge; no mainnet calls happen.

## 5. Tear down

```bash
docker compose --profile tools down           # stop devshardctl
make dev-down                                 # stop the rest (keeps caches)
# or
make dev-clean                                # full wipe including volumes
```

## 6. Troubleshooting

| Symptom                                        | Likely cause                                      | Fix                                                                                                              |
|------------------------------------------------|---------------------------------------------------|------------------------------------------------------------------------------------------------------------------|
| `air: building... failed: package not found`   | `.air.<svc>.toml` out of sync with cmd layout     | `go test ./testenv/ -run TestAirConfigs_ReferenceRealPackages` pinpoints the bad entry                           |
| dlv port refuses connection                    | `SYS_PTRACE` or `seccomp:unconfined` missing      | `docker inspect` the service; `go test ./testenv/ -run TestDockerComposeDev_DlvPortsMatchAirConfigs` re-asserts  |
| `curl: (7) Failed to connect to 127.0.0.1:8081`| devshardctl started via `make ctl` (`run --rm`)   | `docker compose --profile tools up -d devshardctl` instead — `run` does not publish ports                        |
| Every inference lands on host-0                | `DEVSHARDD_URL` env set in devshardctl container  | `docker compose exec devshardctl env` — look for `DEVSHARDD_URL`; unset it or pass `--host=""` to devshardctl      |
| Response content is identical across requests  | One request reached host, other N-1 failed retry  | Check `POST /v1/debug/pending` and host logs for `MsgTimeoutInference` spam                                      |
| `make dev-up` hangs building                   | Module cache empty + proxy unreachable            | Retry once; if persistent, `make dev-clean && make dev-up` to rebuild from scratch                               |

## 7. What this does *not* test

- Real mainnet or cross-epoch settlement (mock-chain is a fixed state).
- Validator set changes mid-run (fixed at `gencompose` time).
- Inference correctness beyond "the deterministic engine is
  deterministic" — that's the job of `engine/*_test.go`.
- Observability / metrics — tracked by Phase 13, not yet landed.

Use this spec as the "does my laptop run the stack?" check, and the
Go test suites for everything else.
