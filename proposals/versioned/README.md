# Proposal: Versioned

## Goal / Problem

Devshard nodes need to run multiple binary versions concurrently behind a single endpoint. Cosmovisor handles this for chain nodes but is fragile, single-version-only, and not production-grade.

We need a lightweight version manager that:
- Polls an oracle for which versions to run
- Downloads, verifies, and runs binaries
- Reverse-proxies traffic to the right version (keeps version routing internal, avoids pushing version awareness into nginx/ingress config)
- Works identically in containers and on bare metal


## On-Chain State

Approved versions are stored in the inference module `Params` within `DevshardEscrowParams`.

```proto
message DevshardEscrowParams {
  option (gogoproto.equal) = true;
  // ... existing escrow fields ...
  repeated DevshardApprovedVersion approved_versions = N;
}

message DevshardApprovedVersion {
  option (gogoproto.equal) = true;
  string name = 1;    // e.g. "v0.2.11"
  string binary = 2;  // download URL (GitHub releases, S3, mirror -- any HTTP source)
  string sha256 = 3;  // required, hex-encoded sha256 of the zip archive
}
```

Changes only via governance proposal through the existing `MsgUpdateParams` flow. No dedicated query -- use the existing params query and read the approved versions from `DevshardEscrowParams`.

The `sha256` field is always required. sha256 is the sole identity for a binary -- the URL is a download hint only. Changing the URL while keeping the same hash is a no-op. Two governance proposals pointing to different mirrors but the same hash result in zero restarts.

### Example governance proposal

```json
{
  "messages": [
    {
      "@type": "/inference.inference.MsgUpdateParams",
      "authority": "inference10d07y265gmmuvt4z0w9aw880jnsr700j2ghlke",
      "params": {
        "devshard_escrow_params": {
          "approved_versions": [
            {
              "name": "v0.2.11",
              "binary": "https://github.com/gonka-ai/gonka/releases/download/release%2Fv0.2.11/devshard-amd64.zip",
              "sha256": "e574c3d86189daf325cc7008603ee8e952efb028afda5bcd4a154dcd334192d4"
            },
            {
              "name": "v0.2.12",
              "binary": "https://github.com/gonka-ai/gonka/releases/download/release%2Fv0.2.12/devshard-amd64.zip",
              "sha256": "a1b2c3d4e5f67890abcdef1234567890abcdef1234567890abcdef1234567890"
            }
          ]
        }
      }
    }
  ],
  "title": "Update devshard approved versions",
  "summary": "Add v0.2.12 to approved devshard versions"
}
```


## Decentralized API

The decentralized-api reads approved versions from chain params and serves them to versiond. No local storage, no admin CRUD, no config files.

### How it works

The block dispatcher (`new_block_dispatcher.go`) already queries chain params on every new block and caches them in `configManager`. The same pattern applies here:

1. Dispatcher reads `params.devshard_escrow_params.approved_versions` from the chain params response
2. Writes to a `DevshardVersions` cache field in `configManager` (same as `ValidationParams`, `BandwidthParams`, etc.)
3. A single `GET /versions` handler on the ML server port (9100) reads from this cache and returns JSON

No new goroutines, no polling intervals, no config flags. The existing block event loop drives updates.

### What gets removed

The entire `internal/versioned/` package is deleted:
- `store.go` (JSON file store with Put/Delete/flock) -- replaced by configManager cache field
- `handler.go` (admin CRUD handlers) -- replaced by one read-only GET handler
- `store_test.go`, `handler_test.go`
- All admin API versioned routes (PUT, DELETE, GET /binaries)
- All versioned-related config (config_path, binary_dir, enabled flag)

### Version config format

`GET /versions` on port 9100 returns:

```json
{
  "versions": [
    {
      "name": "v0.2.11",
      "binary": "https://github.com/gonka-ai/gonka/releases/download/release%2Fv0.2.11/devshard-amd64.zip",
      "sha256": "e574c3d86189daf325cc7008603ee8e952efb028afda5bcd4a154dcd334192d4"
    }
  ]
}
```

Same format versiond expects. The `sha256` field is always present.


## Version Manager (`versiond`)

Single Go binary. Manages child processes + built-in reverse proxy. Lives in `/versioned/`.

