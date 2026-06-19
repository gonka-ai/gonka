# Devshard Upgrades

This document is the target architecture. It describes where the system should
end up, not only what is implemented in the first temporary release.

The temporary implementation is tracked separately in
`devshard/docs/upgrade-impl-notes.md`.

## Goal

Devshard binaries version independently of mainnet. Changing the devshard
runtime should not require cosmovisor or a coordinated full-node upgrade.

The stable client contract is path-based:

```
/v1/devshard/*        -> legacy path, served directly by dapi
/devshard/<version>/* -> versioned path, served by versiond-managed binaries
```

The legacy path stays available for backward compatibility while the versioned
path becomes the normal way to run newer devshard releases.

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
/v1/devshard/*        -> dapi, in-process
/devshard/<version>/* -> versiond -> devshard binary for <version>
```

The target safety model is that the first request binds the session to one
binary version off-chain. Every later diff must continue with that same
version. A host running the wrong binary refuses to sign, so a version-mixing
session cannot gather the threshold needed to settle.

The bound version is recorded in shard state. Use `v1` for the legacy path and
`<version>` for `/devshard/<version>/*`.

## Deprecation

In the target design, governance removes a version from `approved_versions`.

Settlement is still user-driven. The user is the party with the strongest
incentive to recover unused escrow, so in-flight sessions should be settled by
the user during the voting window before a deprecated version is finally
disabled.

Because escrow creation carries no version, deprecation enforcement can only
happen later in the flow. The intended enforcement point is settlement, not
escrow creation.

Settlement carries a cleartext **state root and protocol version** tag
(`state_root_and_protocol_version`) and that same value is part of the signed
state commitment. Mainnet recomputes the root with
`version_hash = sha256(tag_utf8)`. That tag is independent of versiond runtime
names in `approved_versions`; bump it only when state-root or settlement protocol
changes — see [protocol-version.md](./protocol-version.md).

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
