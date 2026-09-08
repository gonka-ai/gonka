# Devshard v5 fresh-install sandbox — 2026-09-08

**Result: not accepted.** Fresh topology startup succeeded, but an unmodified gateway could not start inference and the updater preflight failed. A targeted fence-loss test also failed automatic child recovery. Container health and fleet admission did not detect these failures.

## Scope and source

- Guide: `docs/devshard-host-ha-setup.md` at `2b2a50534`.
- Core, real `versiond`, `devshardd` and gateway: `39240311fb`.
- Join files, router fleet, public proxy, policy workers and updater: PR #1611 integration at `9a4ec48886`. These directories were overlaid onto the core archive; this was not a published release image set.
- Isolated Docker-in-Docker daemon (`docker:29-dind`, inner Engine 29.8.0 / Compose 5.5.1). No host deployment or production keys were used, and no sandbox ports were published on the outer host.
- Real PostgreSQL 16 Alpine, versiond, v5 devshardd and both routing tiers. Chain, dapi and ML prerequisites were replaced by the project's mock-chain, mock-dapi and mock-openai with newly generated participant keys.
- The guide's filter override was extracted verbatim. A separate sandbox override supplied prerequisite mocks and private-address access for the isolated test network. Locally built images were preloaded, so the fleet pull policy was `never`.
- No binary override or prepopulated versiond cache: both supervisors downloaded the v5 ZIP from the mock governance feed and verified its SHA256: `896e2b5c61fdc60680f0853c96fd03cfffa9a46b67452232f6c78ad83ad33b37`.

This run validates local deployment wiring with controlled prerequisites. It does not validate published image availability, a real chain/dapi/ML deployment, TLS, external PostgreSQL, multi-host placement or v4 database migration.

## Results on the unmodified component set

| Check | Result |
| --- | --- |
| `versiond-router-fleet.sh prepare-networks` | PASS |
| Main Compose `up -d --wait --wait-timeout 2100` | PASS; fresh persistent PostgreSQL cluster and both real v5 children |
| Fleet `apply`, `verify-admission`, `wait-version v5` | PASS; all three slots healthy, v5 ready 3/3, parent admission confirmed |
| Public `/devshard/v5/healthz` | PASS; HTTP 200 and final upstream header |
| Gateway catalog probe `/v5/healthz` | FAIL; HTTP 404 through nginx policy workers |
| Gateway inference | FAIL; `catalog_pending`, 0/0 height anchors |
| `update-devshard.sh --check` | FAIL; supervisor storage identity returns HTTP 503 despite healthy PostgreSQL and child readiness |
| Terminate one child's PostgreSQL advisory-lock connection | Withdrawal works; automatic replacement FAILS. After 40 seconds the same child PID (98) remains alive and `/readyz?version=v5` returns 503 |

### Public catalog probe mismatch

At core `39240311fb`, `devshard/transport/client.go:308` builds the gateway catalog probe as `/<version>/healthz`. At integration `9a4ec48886`, `proxy/entrypoint.sh:811` routes `/devshard/`; the unprefixed probe reaches the ordinary API path and returns 404. The gateway stays in `catalog_pending` although fleet admission and `/devshard/v5/healthz` succeed.

Minimum reproduction after the guide's startup:

```bash
curl -i http://127.0.0.1:8000/devshard/v5/healthz  # 200
curl -i http://127.0.0.1:8000/v5/healthz           # 404
```

The gateway and public proxy must agree on the catalog probe path. Do not treat the first URL alone as evidence that inference can start.

### Storage wrapper drops optional contracts

At core `39240311fb`, `devshard/cmd/devshardd/session/manager.go:336` wraps the managed store in `ObsRepairGate`. `devshard/storage/obs_repair_gate.go` forwards `Ready`, but does not forward `StorageProof` or `FatalErrors`. Those optional methods are not part of the embedded `Storage` interface.

Consequences reproduced here:

- `HostManager.StorageProof` cannot assert `storage.ProofProvider`; the child `/storage/identity` returns 503, propagated by supervisor `/internal/storage-identity`. The updater correctly stops on this error.
- `HostManager.StorageFatalErrors` receives no fatal-error channel. Terminating the real fence connection logs “postgres fence session lost; terminating process”, but the child stays alive and unready instead of being replaced.

Do not enable updater probe bypasses to turn these failures into a passing check. The storage wrapper must preserve both contracts and the actual boot path must be retested.

## Diagnostic continuation — not release acceptance

Only in the running sandbox policy configs, an exact `/v5/healthz` alias to `/devshard/v5/healthz` was added and nginx reloaded. No repository code or release image was changed. After the reload, the gateway's height seed completed and real inference through both routing tiers reached mock-openai.

- First completion: `devshard-1-1`, served by `versiond2`; escrow `1`, nonce `1`, reported balance `962500`.
- `docker compose stop versiond2` used the configured graceful stop.
- A different prompt completed on surviving `versiond`: `devshard-1-2`; the same escrow, nonce `2`, reported balance `951000`.
- `versiond2` restarted with its existing state and fleet admission passed again.

The request IDs and serving-member logs confirm that the second request was not a gateway cache replay. This shows session continuation after a completed request; it does not establish uninterrupted in-flight SSE or independently audit all fee calculations. The catalog alias is a diagnostic patch, not an operator workaround endorsed by the guide. The fence-loss check was performed afterward and does not depend on that HTTP alias.

## Evidence and reproduction materials

The disposable daemon, its inner containers and anonymous Docker volume were removed after collection, along with the six sandbox image tags created on the outer daemon. No sandbox containers remain running. Source archives, bind-mounted test data and logs are retained in `/tmp/devshard-install-sandbox-Axcawe/`:

- `guide-before.md`, `prepare.py`, `start-install.sh`, `check-chat.sh`, `check-failover.sh`, `check-fence.sh`.
- `logs/02-main-install.log`, `03-fleet-install.log`, `public-health.headers`, `catalog-probe.headers`.
- `logs/chat-unmodified-failed.json`, `gateway-unmodified.json`, `04-updater-preflight.log`, `child-storage-probe.log`.
- `logs/chat-before.json`, `chat-after-stop.json`, gateway state snapshots and serving-member logs.
- `logs/fence-terminate-result.txt`, `fence-child-pid-before.txt`, `fence-child-pid-after.txt`, `fence-ready-after.txt`, `fence-versiond.log`.
- `logs/final-*.log`, image inventory, PostgreSQL system identifier and table inventory.

The setup guide now checks the gateway's actual public catalog path and every member's storage proof. The test plan also requires those checks and a replacement generation after fence loss. The runtime defects remain unresolved in the checked revisions.
