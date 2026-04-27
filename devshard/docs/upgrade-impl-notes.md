# Devshard Upgrade -- Implementation Notes

Scope: the first versioned release only.

This document is about the temporary implementation we actually ship now. The
long-term architecture stays in `devshard/docs/upgrade.md`.

## Current implementation contract

The first versioned release is intentionally small:

- `/v1/devshard/*` remains the legacy path served directly by dapi
- `/devshard/<version>/*` is the new path served through
  `proxy -> versiond -> devshardd`
- `devshardd` is a temporary standalone host binary built out of the
  `decentralized-api/` module
- `devshardctl` defaults to its release version path and can still override the
  route prefix for tests or local debugging

The main goal of this release is to prove that the standalone host process can
run behind versiond while the legacy dapi path continues to work unchanged.

Version isolation is strict:

- `/devshard/<version>/*` hosts must talk to other `/devshard/<version>/*`
  hosts
- `/v1/devshard/*` hosts must talk only to `/v1/devshard/*` hosts
- the temporary release should not add cross-prefix fallback between those two
  families

## What is implemented now

### Proxy and routing

The proxy exposes two parallel routes:

```text
/v1/devshard/*        -> dapi legacy HostManager
/devshard/<version>/* -> versiond-managed devshardd
```

Location ordering matters. `/v1/devshard/*` must continue to hit dapi
directly. `/devshard/*` is reserved for versiond-routed standalone binaries.

### Temporary standalone binary

The standalone host binary lives at `decentralized-api/cmd/devshardd/`.

It is a thin bootstrap around shared devshard runtime code:

- query-only chain access
- devshard signer loaded from the existing keyring
- mainnet bridge backed by chain queries
- NodeManager gRPC client for ML node acquisition
- shared devshard host/session HTTP runtime
- sqlite session storage under versiond's data dir

Dropped from dapi's `main()`:

- admin server
- model manager
- PoC worker
- event dispatcher
- block queue
- config sync
- NodeManager gRPC server
- NATS / tx pipeline

Build:

```bash
go build -ldflags "-X main.Version=$VERSION" \
  -o build/devshardd ./cmd/devshardd
```

The binary can already carry a release version at build time. The current build
wiring passes `DEVSHARD_VERSION` into `main.Version`, so a local or release build can
stamp `devshardd` and `devshardctl` with the same version string.

That same route/binary token is now bound into session state:

- `v1` for the legacy `/v1/devshard/*` path
- `<version>` for `/devshard/<version>/*`

Settlement sends the cleartext version to mainnet and also includes it in the
signed state root as `version_hash = sha256(version_utf8)`.

What is not implemented yet is container-default activation. Today versiond
only runs a local `devshardd` binary when the operator explicitly sets both
`VERSIOND_FORCE=<name>` and `VERSIOND_OVERRIDE_<name>=/path/to/devshardd`.

### Join deployment

`deploy/join/docker-compose.yml` is the single join entrypoint.

The join proxy enables the versioned route by setting
`VERSIOND_SERVICE_NAME=versiond`, so `/devshard/<version>/*` goes through
`proxy -> versiond -> devshardd` in the same stack.

The versiond service passes the child env that `devshardd` already expects:
`VERSIOND_BINARY_NAME=devshardd`, `NODE_MANAGER_ADDR=api:9400`,
`NODE_HOST=node`, `KEY_NAME`, `ACCOUNT_PUBKEY`, and `KEYRING_*`.

Warm-key access stays on the existing file keyring. The join stack mounts
`.inference` into versiond at `/root/.inference:ro`, so the devshardd child
can sign with the same join identity without giving versiond write access to
key material.

Versiond-managed runtime state is persisted on the host under `./devshards`:

- `./devshards/bin -> /opt/versiond/bin`
- `./devshards/data -> /opt/versiond/data`

### Test shape

Both flows are covered on purpose:

