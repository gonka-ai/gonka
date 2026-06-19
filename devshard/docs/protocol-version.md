# State root and protocol version

Devshard uses **two independent version concepts**. Conflating them breaks recovery, host binding, migration, and operator mental models.

## Runtime version (versiond)

Governance lists **which devshard binaries may run** in `DevshardEscrowParams.approved_versions` (`name`, `binary` URL, `sha256`). versiond polls dapi `GET /versions`, downloads matching zips, and routes HTTP to `/devshard/<name>/…`.

At session bind, storage records this as `CreateSessionParams.Version` / `SessionMeta.Version` / host **`boundVersion`**. It identifies the running process build so only hosts on the **same** versiond runtime name participate in a session (storage returns `ErrSessionVersionConflict` on mismatch).

| Surface | Field / API | Value |
|---------|-------------|--------|
| Embedded dapi | `HostManager` ctor, `main.go` | `"v1"` → `types.LegacyRouteSessionVersion` |
| devshardd | `NewHostManager(..., runtimeVersion, ...)` | versiond child name (e.g. from `DEVSHARD_BINARY_VERSION`) |
| User / devshardctl | `VersionForRoutePrefix(routePrefix)` | `LegacyRouteSessionVersion` for `/v1/devshard`; else `/devshard/<name>` segment |
| Storage | `sessions.version` | Same bind tag as above |

This tag is **not** hashed into the state root and is **not** sent as `state_root_and_protocol_version` on settlement.

See [upgrade.md](./upgrade.md) and [params-dataflow.md](./params-dataflow.md).

## State root and protocol version

**Protocol version** is the tag in:

- `EscrowState.StateRootAndProtocolVersion` (state machine; set via `WithStateRootAndProtocolVersion` / `state.WithVersion`)
- `MsgSettleDevshardEscrow.state_root_and_protocol_version` (on-chain settlement)
- Settlement JSON from devshardctl / hosts

It is hashed into every state root:

```text
version_hash = sha256(state_root_and_protocol_version_utf8)
state_root     = sha256(host_stats_hash || fees_be || rest_hash || version_hash || phase_byte)
```

All hosts in a session must use the **same** protocol tag for a given escrow lifetime or signatures and settlement quorum will not align.

| Surface | How it is set |
|---------|----------------|
| Default in source | `types.DevshardStateRootAndProtocolVersion` in `domain.go` (currently `"v2"`) |
| Release binaries | Link-time `-X devshard/types.buildStateRootProtocolVersion=…` from `DEVSHARD_PROTOCOL_VERSION` at build; resolved once at init into `types.EffectiveStateRootAndProtocolVersion` |
| Testermint | Reads `build/devshard-protocol-version` written by `make devshardd-build` (same value as link flags) |

Implementation: `devshard/types/protocol_version.go`, `devshard/state/hash.go`, `devshard/state/settlement.go`, chain keeper `VerifyDevshardSettlement`.

## When to bump `DevshardStateRootAndProtocolVersion`

Change the constant in `domain.go` **and** the default `DEVSHARD_PROTOCOL_VERSION` / link flags when any of the following change incompatibly:

| Change type | Examples |
|-------------|----------|
| State-root composition | Preimage fields, `rest_hash` contents, sealed accumulator rules, inference record hashing, phase handling |
| Settlement protocol | Cleartext settlement fields, what hosts sign, keeper verification steps |

Do **not** bump this tag for ordinary release builds that only fix bugs without changing roots or settlement. Do **not** assume it must equal an `approved_versions.name` entry.

New sessions stamp the protocol tag at state-machine creation (`WithVersion(types.EffectiveStateRootAndProtocolVersion)`). Existing sessions keep the tag they were created with until settled.

## Operator checklist

1. Implement the protocol change in `devshard/state` (and chain keeper if settlement rules change).
2. Increment `DevshardStateRootAndProtocolVersion` in `domain.go`.
3. Set `DEVSHARD_PROTOCOL_VERSION` (Makefile / Docker build) to the same string.
4. Update hash/settlement/migration tests that hardcode the tag.
5. Document the upgrade path for in-flight escrows (settle under the old tag before removing old behavior, if applicable).

## Build stamps

| File | Written by | Consumed by |
|------|------------|-------------|
| `build/devshard-version` | `make devshardd-build` | Testermint `devshardTestVersion()` / `VERSIOND_FORCE` |
| `build/devshard-protocol-version` | `make devshardd-build` | Testermint `devshardStateRootProtocolVersion()`; must match binary link flags |
