# Cherry-pick guide: `devshard-0.2.13-v2-fork` vs `devshard-0.2.13-v2`

This document lists differences between **`devshard-0.2.13-v2-fork`** (integration-test / Apple Silicon work) and **`devshard-0.2.13-v2`** (upstream baseline). Use it to port only what you need onto another branch, or to understand what the fork adds beyond v2.

Complements:

- [`testermint-infrastructure.md`](./testermint-infrastructure.md) — versiond overlay, genesis boot, DockerGroup behaviour
- [`protocol-version.md`](./protocol-version.md) — runtime version vs state-root protocol version (`v2`)

---

## Branch relationship

| Branch | Position |
|--------|----------|
| **`devshard-0.2.13-v2-fork`** | ~13 commits ahead of `devshard-0.2.13-v2` (~102 files, +3338 / −505 lines) |
| **`devshard-0.2.13-v2`** | 1 commit **not** on the fork: `694c53473` — *join stack `up -d`, fail-fast 501/503, gov 120s SLA* |

Fork commits (oldest → newest):

| Commit | Summary |
|--------|---------|
| `30a541c96` | Fork integration tests |
| `c4a4b40e2` | Merge upstream `devshard-0.2.13-v2` from gonka-ai/gonka |
| `937d6fcd8` | devshardctl: X-Devshard-Error fail-fast on upstream |
| `59f3de4ae` | Prepare for remote run |
| `54e0a2994` | Continue integration tests debugging |
| `33e1db6ec` | Debug integration tests |
| `f2166ddd1` | Added version debugging |
| `2cf450a39` | Debugging e2e testermint |
| `bed20e4c2` | Fixing host stats for validations |
| `10f436717` | e2e tests expect `DEVSHARD_PROTOCOL_VERSION=v2`; docker build arg |
| `c81c7bc76` | Validation observability; e2e testermint timings |
| `62faba5d2` | Running devshard standalone tests on Apple Silicon |

**`694c53473` on v2 only:** Do **not** cherry-pick as a single commit onto the fork—the fork already includes:

- Join inference stack: `docker compose up -d` + services (`DockerGroup.kt`)
- Fail-fast 501/503 + `X-Devshard-Error` (`937d6fcd8` / transport + proxy)
- `governanceReEnableSlaMs = 120_000L` in `DevsharddRuntimeConfigTests.kt`

Diff `694c53473` vs the fork only if a specific test still fails.

---

## Take (needed to run tests reliably)

### 1. Apple Silicon / Docker platform

**Required on Apple Silicon (M-series).** Without this, versiond runs `linux/amd64` while `build/devshardd` is `linux/arm64`; override copy succeeds but `fork/exec` fails with misleading `no such file or directory`.

| Area | Files |
|------|--------|
| Build platform | `scripts/blst-portable.mk`, `scripts/blst-portable.sh`, root `Makefile`, `decentralized-api/Makefile`, `inference-chain/Makefile`, `proxy/Makefile`, `proxy-ssl/Makefile`, `bridge/Makefile`, `versioned/Makefile` |
| versiond compose | `local-test-net/docker-compose.versiond.yml` — `platform: ${DOCKER_PLATFORM:-linux/amd64}` (not hardcoded `linux/amd64`) |
| Rebuild script | `local-test-net/stop-rebuild.sh` — `make versiond-build-docker devshardd-build` |
| Testermint compose env | `testermint/src/main/kotlin/DockerGroup.kt` — `resolveDockerPlatform()`, pass `DOCKER_PLATFORM` to compose |
| Commit | `62faba5d2` |

**Verify after build:**

```bash
file build/devshardd
docker image inspect versiond:latest --format '{{.Os}}/{{.Architecture}}'
# Both should be linux/arm64 on Apple Silicon

docker exec genesis-versiond uname -m          # aarch64
curl -sf http://localhost:8000/devshard/dev/healthz   # ok
```

### 2. Protocol v2 + build stamps

**Required** if e2e and settlement tests expect protocol tag `v2`.

| Area | Files |
|------|--------|
| Protocol version | `devshard/types/protocol_version.go`, `devshard/docs/protocol-version.md`, `DEVSHARD_PROTOCOL_VERSION` in root `Makefile`, `decentralized-api/Dockerfile` |
| Build stamps | `build/devshard-version`, `build/devshard-protocol-version` from `devshardd-build` |
| Testermint | `testermint/src/main/kotlin/DevshardVersiondTestConfig.kt` |
| State / host / tests | `devshard/state/*`, `devshard/host/*`, widespread test updates |
| Commit | `10f436717` |

### 3. Validation observability + parallel test stats

