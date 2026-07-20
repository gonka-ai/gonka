# devshard-testing

End-to-end test harness for the devshard protocol. Creates on-chain escrows, starts `devshardctl` proxy instances, sends inferences, and optionally finalizes escrows for settlement checks.

## What it does

1. Creates N devshard escrows on chain (or reuses escrows from a state file).
2. Starts one `devshardctl` process per escrow, each on its own local port.
3. Sends M inferences per escrow, logging which host will execute each one.
4. Optionally finalizes escrows and checks settlement data when `--finalize` is set.

Finalization is disabled by default — escrows stay open for manual inspection unless `--finalize` is set.

## Prerequisites

- `devshardctl` binary in PATH (or passed via `--devshardctl`)
- SSH tunnel to the testnet node (chain gRPC on `localhost:9090`, chain REST on `localhost:1317`)
- A funded account private key (raw hex)

## Building

```bash
go build -o devshard-testing .
```

## Usage

```bash
./devshard-testing \
  --grpc localhost:9090 \
  --rest http://localhost:1317 \
  --private-key <hex> \
  --route-prefix /devshard/v0.2.12 \
  --model Qwen/Qwen3-4B-Instruct-2507 \
  --count 3 \
  --inferences 2
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--grpc` | — | Chain gRPC endpoint `host:port` (required) |
| `--rest` | — | Chain REST URL for devshardctl (required) |
| `--private-key` | — | Raw hex private key (required) |
| `--route-prefix` | — | Devshard route prefix, e.g. `/devshard/v0.2.12` (required) |
| `--model` | `Qwen/Qwen2.5-7B-Instruct` | Model ID (must match a chain-registered model) |
| `--count` | `3` | Number of escrows to create |
| `--inferences` | `2` | Inferences to send per escrow |
| `--finalize` | `false` | Finalize escrows after inferences and remove the state file |
| `--amount` | `5_000_000_000` | Escrow amount in ngonka |
| `--base-port` | `18080` | First local port for devshardctl |
| `--devshardctl` | `devshardctl` | Path to devshardctl binary |
| `--state-file` | `devshard-test-state.json` | Escrow ID persistence file |
| `--reset` | `false` | Ignore state file, create fresh escrows |
| `--grpc-tls` | `false` | Use TLS for gRPC connection |
| `--chain-id` | `gonka-mainnet` | Chain ID |

### Checking registered models

```bash
curl http://localhost:1317/productscience/inference/inference/models_all
```

## State file

On first run escrow IDs are saved to `devshard-test-state.json`. Subsequent runs reuse open escrows from that file, only creating new ones to reach `--count`. Use `--reset` to ignore the file and start fresh.

## Output

Each inference log line includes the predicted executor host address:

```
Sending inference 1/2 for escrow 42 (nonce 1 → host gonka1abc...)
```

With `--finalize`, settlement data is checked after inferences:

```
Finalizing escrow 42...
  nonce=2 slots=[1 7 12]
```

Without `--finalize`, escrows stay open for manual inspection.