Responsibilities:
- Poll `GET /versions` on dapi ML port (9100) every 30s (configurable)
- Download and verify new binaries
- Start/stop child processes with zero-downtime swaps on hash changes
- Reverse-proxy incoming traffic with streaming support
- Forward signals to children on shutdown
- Support manual binary overrides and forced versions for development

### Directory Layout

```
/opt/versiond/
  bin/
    v0.2.11/devshard        # extracted binary (or atomic copy from override)
    v0.2.12/devshard
  data/
    v0.2.11/              # version-specific data dir, passed as --data-dir to child
    v0.2.12/
```

### Reconciliation Loop

sha256 is the sole identity. URLs are download hints only. Every cycle, versiond builds the desired set (oracle versions + forced versions), then converges:

```
# Step 0: build desired set
desired = oracle versions
for each forced version not already in desired:
  if override exists -> append to desired
  else -> skip with warning

# Step 1: process desired versions
for each version in desired:
  binPath = bin/{name}/{binaryName}

  case OVERRIDE (name in cfg.Overrides):
    hash override source file
    hash binPath (if exists)
    if hashes match AND already running -> do nothing
    if hashes differ OR binary missing:
      atomic copy override source -> binPath
      if running -> stop old, start new child
      else -> start child

  case RUNNING (name in processes):
    resolve desired sha256
    compute sha256 of binPath
    if matches -> do nothing
    if mismatch:
      download new binary to temp (zero-downtime: old keeps running)
      THEN stop old process
      atomic rename temp -> binPath
      start new child

  case NOT RUNNING, BINARY EXISTS on disk:
    resolve desired sha256
    compute sha256 of binPath
    if matches -> start child
    if mismatch -> delete, download, start

  case NOT RUNNING, NO BINARY:
    download, start

# Step 2: stop versions not in desired set
for each running version not in desired:
  stop process
```

Key properties:
- sha256 is the sole identity, URL is irrelevant for comparison
- Download happens BEFORE stopping old process (zero-downtime swap)
- Cached binaries are verified on every startup (no blind trust)
- Overrides use atomic copy to bin/{name}/, detect source changes, and restart
- Forced versions feed into the same loop naturally
- Hash verification failure never stops existing versions

### Port Assignment

Ports are assigned from a base port (5000) and persist for the manager's lifetime. Once a version name gets a port, it keeps it across stop/start cycles. The assignedPorts map is only cleared on full manager shutdown.

### Manual Override

For development and debugging, operators can override specific version binaries via environment variables:

```
VERSIOND_OVERRIDE_v0_2_11=/local/path/to/devshard
VERSIOND_OVERRIDE_v0_2_12=/another/path/to/devshard
```

Dots in version names become underscores in the env var name. When an override is set:
- versiond atomically copies the override binary to bin/{name}/
- sha256 of the override source is compared against the copy on disk
- If the operator swaps the file the env var points to, versiond detects the change and restarts
- The version must still appear in the oracle response (or be forced) to be active

### Force Versions

`VERSIOND_FORCE=v1,v2,v3` -- comma-separated list of version names that must run regardless of oracle state.

Forced versions must also have a corresponding `VERSIOND_OVERRIDE_<name>` pointing to a local binary. Force without override is a config error (logged at startup, skipped during reconcile).

Use case: testing/rolling out a version before it passes governance. The operator builds the binary locally, sets the override path, and forces it into the running set.

### Reverse Proxy

versiond listens on :8080 (hardcoded). Routes by path prefix:

```
/v0.2.11/*  ->  localhost:5000
/v0.2.12/*  ->  localhost:5001
```

The prefix is stripped before forwarding. A request to `versiond:8080/v0.2.11/chat/completions` hits `localhost:5000/chat/completions`.

Why internal proxy instead of external nginx routing: version set changes dynamically based on oracle state. Pushing version awareness into nginx/ingress config means syncing two systems. Keeping routing inside versiond means one component owns the full lifecycle -- download, run, route, stop. External infra only sees a single port.

Streaming and SSE: `httputil.ReverseProxy` with `FlushInterval: -1` flushes every write immediately. This is required for `/chat/completions` with `stream: true` (SSE). No buffering, no additional config. Works out of the box for SSE and chunked transfer encoding.

Routing table updates: the poll goroutine builds a new immutable route map and swaps it via `atomic.Value`. Request handlers load the current map with zero lock contention. In-flight requests continue on the old map, new requests use the updated one.

`GET /healthz` returns aggregate health: list of versions, their ports, and process status (running/stopped/starting).

