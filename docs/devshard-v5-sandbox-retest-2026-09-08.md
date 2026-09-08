# Devshard v5 fresh-install retest — 2026-09-08

**Result: the two fixes passed runtime acceptance.** A new empty deployment started from the setup guide, completed gateway inference and updater preflight, continued the same session after stopping its serving member, and replaced a child after PostgreSQL fence loss. No diagnostic nginx alias or updater probe bypass was used. A separate concurrent payload schema creation race delayed first startup; the supervisor recovered automatically.

## Source and isolation

- Fix branch: `sn/devshard-v5-ha-install-fixes` ([PR #1730](https://github.com/gonka-ai/gonka/pull/1730)), based on the original core revision `39240311fb`.
- `c78fcfbc4`: gateway catalog probes use the public `/devshard/<version>/healthz` path. Direct router probes retain their internal paths; the standalone test host accepts the public form too.
- `bf4de2d21`: `ObsRepairGate` forwards `StorageProof` and `FatalErrors` to the actual backend, preserving unsupported-backend behavior.
- Join, router fleet, public proxy, policy workers and updater: unchanged PR #1611 integration `9a4ec48886`, overlaid onto the fixed core. This was not a published release image set.
- Fresh Docker-in-Docker daemon: Engine 29.8.0, Compose 5.5.1, no outer published ports. New PostgreSQL data, participant/user keys, gateway state and versiond caches.
- Real PostgreSQL 16 Alpine, versiond, v5 devshardd, gateway and both routing tiers. Project mock-chain, mock-dapi and mock-openai supplied controlled prerequisites. Unchanged auxiliary binaries from the original core build were reused; devshardd and gateway were rebuilt with the fixes and production build settings.
- The guide's override was extracted verbatim. A separate sandbox override supplied mocks and private-address access. Images were preloaded locally; fleet pull policy was `never`.
- Both supervisors downloaded and verified the new v5 ZIP from the mock governance feed, without a binary override. SHA256: `d6b3f2e9a0a209352fe047cc29e7ab4e378450e54d1c1ec8a0d9150e8d0759c7`.

## Runtime results

| Check | Result |
| --- | --- |
| `versiond-router-fleet.sh prepare-networks` | PASS |
| Main Compose `up -d --wait --wait-timeout 2100` | PASS without intervention; one child retried after the schema race described below |
| Fleet `apply`, `verify-admission`, `wait-version v5` | PASS; three healthy slots, v5 ready 3/3, parent admission confirmed |
| Public gateway catalog probe | `/devshard/v5/healthz` returns 200; gateway height seed completes 1/1 |
| Bare public `/v5/healthz` | Remains 404; the fixed gateway no longer uses this internal path publicly |
| Every member's `/internal/storage-identity` | PASS; nonempty generation targets and the same database identity |
| `update-devshard.sh --check` | PASS with database probes enabled; both generations wrote challenges observed in the configured database |
| First inference | PASS on `versiond`: `devshard-1-1`, escrow 1, nonce 1, reported balance 962500 |
| Stop the serving member and continue | PASS on `versiond2`: `devshard-1-2`, same escrow, nonce 2, reported balance 950000; stopped member restarted and rejoined |
| Terminate `versiond`'s actual PostgreSQL fence connection | PASS; child PID 94 exited, replacement PID 218 became ready with proof generation 2 instead of 1, same database identity; recovery took 6 seconds |
| Inference on the replacement | PASS with the other member stopped: `devshard-1-11`, same escrow, nonce 11, reported balance 929300 |
| Restore both members and repeat admission/preflight | PASS; v5 ready on all three router slots and both generations passed database challenges again |

Each inference used a different prompt. Serving-member logs confirm successful execution on the expected member, including the replacement child. Background protocol activity advanced nonces between the second and third completions. Reported balances and monotonic nonces were recorded; this run did not independently audit every fee calculation or prove uninterrupted in-flight SSE.

## Additional first-start finding

On the first concurrent boot, one child exited with `SQLSTATE 23505`, constraint `pg_type_typname_nsp_index`, while initializing payload storage. The other child completed startup. After the default readiness timeout and retry, versiond started the failed child successfully, and the guide's Compose wait completed without intervention.

`common/storage/payloads/store.go` runs `CREATE TABLE IF NOT EXISTS` without serializing concurrent first initialization. PostgreSQL can race on the table's implicit row type despite that clause. This occurs before the separately locked session-storage migrations. The race remains a separate defect; the two fixes in this branch do not change payload schema initialization. The startup and recovery logs are retained so the successful retry is not mistaken for an error-free first attempt.

## Regression tests and limits

- Gateway URL cases, catalog waiting through a prefix-stripping HTTP proxy, heartbeat admission, standalone host routes and affected height-seed tests passed.
- Restoring the original gateway method through a Go overlay made the new proxy regression fail as expected.
- HostManager tests exercise the real `ManagedStorage → ObsRepairGate` chain: identity/write/read proof operations, cancellation, fatal notification during observation repair and unsupported backends. They failed before the fix and passed afterward.
- Existing observation-repair tests and the application's immediate exit on terminal storage failure also passed.

This retest covers the local fresh-install path and the reproduced gateway, preflight, failover and fence-recovery failures. It does not validate published images, a real chain/dapi/ML deployment, TLS, external PostgreSQL, multi-host placement, v4 migration or an actual updater rollout. Updater preflight writes transient storage challenges; its CLI message “nothing was changed” is not a claim of zero database writes.

## Evidence

Artifacts are retained in `/tmp/devshard-install-fixed-t6orn926/`: guide snapshots, source archive with fixes, fixture setup and run scripts, binary checksum, unit-test transcripts and runtime logs. Key evidence under `logs/` includes:

- `02-main-install.log`, `03-fleet-install.log`, `04-updater-preflight.log`, `07-updater-after-recovery.log`.
- `public-health.headers`, `bare-catalog.headers`, `proof-versiond.json`, `proof-versiond2.json`.
- `chat-*.json`, `gateway-*.json`, `stopped-serving-member.txt`, serving-member logs.
- `fence-proof-before.json`, `fence-proof-after.json`, child PID snapshots, `06-fence.log`, `fence-versiond.log`.
- `acceptance-assertions.txt`, `final-*.log`, image inventory, PostgreSQL system identifier and table inventory.

The disposable daemon, its containers and anonymous Docker volume were removed after collection, along with the six custom sandbox image tags. Bind-mounted test data, source and logs remain available locally.
