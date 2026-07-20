# Testermint infrastructure changes (devshard / versiond / Step 7)

This document describes **supporting changes in Testermint and local Docker build tooling** added so Step 7 host-path e2e (`DevsharddRuntimeConfigTests`) and the existing versiond stack can run reliably. It complements [`params-refactoring-implementation.md`](./params-refactoring-implementation.md) (Steps 3b-T and 7) and [`params.md`](./params.md).

**Motivation:** Step 7 proves that **devshardd** (via **versiond**) applies dapi **NodeManager** `GetRuntimeConfig` long-poll in real HTTP paths (governance 503/200, epoch, dapi restart). That requires a cluster with `docker-compose.versiond.yml`, a host-built `devshardd` binary, and a healthy **genesis-api** on developer machines—including **Apple Silicon**.

---

## What we did *not* change

| Area | Policy |
|------|--------|
| Default genesis bootstrap | Tests **without** the versiond compose overlay still use the original single `docker compose up -d` path in `DockerGroup.init()`. |
| Global two-phase genesis | The chain-node-first boot is gated on `usesVersiondOverlay()` only. |
| Other Testermint suites | No change to join-node registration, mock-server, or proxy behaviour unless the pair uses the versiond overlay. |

---

## Test split (Step 3b-T vs Step 7)

| Class | Cluster | What it exercises |
|-------|---------|-------------------|
| `RuntimeConfigTests.kt` | Base compose only | **dapi** NodeManager gRPC from the **JVM** (long-poll matrix, governance field on snapshot). No versiond/devshardd. |
| `DevsharddRuntimeConfigTests.kt` | Base + `docker-compose.versiond.yml` | **Host path:** versiond → devshardd → `NODE_MANAGER_ADDR` long-poll; HTTP completions via proxy (503 when `devshard_requests_enabled=false`). |
| `VersiondTests.kt` | Same versiond overlay | versiond download/install/proxy (testapp binary by default). |

