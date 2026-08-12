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
anything. Consensus permanently binds an approved name to that hash. A proposal
for different bytes must append a new version name; changing the hash in place
is rejected. This lets routers use the version name as an immutable data-plane
identity even while hosts observe governance at different times. See
[rolling-update.md](./rolling-update.md) for the child lifecycle mechanics.

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

The **escrow creator** chooses a version by calling a versioned protocol path
with a valid owner signature. Bind is the first successful owner
`POST /devshard/<version>/sessions/{id}/chat/completions` (see
[versionless-observability-plan.md](./versionless-observability-plan.md)).

```
/devshard/<version>/sessions/.../chat/completions  → versiond → bind + protocol
/devshard/sessions/.../diffs|mempool|signatures      → versionless observability (no bind)
```

Legacy versioned observability URLs are rewritten internally by the join proxy
to the versionless forms; the version segment in those URLs does not bind.

The target safety model is that the first **owner** request binds the session to
one **protocol** version off-chain. Every later diff must continue with that same
protocol name. A host running the wrong protocol binary refuses to sign, so a
version-mixing session cannot gather the threshold needed to settle.

The bound version is recorded in shard state / storage (`sessions.version`).
Use the `<version>` segment from `/devshard/<version>/*` for protocol traffic only.

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
| Protocol name (`approved_versions.name`) | `v4` | Routing `/devshard/v4/`, session bind, state-root / settlement tag |
| Binary build id | `0.2.14-v4-r2` | Link-time build stamp: logs, `--print-binary-version`, versiond `/healthz`, and `GET …/stats/shards` `binary_version` |

Protocol name is what binds sessions and settlements. Binary build id identifies
the exact running build in logs and diagnostics. Production governance does not
approve two SHA values under one protocol name; a new artifact receives a new
name. The distinct build id remains useful for detecting packaging mistakes,
local overrides, and mixed pre-contract deployments.

| Build / runtime | Mechanism |
|-----------------|-----------|
| `DEVSHARD_VERSION` | Makefile / `-X main.Version=...` — protocol name at link time |
| `DEVSHARD_BINARY_VERSION` | Makefile / `-X main.BinaryVersion=...` — build id at link time |
| versiond slot | `c.version.Name` = protocol (`v4`) |
| versiond → child | `DEVSHARD_BINARY_LOG_VERSION=<build id>` from `devshardd --print-binary-version` |
| Session / settlement | protocol name only (`RuntimeVersion` = link-time `main.Version`) |
| Stats / health | `protocol_version` + `binary_version` on `GET …/stats/shards` (and detail); `binary_version` also on versiond `/healthz` |

Build example (same protocol, two roll stamps):

```bash
make devshardd-build DEVSHARD_VERSION=v4 DEVSHARD_BINARY_VERSION=0.2.14-v4
make devshardd-build DEVSHARD_VERSION=v4 DEVSHARD_BINARY_VERSION=0.2.14-v4-r2
```

versiond verifies `--print-protocol-version` matches the slot name before start.
When that flag is absent, versiond trusts the governance slot name and skips
the embed check. When `--print-binary-version` is absent, versiond sets
`DEVSHARD_BINARY_LOG_VERSION` to the slot name (legacy path). devshardd accepts
that value when it matches the link-time protocol name.

**Legacy path:** `/v1/devshard/*` uses `v1` as the protocol tag (embedded dapi
and historical sessions).

**Same-name binary refresh (rolling update):** When governance keeps the same
`approved_versions.name` but updates `binary` URL / `sha256`, versiond
downloads the new artifact under `bin/<name>/<sha>/` and rolls the child:

1. Probe `--print-storage-mode` on old + new; overlap only if **both** are
   exactly `postgres`.
2. **Postgres:** start the new child on a new port → wait admin `/ready` +
   public `/healthz` → publish the new route → drain the old generation
   (proxy leases, then `POST /drain`) → SIGTERM old.
3. **Otherwise (SQLite / hybrid / probe fail):** exclusive stop/start — no
   concurrent children.

New requests land on the new SHA after route publish. In-flight work on the
old child is allowed to finish (long SSE / validation). Sessions stay bound to
the **protocol** name they used; hosts refuse mixed protocol versions via
storage `ErrSessionVersionConflict`. Stamp each roll with a distinct
`DEVSHARD_BINARY_VERSION` so operators and clients can tell which generation
is answering (`binary_version` on stats / `/healthz`).

Full operator walkthrough: [rolling-update.md](./rolling-update.md) and
[v4-deploy-test-plan.md](./v4-deploy-test-plan.md) §7.

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