### Signal Handling and PID 1

In containers, use `tini` as PID 1 for zombie reaping. versiond handles signal forwarding to children:

```dockerfile
FROM alpine:3.20
RUN apk add --no-cache tini
COPY versiond /usr/bin/versiond
ENTRYPOINT ["tini", "--"]
CMD ["versiond"]
```

On SIGTERM/SIGINT:
1. Stop accepting new connections
2. Send SIGTERM to all children
3. Wait up to 10s for graceful shutdown
4. SIGKILL remaining children
5. Exit

### Logging

versiond sets one per-child env var on each child:

- `DEVSHARD_BINARY_LOG_VERSION` — build id read from the child via `--print-binary-version` (e.g. `0.2.13-v2-r2`). Used as the log-line prefix. **Not** the protocol slot name (`v2`). devshardd checks it matches the link-time `DEVSHARD_BINARY_VERSION` stamp.

The protocol slot name (`approved_versions.name`, e.g. `v2`) is embedded via `DEVSHARD_VERSION` at build (`--print-protocol-version`). versiond routes by slot name and verifies it matches before starting the child. Session binding and settlement use the protocol name only.

Build example:

```bash
make devshardd-build DEVSHARD_VERSION=v2 DEVSHARD_BINARY_VERSION=0.2.13-v2-r2
```

### Configuration

All via environment variables:

| Variable | Default | Description |
|---|---|---|
| VERSIOND_ORACLE_URL | (required) | Oracle endpoint, e.g. `http://api:9100/versions` |
| VERSIOND_POLL_INTERVAL | 30s | Oracle poll interval |
| VERSIOND_BIN_DIR | /opt/versiond/bin | Binary storage |
| VERSIOND_DATA_DIR | /opt/versiond/data | Per-version data directories |
| VERSIOND_BINARY_NAME | devshard | Expected binary name inside zip |
| VERSIOND_OVERRIDE_{VERSION} | (none) | Local binary path override per version |
| VERSIOND_FORCE | (none) | Comma-separated version names to force-run |

Listen address is hardcoded to :8080. Base port is hardcoded to 5000.


## Data Flow

```
governance proposal
    |
    v
chain params (DevshardEscrowParams.approved_versions)
    |
    v
dapi block dispatcher (every block) -> configManager cache
    |
    v
GET /versions on ML port (9100)
    |
    v
versiond (polls every 30s)
    |
    +-- build desired set (oracle + forced versions)
    +-- for each version: compare sha256 on disk vs desired
    +-- download new binary BEFORE stopping old (zero-downtime)
    +-- atomic copy/rename into bin/{name}/
    +-- start process on assigned port (from base 5000)
    +-- proxy traffic on :8080
```


## Implementation

```
/inference-chain/
  proto/inference/inference/params.proto     -- add DevshardApprovedVersion to DevshardEscrowParams
  x/inference/types/params.go               -- validation, defaults

/decentralized-api/
  internal/versioned/                       -- DELETE entire package
  internal/event_listener/
    new_block_dispatcher.go                 -- read approved_versions, write to configManager
  internal/server/ml/                       -- add GET /versions handler
  apiconfig/                                -- add DevshardVersions cache field to configManager

/versioned/
  internal/config/config.go                 -- ForceVersions, hardcoded BasePort/ListenAddr
  internal/process/manager.go               -- hash-based reconcile, zero-downtime swap, force support
  internal/oracle/client.go                 -- no changes (format unchanged)
```

### Build

```makefile
# in /versioned/Makefile
versiond:
	go build -o build/versiond ./cmd/versiond
```

### Bare metal usage

```ini
# /etc/systemd/system/versiond.service
[Unit]
Description=Devshard Version Manager

[Service]
ExecStart=/usr/bin/versiond
Environment=VERSIOND_ORACLE_URL=http://localhost:9100/versions
Restart=always

[Install]
WantedBy=multi-user.target
```

### Docker compose (example)

```yaml
versiond:
  image: ghcr.io/gonka-ai/versiond:latest
  environment:
    VERSIOND_ORACLE_URL: http://api:9100/versions
    # Optional: override a version for local testing
    # VERSIOND_OVERRIDE_v0_2_11: /opt/local/devshard-dev
    # Optional: force versions not yet in governance
    # VERSIOND_FORCE: v0.2.13-rc1
  ports:
    - "8080:8080"
```
