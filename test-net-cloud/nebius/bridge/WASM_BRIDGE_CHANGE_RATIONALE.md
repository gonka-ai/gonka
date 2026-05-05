# WASM Bridge Change Rationale

Scope: this document covers the Wasm bridge permission changes introduced by
commit `148293c0bbb568b3d9a5d1f92ebe6d733f982ede` plus the current uncommitted
worktree changes that are directly related to the Wasm unwrap path and bridge
testnet scripts.

## Problem Being Fixed

`bridge-token-unwrap.sh` executes a CW20 contract message:

- [`bridge-token-unwrap.sh`](bridge-token-unwrap.sh#L105-L111) builds
  `{withdraw: {amount, destination_bridge_address, destination_address}}` and
  runs `tx wasm execute`.
- The CW20 contract dispatches `MsgRequestBridgeWithdrawal`.
- [`permissions.go`](../../../inference-chain/x/inference/keeper/permissions.go#L99)
  requires `ContractPermission` for `MsgRequestBridgeWithdrawal`.
- `ContractPermission` needs the Wasm keeper to answer whether the signer is an
  actual contract address.

The observed failure was a node panic during simulation:

```text
RequestBridgeWithdrawal
  -> CheckPermission
  -> checkContractPermission
  -> wasm Keeper.GetContractInfo
  -> nil pointer dereference
```

So the issue was not the shell script JSON itself. The script reached the
correct on-chain path, but the chain-side Wasm keeper available to the inference
permission check was nil/zero in that execution context.

## Commit 148293c0 Changes

### Lazy contract info lookup

File links:

- [`msg_server.go`](../../../inference-chain/x/inference/keeper/msg_server.go#L11-L21)
- [`permissions.go`](../../../inference-chain/x/inference/keeper/permissions.go#L235-L254)

The commit changed `msgServer` so it no longer stores an eagerly initialized
`types.WasmKeeper`. Instead it stores an optional `contractInfoLookup` function.
Normal runtime code can resolve the lookup lazily from the keeper, while tests
can inject a small lookup function directly.

Why it was necessary:

- The Wasm keeper is initialized through app wiring after the inference keeper is
  constructed.
- Capturing the keeper too early can preserve a nil/zero Wasm keeper inside the
  message server.
- The unwrap path depends on `ContractPermission`, so a stale Wasm keeper turns a
  permission check into a process panic instead of a normal authorization error.

### Fail closed instead of panicking

File link:

- [`permissions.go`](../../../inference-chain/x/inference/keeper/permissions.go#L235-L254)

`checkContractPermission` now recovers from Wasm keeper lookup panics and returns
`types.ErrNotSupported`. If the lookup succeeds but the signer is not a Wasm
contract, it returns `types.ErrNotAContractAddress`.

Why it was necessary:

- A permission check should not be able to crash the node.
- If Wasm support is unavailable, bridge withdrawal is not supported in that
  environment.
- If Wasm is available and the signer is not a contract, the request should be
  rejected as a non-contract caller.

### Permission tests

File links:

- [`permissions_test.go`](../../../inference-chain/x/inference/keeper/permissions_test.go#L26-L36)
- [`permissions_export_test.go`](../../../inference-chain/x/inference/keeper/permissions_export_test.go#L21-L27)

The commit added a focused test Wasm keeper and a test helper that can inject
`GetContractInfo` directly into `msgServer`.

Why it was necessary:

- `ContractPermission` must prove three distinct cases: Wasm unsupported,
  signer is not a contract, and signer is a contract.
- The panic case must also be covered because that was the production failure
  mode from the unwrap path.

### Wrapped token artifact refresh

File links:

- [`wrapped_token.wasm`](../../../inference-chain/contracts/wrapped-token/artifacts/wrapped_token.wasm)
- [`checksums.txt`](../../../inference-chain/contracts/wrapped-token/artifacts/checksums.txt)

The commit also updated the compiled wrapped token artifact and checksum.

Why it was necessary:

- The bridge unwrap path is initiated from the wrapped CW20 contract, so the repo
  needs the checked-in Wasm artifact and checksum to match the contract build
  used by deployment scripts.

## Current Uncommitted Chain Changes

### Shared Wasm keeper getter

File links:

- [`keeper.go`](../../../inference-chain/x/inference/keeper/keeper.go#L33)
- [`keeper.go`](../../../inference-chain/x/inference/keeper/keeper.go#L125-L127)
- [`keeper.go`](../../../inference-chain/x/inference/keeper/keeper.go#L613-L629)
- [`app.go`](../../../inference-chain/app/app.go#L298-L300)

The uncommitted change replaces the plain `getWasmKeeper func()` field with a
shared `wasmKeeperGetter` holder. After legacy modules are registered,
`app.New` calls `app.InferenceKeeper.SetWasmKeeperGetter(app.GetWasmKeeper)`.

Why it was necessary:

- `Keeper` values are copied into app modules and message servers during app
  wiring.
- Updating a plain function field after those copies exist does not update the
  copies.
- A shared holder lets all copied keeper values observe the final Wasm keeper
  getter after `app.WasmKeeper` has been initialized.
- This directly addresses the unwrap panic where the permission path reached a
  zero Wasm keeper.

### Earlier recovery in `checkContractPermission`

File link:

- [`permissions.go`](../../../inference-chain/x/inference/keeper/permissions.go#L235-L254)

The recovery `defer` is now installed at the start of
`checkContractPermission`, before resolving the lookup function.

Why it was necessary:

- The panic can happen while resolving or binding `wasmKeeper.GetContractInfo`,
  not only when calling the lookup.
- Installing recovery after lookup setup was too late for the stack observed
  from `bridge-token-unwrap.sh`.

### Liquidity pool Wasm keeper access

File link:

- [`msg_server_register_liquidity_pool.go`](../../../inference-chain/x/inference/keeper/msg_server_register_liquidity_pool.go#L48-L50)

The code now calls `k.GetWasmKeeper()` instead of directly calling the old
function field.

Why it was necessary:

- The old field is now a shared holder, not a callable function.
- This keeps liquidity pool contract instantiation on the same guarded Wasm
  keeper access path as the permission check.

## Current Uncommitted Bridge Script Changes

### Unwrap reproduction script

File link:

- [`bridge-token-unwrap.sh`](bridge-token-unwrap.sh#L105-L111)

The new script exercises the exact Gonka-to-Ethereum unwrap path by executing
the wrapped CW20 contract's `withdraw` message.

Why it was necessary:

- It reproduces the failure path through Wasm contract execution, SDK submessage
  dispatch, `MsgRequestBridgeWithdrawal`, and `ContractPermission`.
- It separates shell/script issues from chain-side permission wiring issues.

### Governance bridge registration script

File link:

- [`bridge-register.sh`](bridge-register.sh#L19-L24)
- [`bridge-register.sh`](bridge-register.sh#L87-L93)
- [`bridge-register.sh`](bridge-register.sh#L170-L225)

Changes include executable mode, cold-key warning, `KEY_DIR` override,
`CHAIN_NAME_ID`, `--chain-name`, `--bridge-only`, and consistent `--home`
keyring usage.

Why it was necessary:

- Governance deposits are held by the validator/operator cold keys, not by an
  arbitrary hot key.
- The bridge address can be registered independently from token metadata/trading
  approval when debugging.
- `--home "$KEY_DIR"` matches how the inference CLI locates keyring state.

### Wrapped token registration script

File link:

- [`bridge-register-wrapped.sh`](bridge-register-wrapped.sh)

Changes include executable mode, cold-key warning, local/key-string support,
binary discovery, `KEY_DIR`, `CHAIN_ID`, `KEY_NAME`, and `NODE_OPTS`.

Why it was necessary:

- Wrapped token code registration needs to work both on Nebius hosts and during
  local/manual test runs.
- Key import and local mode make it easier to reproduce governance and Wasm code
  registration without modifying the remote host keyring permanently.

### Bridge setup bootstrap script

File links:

- [`bridge-setup.sh`](bridge-setup.sh#L7-L9)
- [`bridge-setup.sh`](bridge-setup.sh#L76-L81)
- [`bridge-setup.sh`](bridge-setup.sh#L153-L182)

Changes include fixing the relative Ethereum bridge contract directory,
writing `GONKA_CHAIN_ID=gonka-testnet`, submitting the current epoch group key,
and enabling normal operation after deployment.

Why it was necessary:

- The Ethereum bridge contract needs the Gonka chain ID used by signatures.
- Deployment alone leaves the bridge without the current epoch key and not yet in
  normal operation.
- The Wasm unwrap flow cannot complete end to end unless the Ethereum bridge is
  deployed, initialized with epoch data, and operational.

## Verification Status

Intended focused tests:

```bash
GOCACHE=/Users/gliberman/Documents/GitHub/gonka/.gocache \
go test ./x/inference/keeper -run 'TestPermission_Contract|TestMsgServer_RequestBridgeWithdrawal'
```

Current local blocker:

```text
github.com/bytedance/sonic@v1.14.0/internal/rt/stubs.go: undefined: GoMapIterator
```

The repo pins Go `1.24.2`, while the local machine reported Go `1.26.2`. This
is a toolchain compatibility failure before the keeper tests run, not evidence
that the Wasm permission fix failed.

Run with the pinned Go version:

```bash
$(brew --prefix go@1.24)/bin/go version
GOCACHE=/Users/gliberman/Documents/GitHub/gonka/.gocache \
$(brew --prefix go@1.24)/bin/go test ./x/inference/keeper -run 'TestPermission_Contract|TestMsgServer_RequestBridgeWithdrawal'
```

Broader repo check after the focused tests pass:

```bash
GOCACHE=/Users/gliberman/Documents/GitHub/gonka/.gocache \
$(brew --prefix go@1.24)/bin/go test ./x/inference/keeper ./x/inference/types ./app
```

## Known Follow-Ups

- Re-run the focused keeper tests with Go `1.24.2`.
- Re-run `bridge-token-unwrap.sh` against a node built from the uncommitted
  keeper/app changes. A node still running commit `148293c0` without the shared
  getter update may still have the stale Wasm keeper path.
- If `bridge-setup.sh` is moved or its `cd "$BRIDGE_DIR"` flow changes, re-check
  the cleanup line that removes `.env` after deployment.