See [3b-T vs Step 7 duplication](./params-refactoring-implementation.md#3b-t-vs-step-7--duplication) in the implementation plan—Step 7 does **not** repeat the full long-poll RPC matrix.

---

## File-by-file changes

### `testermint/src/main/kotlin/DockerGroup.kt`

**Versiond-only genesis boot** (`isGenesis && usesVersiondOverlay()`):

1. `compose up -d chain-node` — genesis key exists before api `init-docker.sh` runs.
2. `waitForColdKeyInNodeContainer()` — poll `inferenced keys show` in `genesis-node` (up to 3 minutes).
3. `extractColdPubkeyFromNodeContainer()` — set `coldAccountPubkey` for compose env.
4. `compose up -d` — full stack (api, versiond, postgres, mock-server, …).
5. `ensureGenesisApiRunning()` — if api exited during init (e.g. missing key), `--force-recreate api` with `ACCOUNT_PUBKEY` / `KEYRING_BACKEND` / `CREATE_KEY=false`.

**Why:** With versiond overlay, api and chain-node start together; api init historically raced genesis key creation (`Key 'genesis' not found`) or left a dead container while the test JVM saw `InvalidClusterException: APIs (0)`.

**Deferred pair discovery:** For versiond genesis, `getLocalInferencePairs()` is **not** called at the end of `init()`; `initializeCluster()` discovers pairs after RPC readiness (same as avoiding a half-dead api during `@BeforeAll`).

**`LocalCluster`:** Type lives in this file (genesis + join pairs, `withAdditionalJoin`, `Consumer` helper). Used by governance tests and `DevsharddRuntimeConfigTests`.

**`initializeCluster`:** Calls `ensureGenesisApiRunning()` on genesis when the stack uses the versiond overlay.

**Detection:** `usesVersiondOverlay()` is true when `config.additionalDockerFilesByKeyName[pairName]` includes `docker-compose.versiond.yml`.

### `local-test-net/docker-compose.versiond.yml`

- **`api`:** `ACCOUNT_PUBKEY`, `KEYRING_BACKEND` from Testermint env (injected after cold-key extract).
- **`versiond`:** `NODE_MANAGER_ADDR`, `VERSIOND_BINARY_NAME`, `VERSIOND_FORCE`, `VERSIOND_OVERRIDE_dev` (must be listed in compose so Testermint host env reaches the container), shared keyring/postgres env for child **devshardd**.

**Why:** devshardd must long-poll the same NodeManager port dapi exposes (9400) and sign with the same keyring dapi registered on chain.

### `testermint/src/test/kotlin/DevsharddRuntimeConfigTests.kt`

- `@BeforeAll`: `initCluster(joinCount = 0, config = versiondDevsharddConfig, reboot = true)`.
- Requires `build/devshardd` (`make devshardd-build`).
- Route prefix `/devshard/<version>/` from [`DevshardVersiondTestConfig.kt`](../../testermint/src/main/kotlin/DevshardVersiondTestConfig.kt) (`devshardTestVersion()`, default **`dev`** from Makefile / `build/devshard-version`).
- **30s propagation SLA** for governance and epoch (matches implementation plan).

### `testermint/src/test/kotlin/RuntimeConfigTests.kt`

- Class-level note: dapi-only; host-path coverage is `DevsharddRuntimeConfigTests`.

### `testermint/src/main/kotlin/LocalInferencePair.kt`

- `runProposal(cluster: LocalCluster, …)` remains a **member** on `LocalInferencePair` (governance proposals from tests).

### `testermint/build.gradle.kts`

- Ignore blank `-DexcludeTags=` / `-DincludeTags=` (empty tag list broke JUnit tag filtering).

### `decentralized-api/scripts/init-docker.sh` (container image)

- When `CREATE_KEY=false` and `ACCOUNT_PUBKEY` is unset, waits and reads the key from the shared keyring. **Testermint’s primary fix** is injecting `ACCOUNT_PUBKEY` from `DockerGroup` so api does not depend on this race.

---

## Apple Silicon: portable BLST (`BLST_PORTABLE=1`)

**Problem:** `genesis-api` crashed at startup with `Caught SIGILL in blst_cgo_init` (exit 132) when running the default **linux/amd64** api image under Docker on M-series Macs.

**Fix:** Docker builds pass `BLST_PORTABLE=1`, which adds `-D__BLST_PORTABLE__` to CGO flags in `decentralized-api/Dockerfile` and `inference-chain/Dockerfile` (see existing `ARG BLST_PORTABLE`).

**Auto-detection (no manual flag on Mac):**

| File | Role |
|------|------|
| [`scripts/blst-portable.mk`](../../scripts/blst-portable.mk) | Included by root, `decentralized-api`, and `inference-chain` Makefiles. Sets `BLST_PORTABLE=1` when `sysctl hw.optional.arm64 == 1` on Darwin (works even if the shell reports `x86_64` under Rosetta). |
| [`scripts/blst-portable.sh`](../../scripts/blst-portable.sh) | Sourced by `local-test-net/stop-rebuild.sh`, `test_build.sh`, `testermint/setup-base-image.sh`. |
| Root `Makefile` | `api-build-docker` / `node-build-docker` pass `BLST_PORTABLE=$(BLST_PORTABLE)` to sub-makes; `devshardd-build` uses the same variable. |

**Override:** `make api-build-docker BLST_PORTABLE=0` on Apple Silicon if you intentionally want the non-portable build.

**Verify BLST dry-run:**

```bash
make -C decentralized-api -n build-docker SET_LATEST=0 2>&1 | grep BLST_PORTABLE
# Expect: BLST_PORTABLE: 1 and --build-arg BLST_PORTABLE=1 on Darwin arm64 / Apple Silicon
```

### Apple Silicon: versiond must be `linux/amd64`

**Problem:** `genesis-versiond` logs `rosetta error: failed to open elf at /lib/ld-musl-x86_64.so.1` and `child exited … signal: trace/breakpoint trap` when starting **devshardd**. `make devshardd-build` always produces an **amd64** musl binary; `versiond` was built without `--platform`, so on M-series Macs the image is often **arm64**. An arm64 parent cannot exec the amd64 musl interpreter reliably under Docker Desktop.

**Fix:** `make versiond-build-docker` and `docker-compose.versiond.yml` pin **`platform: linux/amd64`** (same as `api-build-docker` / `devshardd-build`). Rebuild and recreate the container after changing this:

```bash
make versiond-build-docker
# From repo root (same -f list Testermint uses for genesis + versiond overlay):
docker compose -p genesis \
  -f local-test-net/docker-compose-base.yml \
  -f local-test-net/docker-compose.genesis.yml \
  -f local-test-net/docker-compose.dns.yml \
  -f local-test-net/docker-compose.dns-overrides.yml \
  -f local-test-net/docker-compose.postgres.yml \
  -f local-test-net/docker-compose.versiond.yml \
  --project-directory . \
  up -d --force-recreate versiond
docker inspect genesis-versiond --format '{{.Platform}}'  # expect linux/amd64
```

### Restarting the Testermint environment

There is **no** single `local-test-net/docker-compose.yml`. Testermint runs from the **repo root** with project name **`genesis`** and several `-f local-test-net/...` files (see `DockerGroup.kt`).

| Goal | Command |
|------|---------|
| **Full rebuild + fresh cluster** (after image/Makefile changes) | `cd local-test-net && ./stop-rebuild.sh` then run the Gradle test (or `./gradlew :test …` from `testermint/`). |
| **Stop cluster only** | `cd local-test-net && ./stop.sh` |
| **Let the test reboot the cluster** | Run `DevsharddRuntimeConfigTests` with `reboot = true` in `@BeforeAll` (default), or `touch testermint/reboot.txt` before Gradle. |
| **Recreate versiond only** (after `make versiond-build-docker`) | `docker compose` command in the versiond amd64 section above (from repo root). |

---

## Prerequisites to run Step 7 tests locally

```bash
# From repo root (Apple Silicon: BLST_PORTABLE=1 is automatic)
make api-build-docker
make versiond-build-docker
make devshardd-build   # writes build/devshard-version (Makefile DEVSHARD_VERSION, default `dev`)

cd testermint
# VERSIOND_FORCE follows devshardTestVersion() unless DEVSHARD_VERSION env is set
./gradlew :test --tests "DevsharddRuntimeConfigTests" -DexcludeTags=unstable,exclude
```

Do **not** pass `-DexcludeTags=` with an empty value.

**Cluster sanity after boot:**

```bash
docker logs genesis-api --tail 30    # no SIGILL; servers on :9000/:9100/:9200/:9400
curl -sS http://localhost:9002/admin/v1/nodes
docker logs genesis-versiond --tail 50 # devshardd listening; no "version mismatch" or rosetta/ld-musl
docker inspect genesis-versiond --format '{{.Platform}}'  # expect linux/amd64
```

---

## Debugging a failing Step 7 test

Governance test (`governance flip disables then re-enables…`) expects HTTP **503** within 30s after `devshard_requests_enabled=false`, then **200** after re-enable.

```bash
# docker logs -f accepts only one container; use separate terminals or:
docker logs -f genesis-versiond
# docker logs -f genesis-api
# docker logs -f genesis-proxy

docker logs genesis-versiond --since 10m 2>&1 | grep -iE 'version mismatch|child exited|runtime|GetRuntimeConfig'
docker logs genesis-api --since 10m 2>&1 | grep -iE 'runtime_config|devshard_requests|UpdateParams'
docker logs genesis-proxy --since 10m 2>&1 | grep -iE 'devshard|503'
```

If versiond shows a version mismatch, ensure `make devshardd-build` and Testermint agree: check `cat build/devshard-version` vs `docker logs genesis-versiond` (`VERSIOND_FORCE` / `protocol_version`). Override with `export DEVSHARD_VERSION=<name>` before both make and gradle.

Gradle report: `testermint/build/reports/tests/test/index.html`.

---

## Related docs

- [`params-refactoring-implementation.md`](./params-refactoring-implementation.md) — Steps 3b-T, 7, Testermint e2e table, propagation SLA.
- [`params.md`](./params.md) — Runtime config design and long-poll model.