- `DevshardTests.kt` verifies the legacy `/v1/devshard` path
- `DevshardStandaloneTests.kt` verifies the standalone
  `/devshard/<version>` path through proxy and versiond in two different modes

The override-driven tests use `VERSIOND_FORCE=<version>` together with
`VERSIOND_OVERRIDE_<version>` to run the locally built binary and exercise full
devshard session flows through versiond.

The state-driven startup test does not set `VERSIOND_FORCE` and does not set
any `VERSIOND_OVERRIDE_*` for the tested version. Instead it:

- prepares a deterministic `devshardd.zip` plus `devshardd.zip.sha256` on the
  host before the cluster boots
- seeds that exact `(name, binary URL, sha256)` tuple into
  `devshard_escrow_params.approved_versions` at genesis
- waits for dapi `/versions` to expose that state
- verifies that versiond downloads the archive and records `install.json`

The artifact server stays local to the test environment. Its job is only to
serve already-prepared files over HTTP. It no longer builds the zip at
container startup, because the startup-seeded test needs the final archive
sha256 before `initCluster()` runs.

### Bundled binary as the container default

If we want a versiond image that already contains one pre-built `devshardd`,
the simplest temporary rule is:

- keep the operator contract exactly as it is today:
  `VERSIOND_FORCE` and `VERSIOND_OVERRIDE_<name>`
- let the image optionally carry one bundled binary plus fixed metadata such as
  `VERSIOND_BUNDLED_VERSION` and `VERSIOND_BUNDLED_BINARY`
- set `VERSIOND_BINARY_NAME=devshardd` in that bundled image so versiond looks
  for the correct executable name by default
- during versiond config load, treat that bundled binary as the default
  override for that version only when the operator did not already provide
  explicit env vars
- if the operator sets `VERSIOND_FORCE` explicitly, that replaces the container
  default
- if the operator sets `VERSIOND_OVERRIDE_<bundled>` explicitly, that replaces
  the bundled binary path

That makes the bundled case behave like an automatic
`VERSIOND_FORCE=<bundled>` plus
`VERSIOND_OVERRIDE_<bundled>=<bundled path>`, without introducing a second
runtime model.

### Recommended release shapes

To keep releases simple, support two image shapes from the same code path:

- plain `versiond`: current behavior, no bundled `devshardd`
- bundled `versiond`: same image plus one pre-built `devshardd` for its default
  release version, with `VERSIOND_BINARY_NAME=devshardd` as the image default

The repo already knows how to build a version-stamped `devshardd`. The missing
piece is only how to copy that binary into the versiond image and expose it as
the default override.

One practical note: the current `versioned/Dockerfile` builds from the
`versioned/` directory only, so it cannot include `decentralized-api/` build
artifacts today. The clean temporary fix is to build the bundled image from the
repo root, reuse the existing `decentralized-api` builder flow, and pass
`DEVSHARD_VERSION=<name>` once.

## Explicit non-goals for this release

The following items are not part of the temporary implementation:

- chain-side `approved_versions` enforcement
- settlement rejection based on the binary version
- operator workflow for governance-driven version deprecation
- moving the standalone binary fully into the `devshard/` module
- replacing sqlite with postgres
- session pruning / retention background jobs

Those may still make sense later, but they should not shape the temporary code
path now.

The bound-version work is intentionally broader than a docs-only change. It
touches devshard state hashing, session persistence and recovery, settlement
JSON, and chain-side verification.

## Code ownership

The temporary release should still move reusable code toward `devshard/`.

Current direction:

- keep dapi-only bootstrap and deployment wiring inside `decentralized-api/`
- move reusable devshard HTTP/session runtime into `devshard/`
- keep both legacy dapi and standalone devshardd using the same shared
  runtime underneath

## Known follow-up items

- Rate limiting on transport GET endpoints is still worth fixing for both
  paths.
- sqlite is acceptable for the temporary release but not the final host
  deployment story.
- once the standalone runtime settles, the remaining bootstrap code can move
  from `decentralized-api/cmd/devshardd/` into the `devshard/` module.
