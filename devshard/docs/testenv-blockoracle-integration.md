# Block oracle integration in devshard testenv (Phase 5)

This document complements `[testenv.md](testenv.md)` (Phase 5 — `BlockOracle` on `host.Host`) and `[../testenv/README.md](../testenv/README.md)`. It explains how the **mock mainnet block oracle** is started, how it behaves, how it is wired from `config.yaml` / `gencompose`, and where to read the code that consumes heights on **devshardd hosts**.

---

## 1. Names and containers

In this repo the oracle is **not** a binary named `blockoracle`. Operationally:


| What you call it                  | Container / binary                | Package                                                                           |
| --------------------------------- | --------------------------------- | --------------------------------------------------------------------------------- |
| **height-sync**                   | Docker service `height-sync`      | `devshard/testenv/cmd/heightsyncd`                                                |
| **Standalone oracle HTTP server** | Same process                      | `devshard/blockoracle/standalone` → mounts `observer.Mock` + `server` routes      |
| **In-process oracle API**         | Used inside hosts via HTTP client | `devshard/blockoracle` (`BlockOracle` interface), `blockoracle/client` (consumer) |


Production-shaped **Phase 5** wiring is: inject `blockoracle.BlockOracle` into `host.Host` via `host.WithBlockOracle`, and read chain height through `Host.LatestHeight` / raw `Host.BlockOracle()` — see `[testenv.md](testenv.md)` §5.

---

## 2. How to start devshard and the oracle

Working directory: `**devshard/testenv`** (paths below are relative to that).

### 2.1 Base stack (oracle always included)

The compose file emitted by `gencompose` defines `**mock-chain**`, `**height-sync**`, and `**devshardd-testenv-***`. Hosts `**depends_on: height-sync**`, so bringing hosts up requires the oracle container.

```bash
make gen-compose    # writes docker-compose.yml from config.yaml (once or after config edits)
make dev-build      # builds base images
make hot-up         # starts mock-chain, height-sync, and all devshardd hosts with hot reload
```

Check the oracle is listening on the host-mapped port (default **9100**):

```bash
curl -sS http://127.0.0.1:9100/healthz
curl -sS http://127.0.0.1:9100/block/latest | jq '.Height, .ChainID, (.Commit.Signatures|length)'
```

Follow logs:

```bash
docker compose logs -f height-sync
```

### 2.2 Dev overlay (live-reload + `dlv`)

Same oracle service name; overlay stacks extra dev behaviour:

```bash
make dev-build   # first time / after Dockerfile changes
make dev-up      # docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d
make dev-logs    # all services, or narrow with compose logs height-sync
```

### 2.3 Optional: `devshardctl` (operator HTTP proxy + oracle auditing shell)

`devshardctl` is **not** the block oracle. It is an **inference / session** front-end. It does **not** subscribe to heights as part of runtime protocol flow; however, the container is useful as an operator shell to query `height-sync` endpoints directly (full header or height/hash view) for debugging and audits; see §6.

```bash
docker compose up -d devshardctl
```

---

## 3. How the mock oracle works

### 3.1 Producer: `observer.Mock`

`heightsyncd` maps `config.yaml` → `standalone.Config` and runs `standalone.Run`, which:

1. Builds an in-memory `**observer.Mock**` with the configured validator set.
2. Starts a **ticker** that calls `Mock.AdvanceOne()` on each **block interval** (see §5).
3. Serves JSON over HTTP: `GET /block/latest`, `GET /block/{h}`, `GET /block/stream`, etc. (`devshard/blockoracle/server`).

On each new height the mock fabricates a header (hashes are deterministic from `seed` + height), then `**signCommit`** signs the canonical header bytes with a **subset** of validators:

- **How many signatures?** Per height, a deterministic **drop set** may skip some validators (`observer.Mock.pickDropSet`), but the **remaining voting power is always strictly > 3/4** of total power (stricter than the verifier’s **> 2/3** rule).
- **“Valid” vs “invalid” signatures:** Every signature **present** in `Commit.Signatures` is a **real** secp256k1 signature from the configured key for that validator address. The mock does **not** inject corrupt signatures. What changes across heights is **which validators appear** (some heights include all signers; others omit some). Omitted validators are **missing precommits**, not invalid cryptography.

So when tuning tests, think in terms of: **validator count**, **powers**, **how often the mock omits signers** (driven by `seed` + height), not a separate knob for “bad sig count.”

### 3.2 Cadence (how often height advances)

Controlled by `**standalone.Config.BlockInterval`**, filled from config helpers in `devshard/testenv/config`:

- Prefer `height_sync.block_interval` (duration string, e.g. `500ms`, `2s`).
- Optional jitter via `height_sync.block_interval_delta` (symmetric ±delta around the mean interval).
- Else fall back to `chain.block_time`.
- If unset / unparsable: **1 second**.

