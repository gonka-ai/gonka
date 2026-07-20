# Devshard Upgrades

This document is the target architecture. It describes where the system should
end up, not only what is implemented in the first temporary release.

The temporary implementation is tracked separately in
`devshard/docs/upgrade-impl-notes.md`.

## Goal

Devshard binaries version independently of mainnet. Changing the devshard
runtime should not require cosmovisor or a coordinated full-node upgrade.

The active client contract is path-based:

```
/devshard/<version>/* -> versioned path, served by versiond-managed binaries
```

Clients must choose a versioned route.

The legacy `/v1/devshard/*` path is deprecated and returns `410 Gone`.

## Target flow

The intended steady-state flow is:

```
governance proposal -> params.approved_versions -> dapi GET /versions -> versiond polls, downloads, runs
```

The first temporary release now implements the `approved_versions -> /versions
-> versiond download` path. The remaining WARN blocks below call out the parts
that are still future work beyond that first release.

`DevshardEscrowParams.approved_versions` is the governance-controlled list of
allowed binaries. Each entry carries:

- version name
- download URL
- sha256

sha256 is the real identity. The URL is only a download hint. If two proposals
point at different mirrors but the same hash, operators do not restart
anything. If the name stays the same but the hash changes, versiond downloads
the new binary first and then swaps over.

Versiond re-hashes cached binaries on startup so a tampered file on disk is
detected before any traffic is routed to it.

## Multiple versions per host

In the target design, every host runs every approved version concurrently. If
`approved_versions = [v1, v2, v3]`, a host runs three child processes side by
side under versiond and exposes them under three different URL prefixes.

Hosts do not pick subsets. Governance defines the active set globally.

WARN: concurrent multi-version hosting is target behavior. The temporary
release only needs the standalone path to work for the version currently being
tested or forced locally.

## Version selection and binding

Escrow creation stays version-agnostic. `MsgCreateDevshardEscrow` does not take
a version.

The user chooses a version by selecting the HTTP path at session start:

```
/devshard/<version>/* -> versiond -> devshard binary for <version>
```

The target safety model is that the first request binds the session to one
binary version off-chain. Every later diff must continue with that same
version. A host running the wrong binary refuses to sign, so a version-mixing
session cannot gather the threshold needed to settle.

The bound version is recorded in shard state. Use the `<version>` segment from
`/devshard/<version>/*`.

## Deprecation

In the target design, governance removes a version from `approved_versions`.

Settlement is still user-driven. The user is the party with the strongest
incentive to recover unused escrow, so in-flight sessions should be settled by
the user during the voting window before a deprecated version is finally
disabled.

Because escrow creation carries no version, deprecation enforcement can only
happen later in the flow. The intended enforcement point is settlement, not
escrow creation.

Settlement carries a cleartext **protocol version** tag
(`state_root_and_protocol_version`) and that same value is part of the signed
state commitment. Mainnet recomputes the root with
`version_hash = sha256(tag_utf8)`. The tag equals the session bind version:
`approved_versions.name` for `/devshard/<name>/*`, or `v1` for the legacy
`/v1/devshard/*` path. See [Version naming](#version-naming) below.

## Version naming

Two strings, same governance slot:

| Surface | Example | Role |
|---------|---------|------|
| Protocol name (`approved_versions.name`) | `v2` | Routing `/devshard/v2/`, session bind, state-root / settlement tag |
| Binary build id | `0.2.13-v2-r2` | Log prefix only; can change when governance keeps the same protocol name |

| Build / runtime | Mechanism |
|-----------------|-----------|
| `DEVSHARD_VERSION` | Makefile / `-X main.Version=...` — protocol name at link time |
| `DEVSHARD_BINARY_VERSION` | Makefile / `-X main.BinaryVersion=...` — build id at link time |
| versiond slot | `c.version.Name` = protocol (`v2`) |
| versiond → child | `DEVSHARD_BINARY_LOG_VERSION=<build id>` from `devshardd --print-binary-version` |
| Session / settlement | protocol name only (`RuntimeVersion` = link-time `main.Version`) |

Build example:

```bash
make devshardd-build DEVSHARD_VERSION=v2 DEVSHARD_BINARY_VERSION=0.2.13-v2-r2
```

versiond verifies `--print-protocol-version` matches the slot name before start.
When that flag is absent, versiond trusts the governance slot name and skips
the embed check. When `--print-binary-version` is absent, versiond sets
`DEVSHARD_BINARY_LOG_VERSION` to the slot name (legacy path). devshardd accepts
that value when it matches the link-time protocol name.

**Legacy path:** `/v1/devshard/*` uses `v1` as the protocol tag (embedded dapi
and historical sessions).

**Graceful binary refresh:** When governance keeps the same `approved_versions`
name but updates `binary` URL / `sha256`, versiond downloads the new artifact
and restarts the child. New sessions pick up the refreshed binary under the
same name. In-flight sessions keep the tag they were bound with until settled;
hosts refuse mixed versions via storage `ErrSessionVersionConflict`.

**Protocol-breaking changes** require a **new** approved name (new state-root /
settlement rules). Do not reuse an existing name for incompatible wire or hash
layout changes.

Build stamp: `make devshardd-build` writes `build/devshard-version` (used by
Testermint `VERSIOND_FORCE` and settlement assertions).

## Operator overrides

Operators need an escape hatch for hotfixes and local testing:

- `VERSIOND_OVERRIDE_<name>=/path/to/binary` replaces the downloaded binary for
  `<name>` with a local file. versiond still checks sha256 and still restarts
  on changes.
- `VERSIOND_FORCE=<name>` runs a version that is not in
  `approved_versions`. This is for local validation and release-candidate
  testing, not for the steady-state governance flow.

## What versiond manages

Only the devshard binary. dapi is not managed by versiond.

`devshardctl` is a client-side CLI shipped alongside each release for protocol
compatibility. versiond does not manage it.

## Temporary first release

The first release does not implement the full target state. In particular, the
following items are architectural intent, not current behavior:

- chain-side enforcement that only approved versions can settle
- a self-contained devshard host binary built entirely from the `devshard/`
  module

The first release instead uses a temporary standalone binary built out of
`decentralized-api/` and served through versiond. That temporary shape is an
implementation shortcut, not the intended long-term architecture.

Current join deployment keeps that temporary path in one compose file:
`deploy/join/docker-compose.yml`. The versiond service sits behind proxy,
mounts the existing `.inference` keyring read-only for signing, and persists
its runtime state under `./devshards`. This is deployment wiring for the first
release, not a change to the target architecture.
