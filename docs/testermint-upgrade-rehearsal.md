# Testermint Upgrade Rehearsal

This workflow tests an actual local software upgrade before we spend Testnet time on it. It starts a Testermint cluster from the previous production release, creates meaningful pre-upgrade state, then upgrades the still-running cluster with binaries built from the candidate branch.

The rehearsal is intentionally separate from the normal integration matrix. It needs two repository checkouts, two versions of the Testermint harness, and live Docker state that must survive between phases.

## What It Proves

- The previous release can still boot in the current CI environment using the production images declared by that release.
- The previous release's own Testermint harness can create realistic pre-upgrade state.
- The candidate branch can submit a real Cosmos SDK software-upgrade proposal against that existing cluster.
- Cosmovisor downloads and installs the candidate `inferenced` and `decentralized-api` upgrade archives.
- The chain resumes block production after the upgrade and records the expected `last-upgrade-height`.
- Normal inference and devshard settlement still work after the upgrade.

## Version And Image Discovery

The workflow treats deploy scripts as the canonical source for production image references.

1. The target upgrade defaults to the newest semantic `UpgradeName` found under `inference-chain/app/upgrades/*/constants.go` in the candidate checkout.
2. The previous release defaults to the highest canonical release tag below that target, where canonical means `release/vX.Y.Z`.
3. Suffix tags such as `release/v0.2.13-testnet-3`, `release/v0.2.13-testnet-rehearsal-1`, or ad hoc tags are ignored.
4. The old checkout is made at that previous release tag.
5. Previous production image refs are read from the old checkout's `deploy/join/docker-compose.yml`.
6. Those production images are pulled and retagged to the local Testermint image names, such as `ghcr.io/product-science/inferenced`, `ghcr.io/product-science/api`, and `ghcr.io/product-science/proxy:latest`.

Manual workflow inputs can override the target upgrade or previous release when debugging a non-standard branch.

## Phase 1: Prepare Old State

The old release checkout runs the old release's Testermint harness against the previous production images.

Because old release tags are immutable, the candidate branch stores a small patch at:

`testermint/upgrade-rehearsal/previous-release-prep.patch`

The workflow applies that patch to the old checkout before running the prep test. The patched test:

- boots the old cluster from scratch;
- waits through an epoch;
- runs a normal inference;
- creates and settles a devshard escrow;
- waits into another epoch;
- writes an upgrade rehearsal manifest with heights, epoch indexes, participant ids, inference id, and devshard escrow id.

The workflow must not tear the cluster down after this phase. The live Docker containers and volumes are the upgrade subject.

## Phase 2: Build Candidate Upgrade Archives

The candidate checkout builds the upgrade archives with:

`make build-for-upgrade`

The generated archives live under:

- `public-html/v2/inferenced/inferenced-amd64.zip`
- `public-html/v2/dapi/decentralized-api-amd64.zip`

The workflow starts a small HTTP container on the `chain-public` Docker network and serves `public-html` to the old cluster. The current Testermint attach test computes SHA-256 checksums locally and includes them in the upgrade URLs.

Do not use `make build-for-upgrade-tests` for this rehearsal. That target builds `inferenced` with the `upgraded` build tag, which swaps in the synthetic `v0.0.1test` handler and omits release handlers such as `v0.2.14`. This workflow is meant to exercise production-style release archives.

## Phase 3: Complete The Upgrade

The current checkout runs an attach-only Testermint test. This is the most important safety rule: phase 2 must discover the existing cluster and must not call the normal cluster setup path that can rebuild Docker state.

The attach test:

- reads the phase 1 manifest and asserts that meaningful old state exists;
- attaches to `genesis`, `join1`, and `join2` containers by local Testermint image/name conventions;
- submits a software-upgrade proposal whose title matches the target `UpgradeName`;
- deposits and votes;
- waits for the upgrade height and cosmovisor restart;
- verifies `last-upgrade-height` on every node after the candidate binary is running;
- verifies basic API/node health;
- runs a post-upgrade normal inference;
- enables devshard request handling with a governance params proposal if the upgraded chain reports it disabled;
- creates and settles a post-upgrade devshard escrow.

## Running In GitHub Actions

Use the dedicated workflow:

`testermint-upgrade-rehearsal.yml`

Typical manual run:

```bash
gh workflow run testermint-upgrade-rehearsal.yml --ref <candidate-ref>
```

Useful debug overrides:

```bash
gh workflow run testermint-upgrade-rehearsal.yml \
  --ref <candidate-ref> \
  -f target_upgrade=v0.2.14 \
  -f previous_release=release/v0.2.13
```

The workflow uploads:

- old and new Testermint logs;
- old and new JUnit XML;
- the prep and completion manifests;
- a Docker state snapshot from the runner.

The first live `v0.2.13` to `v0.2.14` rehearsal prep run completed in about 19 minutes and produced old-chain state across heights 76 to 198 and epochs 2 to 5. Treat a quiet prep step as normal while it is still within its workflow timeout.

## Common Failures

If previous image resolution fails, inspect the old tag's `deploy/join/docker-compose.yml`. The parser expects a service-level `image:` entry for at least `node` and `api`.

If phase 1 fails before booting, confirm the old production images are still pullable from GHCR and that the retagged image names match local Testermint compose files.

If the candidate archive build fails with `Cache export is not supported for the docker driver`, keep `USE_REGISTRY_CACHE=0` for this workflow or add an explicit Docker Buildx container-driver setup plus package write permissions. The rehearsal does not need to publish registry build caches.

If the completion test fails on `last-upgrade-height` before submitting the proposal, make sure it is not querying candidate-only CLI commands against the previous release binary. Pre-upgrade assertions should rely on the prep manifest and old-chain health; `last-upgrade-height` is only meaningful after the candidate upgrade has applied.

If phase 2 rebuilds the cluster, that is a bug. The current rehearsal test should only call attach/discovery helpers and should never call `initCluster`, `setupLocalCluster`, `launch.sh`, or `stop-rebuild.sh`.

If the proposal passes but the chain never resumes, inspect node logs around the upgrade height. The most useful signals are cosmovisor download errors, checksum mismatches, missing upgrade handler names, or archive layout issues.

If the node downloads the candidate binary and restarts, but the new binary still halts with `UPGRADE "<target>" NEEDED`, first confirm the candidate archive was built with `make build-for-upgrade` and not the test-only target. A test-tagged binary will not register normal release upgrade handlers.

If post-upgrade inference fails with versioned endpoint routing, check that the test stubs mock-server responses for the target upgrade segment as well as the default segment.

If post-upgrade devshard settlement fails with `devshard completion and timeout requests are disabled`, query inference params and confirm `devshard_escrow_params.devshard_requests_enabled` is true. The rehearsal completion test enables this through a normal governance params proposal because older state or operational settings can carry a disabled value across the upgrade. When building that params proposal, preserve the existing `devshard_escrow_params.max_nonce` value, or repair a missing/zero value to the chain default, because a full `MsgUpdateParams` proposal with `max_nonce: 0` is rejected during proposal validation.