See `Config.HeightSyncBlockInterval()` in `devshard/testenv/config/config.go`.

### 3.3 Logs: what to look for

On startup `**heightsyncd`** prints one summary line (listen address, chain id, **validator count**, **interval**, **seed**, **initial height**):

```text
heightsyncd listening on :9100 chain="..." validators=N interval=1s seed=0 initial_height=1
```

Use:

```bash
docker compose logs height-sync | head -20
docker compose logs -f height-sync
```

---

## 4. How integration is built into config / compose

### 4.1 Single YAML source

`devshard/testenv/config.yaml` is the contract:

```yaml
chain:
  id: gonka-testenv-1
  block_time: 1s          # fallback cadence if height_sync.block_interval empty

height_sync:
  port: 9100
  initial_height: 1
  seed: 0                 # deterministic hashes & drop-set stream
  block_interval: ""      # optional override; empty → use chain.block_time → 1s
  block_interval_delta: "" # optional jitter: sampled interval in [block_interval-delta, block_interval+delta]
  validators:
    - { private_key_hex: "…", power: 1 }
    # … N validators (default skeleton uses 10 × power 1)
```

- `**validators**`: defines **how many signers** exist and their **power**. `gencompose` fills empty/TODO keys with deterministic hex.

### 4.2 `gencompose` wiring

`go run ./cmd/gencompose` (or `**make gen-compose`**) renders `docker-compose.yml`:

- Service `**height-sync**`: image `devshard-height-sync`, env `CONFIG_PATH`, `HEIGHT_SYNC_PORT`, bind-mount `config.yaml`, publishes `height_sync.port`, `**depends_on: mock-chain**`.
- Each `**devshardd-testenv-***`: env `**HEIGHT_SYNC_URL: http://height-sync:<port>**` (and `CHAIN_ID`, `MOCK_CHAIN_URL`, …), `**depends_on: mock-chain` + `height-sync**`.

So the oracle is **always part of the generated stack**; you do not start it manually unless you run `heightsyncd` outside Docker for local debugging.

### 4.3 Host wiring at runtime

`devshard/testenv/cmd/devshardd-testenv/main.go`:

1. `**mockdapi.New(ctx, Config{ HeightSyncURL: $HEIGHT_SYNC_URL, … })`** builds a `**blockoracle/client**` HTTP+SSE client pointed at `height-sync`.
2. `**host.NewHost(..., host.WithBlockOracle(md.Oracle))**` injects that client.

Trust mode for hosts: `**Verifier: nil**` in `mockdapi/newOracleClient` — the host **trusts** the oracle URL and **does not** run `verifier.Verifier` on ingest; it still **stores** full `Commit.Signatures` for downstream proof forwarding. Verification on ingest happens only when a consumer passes a non-nil verifier (e.g. tests / auditors).

Code:

- `devshard/testenv/mockdapi/blockoracle.go` — `newOracleClient`
- `devshard/blockoracle/client/http.go` — `verify()` skips if `Verifier == nil`

---

## 5. Quick tuning reference


| Question                                           | Where                                                         |
| -------------------------------------------------- | ------------------------------------------------------------- |
| How many validators / signers?                     | `len(height_sync.validators)` in `config.yaml`                |
| Block interval / emission rate                     | `height_sync.block_interval` or `chain.block_time` (see §3.2) |
| Block interval randomness (jitter)                 | `height_sync.block_interval_delta` (symmetric ±delta)          |
| Deterministic “which validators sign this height”? | `height_sync.seed` + height → `observer.Mock.pickDropSet`     |
| HTTP port                                          | `height_sync.port` (default 9100)                             |


After edits: `**make gen-compose`** (if compose embeds ports), then `**make hot-up**` (or `**make dev-up**`).

---

## 6. Where heights are read and what data shape is available

### 6.1 `devshardd` host path: full header cached, latest height consumed


| Concern                                        | Location                                                                                          |
| ---------------------------------------------- | ------------------------------------------------------------------------------------------------- |
| `**Host.LatestHeight` → oracle latest height** | `devshard/host/host.go` — calls `h.oracle.Latest(ctx)` and reads `.Height`                         |
| **Expose raw oracle**                          | `host.Host.BlockOracle()` same file                                                               |
| **Constructor injection**                      | `host.WithBlockOracle` in `host.go`                                                               |
| **HTTP client trust / verify skip**            | `devshard/blockoracle/client/http.go` `verify()`                                                   |
| **Testenv process wiring**                     | `devshard/testenv/cmd/devshardd-testenv/main.go` — `WithBlockOracle(md.Oracle)`                   |
| **Periodic sampling for metrics**              | same `main.go` — `startMetricsAndSampler` calls `h.LatestHeight` every 5s when `EXPORT_METRICS=1` |


