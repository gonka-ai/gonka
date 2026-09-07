# Gonka Wasm gRPC allowlist probe

This directory contains an unaudited, test-only CosmWasm contract used to
regression-test Gonka's contract-facing gRPC allowlist. A real Wasm fixture is
needed in addition to `app/legacy_test.go`: the map-level test verifies paths and
Go response constructors, while this contract proves that Wasm bytecode can
encode requests, cross `AcceptListGrpcQuerier`, and decode native protobuf
responses.

The probe exposes typed queries for exactly these production routes:

```text
/inference.inference.Query/GetCurrentEpoch
/inference.inference.Query/ListClaimRecipients
/inference.inference.Query/EpochPerformanceSummaryByParticipant
/inference.streamvesting.Query/TotalVestingAmount
```

`RawGrpc` is an intentionally adversarial test interface used only to verify
that broad or unknown paths are denied and malformed payloads on allowed paths
fail during decoding. It is not a production contract API.

## Layout and consumers

```text
src/                         Rust source and minimal protobuf wire mirror
build.sh                     canonical source-to-Wasm builder
artifacts/p0_probe.wasm      committed optimized fixture
artifacts/checksums.txt      SHA-256 integrity manifest
```

The fixture is consumed by
`app/wasm_grpc_query_allowlist_test.go`, an in-process full-app test using the
real Gonka Wasm VM, query router, keepers, and allowlist wrapper. CI rebuilds the
fixture from a clean checkout and requires the committed bytes and manifest to
match.

## Prerequisites and canonical build

Run these commands from `inference-chain/contracts/p0-probe/`. Docker is the
canonical engine used by CI:

```bash
make build CONTAINER_ENGINE=docker
make check CONTAINER_ENGINE=docker
```

Podman may be supplied explicitly for local experimentation, but committed
fixture bytes are defined by the Docker CI build. The builder is pinned to
`cosmwasm/optimizer:0.17.0` by OCI digest and to `linux/amd64`; the fixed platform
reduces architecture variance. `Cargo.lock` is committed and all Cargo commands
use locked dependency resolution.

`checksums.txt` has deterministic relative paths and covers the Cargo metadata,
build recipe, README, every Rust source file, and the Wasm artifact. It is an
integrity and exact-file-set check. It does not by itself prove that the binary
was derived from the source; the clean CI rebuild and byte comparison provide
that guarantee.

## Verification layers

Format and test the Rust source with the pinned Rust 1.86 toolchain:

```bash
cargo +1.86.0 fmt --check
cargo +1.86.0 test --locked
```

Verify the manifest and run locked unit tests in the pinned container:

```bash
make check CONTAINER_ENGINE=docker
```

Run the contract-facing Gonka regression from `inference-chain/`:

```bash
go test ./app -run '^TestWasmGrpcForwardMarketplaceQueryAllowlist$' -count=1
```

## Updating the fixture

1. Change Rust source or dependencies and update `Cargo.lock` with Rust 1.86.
2. Run `cargo +1.86.0 fmt --check` and `cargo +1.86.0 test --locked`.
3. Run `make build CONTAINER_ENGINE=docker`.
4. Run `make check CONTAINER_ENGINE=docker`.
5. Run the focused Go/Wasm regression shown above.
6. Rebuild again and require no diff in `p0_probe.wasm` or `checksums.txt`.
7. Review and commit source, lockfile, build files, Wasm, and manifest together.

Changing Rust source without rebuilding leaves tests executing stale bytecode;
the exact-set manifest check and clean CI rebuild are both mandatory defenses.

## Safety

Never deploy this artifact to a public or production network. The probe is not
audited, its raw-query surface exists specifically for adversarial tests, and it
has no marketplace authorization or business logic. Production packaging and
deployment configuration must not reference `artifacts/p0_probe.wasm`.