**Required** for current Testermint assertions (`validation_observability`, `getDevshardShardStatsDetail`, parallel settlement waits).

| Area | Files |
|------|--------|
| Storage | `devshard/storage/validation_obs.go`, `observability.go`, `interface.go`, `memory.go`, `sqlite.go`, `postgres.go`, migrations |
| Host / state | `devshard/host/host.go`, `devshard/state/seal.go`, `devshard/state/machine.go` |
| dAPI stats | `decentralized-api/internal/devshard/manager.go`, `validation.go`, `engine.go` |
| Testermint | `DevshardTestSupport.kt`, `data/devshard.kt`, `DevshardTests.kt`, `DevshardStandaloneTests.kt` (e.g. long epoch for streaming) |
| Commits | `c81c7bc76`, `bed20e4c2` |

### 4. devshardctl / transport fail-fast

**Required** for clear client errors (501/503 with `X-Devshard-Error`) instead of opaque 502/timeouts.

| Area | Files |
|------|--------|
| Proxy / HTTP | `devshard/cmd/devshardctl/proxy.go`, `devshard/transport/errors.go`, `httperror.go`, `server.go`, `devshard/server/routes.go`, tests |
| Commit | `937d6fcd8` (overlaps v2 `694c53473`) |

### 5. Testermint cluster / test harness

**Required** for stable versiond stacks and CI/local debugging.

| Area | Files |
|------|--------|
| Join stack | `DockerGroup.kt` — `up -d` for join inference stack + versiond service ordering |
| Diagnostics | `InferenceStackDiagnostics.kt` — compose logging, `logs/docker-dump` on failure |
| Versiond readiness | `DevshardVersiondTestConfig.kt` — `waitForVersiondOverrideReady`, `logVersiondDiagnostics` |
| Stats URL | `LocalInferencePair.kt` — `apiContainerPublicUrl()` for in-container stats curl |
| Runtime tests | `DevsharddRuntimeConfigTests.kt` — governance test re-enabled, `waitForVersiondOverrideReady` |
| Base compose | `local-test-net/docker-compose-base.yml` (minor env tweaks if present) |
| Commits | `30a541c96` and follow-ups through `c81c7bc76` |

**Gradle test filter:** Use wildcards for Kotlin backtick names, e.g. `*DevshardStandaloneTests*`, not exact method strings (see below).

### 6. versiond override diagnostics

**Helpful, low risk** — clearer logs when `VERSIOND_OVERRIDE_*` is missing or copy fails.

| Area | Files |
|------|--------|
| Config / reconcile | `versioned/internal/config/config.go`, `versioned/internal/process/manager.go` |
| Startup | `versioned/cmd/versiond/main.go` |

---

## Omit or narrow (not required for local test runs)

### CI / remote-only workflow

| Change | Why omit locally |
|--------|------------------|
| `.github/workflows/test-workflow.yml` | Hard-coded test matrix (`DevshardTests`, `DevshardStandaloneTests`, … only); disables `listAllTestClasses`; extra artifact upload / docker dump on failure — **fork/CI tuning** |
| `scripts/registry-cache.mk` + `REGISTRY_CACHE_OWNER` in `Makefile` | Only needed when using GHCR build cache in CI |

### Debug / noise in production code

| Change | Why omit |
|--------|----------|
| `decentralized-api/broker/ml_lock_log.go` (new) | ML lock **debug** tracing (`ml_lock_wait_start` / `ml_lock_wait_end`) |
| `decentralized-api/broker/lock_helpers.go`, `broker.go`, `commands.go` | Wiring for ML lock debug |
| `decentralized-api/main.go` | Review: may only enable debug logging for test runs |
| `devshard/logging/slog_env.go` | Env-based slog tuning for debug sessions |

### Intermediate “debugging” commits

Do **not** cherry-pick these individually; take **final** file state from the fork tip instead:

- `59f3de4ae` — Prepare for remote run
- `54e0a2994` — Continue integration tests debugging
- `33e1db6ec` — Debug integration tests
- `f2166ddd1` — Added version debugging
- `2cf450a39` — Debugging e2e testermint

Useful end state is already in `DevshardVersiondTestConfig.kt`, `InferenceStackDiagnostics.kt`, and `DockerGroup.kt`.

### Test-only verbosity (optional)

| Change | Note |
|--------|------|
| `DevshardTestSupport.traceDevshardInferencePhase` | Helpful when debugging failures; omit for minimal diff |
| Extra `logVersiondDiagnostics` calls | Keep `waitForOverrideVersionedHealth` / `waitForVersiondOverrideReady`; trim redundant logging if desired |
| `RuntimeConfigTests.kt` timing tweaks | Take only if those tests flake on your machine |