`Host.LatestHeight()` currently consumes a **scalar** (`Height`) for protocol stamping/metrics, but the underlying cached object is the full `blockoracle.Header` (hashes + commit signatures), so the host can expose/forward verifiable material where needed.

### 6.2 Data shape modes (latest HEIGHT_SYNC_HEADERS design)

The same oracle endpoint supports two consumption modes:

1. **Full verifiable header** (for validation/audit):
   - Includes `Height`, `ChainID`, `BlockHash`, `AppHash` (Merkle root), validator hashes, and full `Commit.Signatures`.
   - Endpoint: `GET /block/latest` (or `/block/{height}`).
2. **Compact projection** (for lightweight checks):
   - Read only `Height` and `BlockHash` from the same payload.
   - Useful for dashboards/log checks where full signature material is unnecessary.

This matches the latest height-sync design: consumers may use full authenticated data when needed, or just height/hash when that is sufficient.

### 6.3 `devshardctl` relation to height-sync

`devshardctl` must be treated as a **client of `devshardd`**, not as a direct `height-sync` consumer for protocol checks. The runtime flow is:

`devshardctl -> devshardd -> blockoracle client -> height-sync`

Current state in `main`:

- `devshardctl` endpoints (`/v1/status`, `/v1/debug/state`, etc.) do **not** expose oracle header payloads.
- `devshardctl` therefore cannot yet assert via `devshardd` API whether it received **full** header/proof fields vs a **compact** projection.

### 6.4 Correct test plan (devshardctl via devshardd only)

Ensure `devshardctl` is up (included in default `docker compose up -d`):

```bash
docker compose up -d devshardctl
```

#### 6.4.1 What can be tested **today** (no new API)

1. Verify `devshardctl` talks to `devshardd`:

```bash
curl -sS http://127.0.0.1:8080/v1/status | jq .
```

2. Trigger normal traffic through `devshardctl` (chat/inference path), then check `devshardd` logs for oracle ingest/cache debug lines:

```bash
# Example traffic through devshardctl:
curl -sS -X POST http://127.0.0.1:8080/v1/chat/completions \
  -H "content-type: application/json" \
  -d '{"model":"test","messages":[{"role":"user","content":"ping"}],"max_tokens":8}'

# Confirm devshardd received/cached height-sync frames:
docker compose logs --since=2m devshardd-testenv-0 | rg "blockoracle: stream frame received|blockoracle: header cached"
```

This proves the chain data path is active in `devshardd`, but it does **not** provide a structured API assertion through `devshardctl`.

Important clarification:

- In current testenv wiring, `devshardd` subscribes to `height-sync` continuously (SSE stream) and ingests headers on producer cadence (`height_sync.block_interval` / fallback `chain.block_time`), **not** only when an inference request arrives.
- Inference requests are used here only as an easy "traffic is alive" signal while observing logs.
- The proposal-level Anchor/Omit/Strong schedule (`K`, `D`) from [`proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md`](./proposals/HEIGHT_SYNC_PROTOCOL_PROPOSAL.md) is **target design** and is not yet fully surfaced as runtime envelope behavior or testenv config knobs (`K`/`D` are not currently configurable in `testenv/config.yaml`).

#### 6.4.2 Required follow-up for full vs compact assertions

To test exactly what you asked (through `devshardctl -> devshardd`), add a debug endpoint in `devshardd` and proxy it in `devshardctl`, for example:

- `GET /v1/debug/oracle/latest?mode=full`
- `GET /v1/debug/oracle/latest?mode=light`

Expected payload contract:

- `mode=full`: `Height`, `ChainID`, `BlockHash`, `AppHash`, `ValidatorsHash`, `NextValidatorsHash`, `Commit.{Height,Round,BlockID,Signatures[]}`.
- `mode=light`: `Height`, `BlockHash` only.

Then test via `devshardctl` only:

```bash
curl -sS "http://127.0.0.1:8080/v1/debug/oracle/latest?mode=full" | jq .
curl -sS "http://127.0.0.1:8080/v1/debug/oracle/latest?mode=light" | jq .
```

Pass criteria:

- `full` contains proof-capable fields + non-empty signature list.
- `light` omits proof fields and returns only height/hash.
- both modes refer to the same latest height.

If you need an **integration-style automated check** that the oracle + verifying client work together, see `**devshard/testenv/citest/stack_integration_test.go`** and `**devshard/testenv/cmd/heightsyncd/main_test.go**` (`TestHeightSync_SignsWithConfiguredValidators`).

---

## 7. Further reading

- Design / phases: `[testenv.md](testenv.md)` — §3 (architecture), **Phase 3** (height-sync), **Phase 5** (host seam), **Phase 6** (mockdapi).
- Operator runbook: `[../testenv/README.md](../testenv/README.md)` §1–3.
- Blockoracle API shape: `devshard/blockoracle/oracle.go`, `devshard/blockoracle/server/http.go`.