### Commit on v2 but not merged as-is on fork

| Commit | Action |
|--------|--------|
| `694c53473` on `devshard-0.2.13-v2` | **Do not cherry-pick wholesale** — fork already has equivalent `up -d`, fail-fast, and 120s gov SLA |

---

## Practical strategies

### Option A — run tests now (simplest)

Stay on `devshard-0.2.13-v2-fork`, rebuild, run tests:

```bash
source scripts/blst-portable.sh   # or ./local-test-net/stop-rebuild.sh
make versiond-build-docker devshardd-build
cd testermint
./gradlew :test --tests '*DevshardStandaloneTests*' -DexcludeTags=unstable,exclude
```

### Option B — minimal port onto `devshard-0.2.13-v2`

Cherry-pick or merge **themes** in this order:

1. `10f436717` — protocol v2 build + test alignment
2. `c81c7bc76` + `bed20e4c2` — validation observability + stats API
3. `937d6fcd8` — fail-fast (or `694c53473` from v2 if you stay on v2 base)
4. `62faba5d2` — Apple Silicon platform (compose + Makefile + `DockerGroup`)
5. `30a541c96` … testermint harness — or one squashed “testermint integration” commit

### Option C — PR to upstream

Include **Take** sections 1–6; **omit** CI matrix narrowing and broker ML debug unless upstream explicitly wants them.

---

## Pre-flight checklist (`DevshardStandaloneTests`)

- [ ] `make versiond-build-docker devshardd-build` with the same `DOCKER_PLATFORM`
- [ ] `file build/devshardd` and `docker image inspect versiond:latest` both **arm64** on Apple Silicon (both **amd64** on Linux CI)
- [ ] `curl -sf http://localhost:8000/devshard/dev/healthz` returns `ok` with cluster up
- [ ] Gradle: `--tests '*DevshardStandaloneTests*'` (wildcard), not `'DevshardStandaloneTests.exact method name()'`
- [ ] `testermint/build.gradle.kts` has `isFailOnNoMatchingTests = false` — a fast **BUILD SUCCESSFUL** with **0 tests** is a filter miss, not a pass
- [ ] Standalone inference e2e: call `waitForOverrideVersionedHealth(genesis)` after epoch wait (streaming test already does; add to non-streaming e2e if missing)

---

## Changed file inventory (fork vs v2)

Full diff stat (~102 files). Grouped by subsystem:

| Subsystem | Representative paths |
|-----------|----------------------|
| Build / platform | `Makefile`, `scripts/blst-portable.*`, `scripts/registry-cache.mk`, `local-test-net/docker-compose.versiond.yml`, `stop-rebuild.sh` |
| devshard core | `devshard/storage/*`, `devshard/state/*`, `devshard/host/*`, `devshard/types/*`, `devshard/transport/*`, `devshard/cmd/devshardctl/*` |
| dAPI | `decentralized-api/internal/devshard/*`, `decentralized-api/broker/*`, `decentralized-api/Dockerfile` |
| versiond | `versioned/internal/config/*`, `versioned/internal/process/manager.go`, `versioned/cmd/versiond/main.go` |
| Testermint | `testermint/src/main/kotlin/DockerGroup.kt`, `DevshardVersiondTestConfig.kt`, `InferenceStackDiagnostics.kt`, `LocalInferencePair.kt`, `testermint/src/test/kotlin/Devshard*.kt` |
| CI | `.github/workflows/test-workflow.yml` |

Generate an up-to-date list:

```bash
git diff devshard-0.2.13-v2...devshard-0.2.13-v2-fork --stat
git diff devshard-0.2.13-v2...devshard-0.2.13-v2-fork --name-only
```

---

## Related failures (quick reference)

| Symptom | Likely cause | Fix theme |
|---------|--------------|-----------|
| `version "dev" not found` / 404 on chat | versiond child not running | §1 Apple Silicon platform + health wait |
| `fork/exec ... devshardd: no such file or directory` with file present | arm64 binary in amd64 container | §1 |
| Gradle success in &lt;30s, no `N test completed` | Wrong `--tests` filter | Pre-flight checklist |
| `NullPointerException` on shard stats | curl to host proxy from inside api container | §5 `apiContainerPublicUrl()` |
| Streaming test fails on inference 17 | PoC / short epoch | Long epoch config in `DevshardTests` / `DevshardStandaloneTests` |
| Parallel test stats timeout | Missing validation observability | §3 |

---

*Last aligned with fork tip containing commits through `62faba5d2` (Apple Silicon standalone tests). Re-run `git log` / `git diff` on your branches if history has moved.*
