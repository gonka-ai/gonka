# HA host updater acceptance gate

This document is the release gate for changes to the join host updater,
router fleet, public proxy cutover, and bundled PostgreSQL migration. It is a
scenario contract, not an implementation description. A change is acceptable
only when every required invariant below has evidence from the named gate.

Related documents:

- [release-0.2.15-v5.md](./release-0.2.15-v5.md)
- [storage-design.md](./storage-design.md)
- [postgres-persistence-migration.md](./postgres-persistence-migration.md)
- [high-availability-architecture.md](./high-availability-architecture.md)

## 1. Merge rule

The PR is accepted only when all of the following are true:

1. Every `Automated` scenario below exits zero using its named command;
   regression harnesses are always run in gate mode. A passing
   bug-reproduction mode is evidence of a bug, not acceptance evidence.
2. Every `Manual` scenario has an attached run log from the candidate commit.
   A manual result may not waive an automated failure.
3. The complete gate is run three consecutive times without a retry or an
   intermittent failure.
4. The test environment starts without updater/fleet containers, networks,
   images, or rollback tags left by a previous run, except where cached state
   is the explicit test input.
5. Failure cases assert both the command status and post-failure state. Merely
   matching an error message is insufficient.
6. There are no `xfail`, ignored failures, or `--repro` invocations in CI.

Evidence/status labels in the scenario tables are normative:

- `Automated` or `Automated regression` means the named executable gate must
  exist, run in CI, and be green.
- `Automated test required` means coverage is not implemented yet and the
  acceptance gate is therefore red.
- `Manual` means a candidate-specific run log is required in addition to the
  automated coverage; it is not a permanent substitute for automatable work.

The regression harnesses have deliberately different modes:

```bash
# Proves that a known bug is present. This is expected to pass before its fix,
# but MUST NOT be used as an acceptance gate.
deploy/join/update-devshard-regression_test.sh --repro
deploy/join/versiond-router-fleet-regression_test.sh --repro
deploy/join/devshard-postgres-provenance_acceptance_test.sh --repro

# Proves the required invariant. Only these invocations count for acceptance.
deploy/join/update-devshard-regression_test.sh --gate
deploy/join/versiond-router-fleet-regression_test.sh --gate
deploy/join/devshard-postgres-provenance_acceptance_test.sh --gate
```

Two whole-path gates complement the scenario harnesses. They are the answer
to the bug class the harnesses were written against: fakes that encode the
author's assumptions about Docker, Compose DNS and the shell tools.

```bash
# Every host name a service resolves (environment, URLs) must be published by
# a service or alias on a network that service joins, across the rendered join
# model and the router fleet slot model. Seconds, no daemon.
deploy/join/compose-contract_test.sh

# The v4 -> v5 cutover with the real updater, the real Docker daemon, the
# router, public proxy and policy images built from this repository, a real
# PostgreSQL in the v4 anonymous volume, and versiond stand-ins that keep their
# storage lineage in that PostgreSQL. Asserts the migrated data, the fleet
# admission by the new proxy, a request through the published port, a no-op
# second run, and convergence after a run killed during the replica step.
deploy/join/update-devshard-e2e_test.sh
```

### Baseline when this gate was introduced

At commit `1eebd4768` on 2026-09-02 the focused suites establish this starting
point:

| Harness | `--repro` | `--gate` | Meaning |
| --- | --- | --- | --- |
| `deploy/join/update-devshard-regression_test.sh` | PASS, 6/6 | RED, 6/6 | Proof parsing/fail-open, slot discovery, policy bootstrap, offline retry, and capacity documentation remain open. |
| `deploy/join/versiond-router-fleet-regression_test.sh` | PASS, 7/7 | RED, 7/7 | Zero-ready admission, PG/maintenance races, interrupted recovery, tag isolation, and offline recovery remain open. (The harness has since grown to 19 scenarios against an in-memory Docker model; the tag scenarios became record scenarios when the previous generation of a slot became a stopped container instead of an image tag.) |
| `deploy/join/devshard-postgres-provenance_acceptance_test.sh` | PASS, 2/2 | RED, 2/2 | Divergent same-system-ID histories and stale sibling markers remain open. |

This historical baseline explains why the gate exists. Acceptance evidence
must be produced from the final candidate commit and must show green gate
mode; changing this table is not a substitute for fixing a scenario.

The three harnesses run in gate mode in CI (`verify.yml`: the join update
script job, the versiond-router configuration job and the PostgreSQL
migration job) together with the two whole-path gates, so a regression in any
scenario fails the pull request.

## 2. Hard invariants

These invariants apply to every scenario, including retry and rollback:

- **Routability:** the updater never exits successfully while any required
  static, pinned, or admitted catalog route has zero ready routers or zero
  ready upstreams. An absent fleet is not an exception.
- **Continuity:** a failed ordinary rollout does not interrupt already
  accepted HTTP/SSE requests and never reduces the ready router reserve below
  `VERSIOND_ROUTER_MIN_READY`. A declared maintenance outage is the only
  exception.
- **Storage lineage:** no successful run silently changes database history.
  Every local and declared remote writer proves the same database, and a write
  made through every generation is visible through every target before any
  storage-affecting mutation.
- **Fail closed:** an unavailable, malformed, timed-out, or unauthorized
  dependency is never treated as an absent optional dependency.
- **Retry convergence:** after updater `SIGKILL`, container death, Docker
  restart, or host reboot, rerunning the same command converges either to the
  complete candidate state or the exact previous state. It never reports
  success for a mixed state.
- **Rollback fidelity:** rollback restores image, command, environment,
  mounts, networks, endpoint membership, route contract, and scale. An image
  tag alone is not a complete rollback record.
- **Rollback isolation:** every recovery artifact is scoped by deployment and
  fleet identity; two installations on one Docker daemon cannot overwrite or
  consume each other's anchors.
- **Bounded failure:** Docker, registry, DNS, and PostgreSQL hangs are bounded
  by documented timeouts and leave enough durable state for retry.
- **No early cleanup:** legacy containers/data and previous-generation
  recovery artifacts are retained until route-aware public E2E verification
  succeeds.

## 3. Baseline and topology scenarios

| ID | Setup and fault injection | Required result | Evidence / status |
| --- | --- | --- | --- |
| `BASE-01` | Lint and render all stock single, local HA, third-replica, remote, endpoint, recovery, and observability Compose combinations. | Shell and Compose contracts are valid; no undeclared or topology-dependent interpolation. | Automated: `shellcheck deploy/join/deployment-lock.sh deploy/join/update-devshard*.sh deploy/join/devshard-postgres-*.sh deploy/join/versiond-router-fleet*.sh`; `deploy/join/versiond-compose-config_test.sh`. |
| `BASE-02` | Remove each required host utility in turn, including `timeout`; supply zero, negative, non-numeric, and overflowing duration values. | Validation rejects before any Docker mutation and names the missing/invalid input; no control operation can wait forever. | Automated test required in both regression harnesses: selector `BASE-02`. |
| `FRESH-01` | Empty host, empty PostgreSQL persistence root, no cached application images; run the HA updater. | Fresh PostgreSQL initializes exactly once; router fleet, policy workers, proxy, and all declared replicas become healthy; required routes pass public E2E. | Automated: `deploy/join/devshard-postgres-upgrade_test.sh`; full-path Docker case required in `deploy/join/update-devshard-regression_test.sh --gate FRESH-01`. |
| `FRESH-02` | Empty host; run the single-instance topology without the HA overlay. | The updater does not create HA networks, fleet slots, or PostgreSQL and the public route becomes healthy. | Automated: `deploy/join/update-devshard_test.sh`; real Docker case required in `deploy/join/update-devshard-regression_test.sh --gate FRESH-02`. |
| `FRESH-03` | Interrupt fresh PostgreSQL initialization before and after the completion marker, then restart container and updater. | An incomplete cluster is never served; a complete cluster is reused; initialization and retries are idempotent. | Restart-after-completion is automated by `deploy/join/devshard-postgres-upgrade_test.sh`; deterministic interruption cases are still required. |
| `UPG-01` | Start the real v4 Compose fixture with data, then perform v4→v5 HA migration. | Rows, PostgreSQL system identity, credentials, ownership, and durability survive; the v4 source is retained until final admission. | Automated: `deploy/join/devshard-postgres-upgrade_test.sh`; full updater path: `deploy/join/update-devshard-e2e_test.sh` (a row written before the cutover is read after it through the migrated persistent cluster). |
| `UPG-02` | First v4→v5 router bootstrap while old `versiond` containers are attached only to the legacy Compose network. | Old upstreams are made reachable to candidates before the first slot is admitted; the fleet cannot bootstrap with a required zero-ready route. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-BOOTSTRAP-ZERO-READY`; full path: `deploy/join/update-devshard-e2e_test.sh` (the pre-v5 replicas are attached under the pool alias before `apply`, and the fleet is admitted by the new proxy). |
| `UPG-03` | First nginx→HAProxy cutover with no pre-existing `proxy-policy-front` network or `proxy-policy-ingress` alias. | Both policy workers have a resolvable/healthy ingress dependency before they are awaited; no startup dependency cycle occurs. | Automated regression: `deploy/join/update-devshard-regression_test.sh --gate UPD-POLICY-BOOTSTRAP`; real images: `deploy/join/update-devshard-e2e_test.sh`; render contract: `deploy/join/compose-contract_test.sh`. |
| `UPG-04` | Fail public route-aware E2E after policy/proxy replacement but before legacy removal. | Exact nginx/public-proxy state is restored, public traffic remains available, and the recovery anchor is not overwritten on retry. | Automated test required: `deploy/join/update-devshard-regression_test.sh --gate UPG-04`. |
| `UPG-05` | Keep a long-lived request and an SSE stream open across first cutover and versiond replacement. | Both complete without truncation; all new requests route only through an admitted candidate. | Automated Docker test required: `deploy/join/update-devshard-regression_test.sh --gate UPG-05`. |

## 4. Day-2 and idempotency scenarios

| ID | Setup and fault injection | Required result | Evidence / status |
| --- | --- | --- | --- |
| `UPD-DISCOVERY-SLOTS` | Complete one HA fleet apply, leave all slot containers running, then invoke updater without explicit `COMPOSE_FILE`. | Autodiscovery selects only the main deployment Compose model; slot labels cannot poison or replace it. | Automated regression: `deploy/join/update-devshard-regression_test.sh --gate UPD-DISCOVERY-SLOTS`. |
| `DAY2-02` | Run the same updater twice after a successful deployment. | Second run is a bounded no-op, keeps the same identities/routes, and does not replace rollback anchors with the current candidate. | Automated: `deploy/join/update-devshard-e2e_test.sh` (container identities unchanged, lineage proven through every replica); the rollback-anchor assertion is still required as `deploy/join/update-devshard-regression_test.sh --gate DAY2-02`. |
| `DAY2-03` | Change only command, environment, mounts, networks, endpoint file, or replica scale; make the candidate fail readiness. | Rollback restores the complete previous service/fleet specification, not merely the previous image. | Automated test required in both regression harnesses: selector `DAY2-03`. |
| `DAY2-04` | Publish a new digest behind a mutable configured tag and rerun; repeat with a digest-pinned reference. | Tag refresh policy is explicit and fetches the new tag; digest-pinned runs are deterministic and never silently use a different digest. | Automated test required: `deploy/join/update-devshard-regression_test.sh --gate DAY2-04`. |
| `DAY2-05` | Make Docker `ps` fail during migration preflight, rollback-anchor discovery, and old-replica decommission. | Every control-plane error aborts; disk checks are not skipped, an unanchored mutation does not start, and a running old replica is not reported removed. | Automated test required: `deploy/join/update-devshard-regression_test.sh --gate DAY2-05`. |
| `DAY2-06` | Configure the legacy owner to a replica other than `versiond` and update all replicas. | The legacy owner is updated last and remains available until every replacement is admitted. | Automated test required: `deploy/join/update-devshard-regression_test.sh --gate DAY2-06`. |
| `LOCK-01` | Invoke the same Compose project concurrently through config files in two different directories; repeat update versus `--check`. | Deployment identity, not config path spelling, owns the lock; at most one mutating operation proceeds and observers cannot corrupt its challenge. | Automated test required: `deploy/join/update-devshard-regression_test.sh --gate LOCK-01`. |
| `UPD-OFFLINE-RERUN` | Cache updater, candidate, and previous images; interrupt after fleet mutation; make the registry unavailable and rerun. | The updater-to-fleet default is cache-safe and retry reaches an admitted or exactly restored state without a mandatory registry call. | Automated source-contract regression: `deploy/join/update-devshard-regression_test.sh --gate UPD-OFFLINE-RERUN`; stateful runtime coverage remains required as `RT-OFFLINE-RETRY-CACHED`. |

## 5. PostgreSQL availability and storage-lineage scenarios

| ID | Setup and fault injection | Required result | Evidence / status |
| --- | --- | --- | --- |
| `PG-01` | PostgreSQL refuses connections before updater preflight. | Updater fails before pull/up/stop/tag/network mutations and reports the failed write-capable probe. | Automated: `deploy/join/update-devshard_test.sh`; real PostgreSQL case required as `deploy/join/update-devshard-regression_test.sh --gate PG-01`. |
| `UPD-PROOF-HEADERS` | Query a real v5 proof endpoint using the exact BusyBox `wget` invocation, including response headers on stderr. | Exactly one JSON document reaches `jq`; HTTP status/headers are parsed separately. | Automated regression: `deploy/join/update-devshard-regression_test.sh --gate UPD-PROOF-HEADERS`; whole path: `deploy/join/update-devshard-e2e_test.sh` (the rerun proves the lineage through both replicas with the real tools). |
| `UPD-PROOF-FATAL` | Return 503, malformed JSON, timeout, Docker exec failure, and permission failure from one proof endpoint. | Every case is fatal and cannot be converted to `continue`, “legacy 404”, or an empty identity. No mutation follows. | HTTP 503 is automated by `deploy/join/update-devshard-regression_test.sh --gate UPD-PROOF-FATAL`; malformed/timeout/Docker/permission variants are still required. |
| `PG-04` | All running replicas expose the legacy 404 proof response during first v4→v5 rollout; point the candidate at a different valid writable database. | Bootstrap continuity is established from the current artifact before mutation, or the updater refuses the run. | Automated test required: `deploy/join/update-devshard-regression_test.sh --gate PG-04`. |
| `PG-05` | Give one local replica a physical clone with the same PostgreSQL system identifier and another the original; repeat with a declared remote replica. | Cross-generation, cross-target challenge fails and names the disagreeing target. Every declared writer participates. | Automated real-PostgreSQL test required: `deploy/join/update-devshard-regression_test.sh --gate PG-05`. |
| `PG-06` | Let one of several replicas return a valid proof while another times out or returns 503. | The valid proof cannot mask the failed replica; updater aborts before mutation. | Automated test required: `deploy/join/update-devshard-regression_test.sh --gate PG-06`. |
| `PG-07` | Drop/blackhole PostgreSQL after router preflight, after the last route gate, and while a candidate child is becoming ready. | Required routes never reach zero-ready with a successful result; the active slot/replica remains or exact rollback completes. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-PG-DROP-AFTER-GATE`; updater integration case required. |
| `PG-08` | Hang PostgreSQL sockets rather than refusing them; separately fill the PostgreSQL volume during challenge/migration. | Failure is bounded, no partial state is admitted, and a retry after recovery converges without data loss. | Automated Docker case required; manual host/volume corroboration required. |
| `PG-09` | Stop PostgreSQL during normal service, then restore the same database. | All PG-backed routes withdraw, public calls fail explicitly, and routes return without changing storage identity or requiring fleet recreation. | Automated integration test required; manual production-like run required. |
| `PG-10` | Run two `--check` commands concurrently and run `--check` alongside an update. | Checks cannot overwrite a singleton nonce or produce a false clone alarm; check has no undocumented persistent side effect, or participates in the same lock. | Automated test required: `deploy/join/update-devshard-regression_test.sh --gate PG-10`. |
| `PG-11` | Set the documented emergency database-change override while any local or remote writer remains running. | Updater refuses until every writer is stopped and the operator explicitly acknowledges the identity change. | Automated test required: `deploy/join/update-devshard-regression_test.sh --gate PG-11`. |
| `PG-12` | Give replicas different `PGHOSTADDR`, port, TLS mode/cert/key/root/CRL, service, or connection options; separately mount candidate-only TLS files. | Storage-model comparison rejects every effective destination/security mismatch, and preflight can inspect the same credential mounts the candidate will use without borrowing stale mounts. | Automated test required: `deploy/join/update-devshard-regression_test.sh --gate PG-12`. |

The storage challenge must exercise the full matrix, not only the first proof
or `.targets[0]`:

```text
for every running/declaratively configured writer W:
    prove identity and snapshot for W
    for every generation G exposed by W:
        write a unique nonce through G
        read that nonce through every target T
```

All identities and snapshots must remain stable for the duration of the
matrix. A disappeared writer or changed snapshot is a failed gate.

## 6. Router, catalog, and rollback scenarios

| ID | Setup and fault injection | Required result | Evidence / status |
| --- | --- | --- | --- |
| `RT-BOOTSTRAP-ZERO-READY` | Absent fleet; declare a static HA route with zero ready upstreams, with and without a healthy legacy route. | `apply` fails before committing a slot. A healthy unrelated route cannot mask the required route. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-BOOTSTRAP-ZERO-READY`. |
| `RT-CATALOG-ZERO-READY` | Catalog admits a new route that is zero-ready before rollout; repeat with catalog unavailable/stale. | The admitted route remains a required postcondition; rollout cannot stop a slot or exit successfully while it is zero-ready. Last known good catalog routes are not silently dropped. | Automated test required: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-CATALOG-ZERO-READY`. |
| `RT-PG-DROP-AFTER-GATE` | Remove the PG-backed route immediately after the final pre-stop gate. | Slot replacement aborts or restores the previous slot; fleet/status cannot report success with zero-ready route. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-PG-DROP-AFTER-GATE`. |
| `RT-MAINT-GATE-RACE` | During acknowledged maintenance, remove the required route after the gate and while the first rollback snapshot/tag is captured. | Maintenance aborts before replacement or restores the exact previous fleet. A zero-ready route cannot disappear from the snapshot. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-MAINT-GATE-RACE`. |
| `RT-SIGKILL-RETRY-PREVIOUS` | `SIGKILL` the fleet command immediately after a slot is stopped; make candidate image unhealthy; rerun `apply`. | Retry uses the durable previous-generation record before capacity repair and restores the old healthy slot. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-SIGKILL-RETRY-PREVIOUS`. The record is the previous generation's own container, kept stopped until the replacement is committed. |
| `RT-CRASH-EXITED-CANDIDATE` | `SIGKILL` after the candidate of a slot was created and exited; rerun `apply`. | The exited candidate is removed and the previous generation is started again; a bad candidate is never restarted as if it had served. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-CRASH-EXITED-CANDIDATE`. |
| `RT-MAINT-RETRY-EXACT` | `SIGKILL` maintenance after one or two slots were replaced; fail a later candidate and retry. | Exact previous image/config/membership/slot placement is restored for every slot; no mixed fleet is described as restored. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-MAINT-RETRY-EXACT`; the environment of every restored generation is asserted by `RT-MAINT-RETRY-EXACT-ENV`. |
| `RT-MAINT-UNHEALTHY-CANDIDATE` | An interrupted maintenance left a slot with an unhealthy candidate on the candidate specification next to its stopped previous generation; retry and fail again. | The unhealthy candidate is removed, the previous generation is put back; no slot is left stopped. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-MAINT-UNHEALTHY-CANDIDATE`. |
| `RT-ROLLBACK-RECORD-ISOLATION` | Operate two fleets with the same slot numbers on one daemon; fail a rollout in one of them. | A rollback touches only the containers of its own fleet; the record of a replacement is the slot's own previous container, addressed by fleet and slot labels, never by a shared tag namespace. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-ROLLBACK-RECORD-ISOLATION`. |
| `RT-ROLLBACK-RECORD-PROVENANCE` | A slot serves a healthy generation on the candidate specification while a stale stopped generation from an older operation stands next to it; run `apply`. | The serving generation is committed and the stale record removed; a leftover record never rolls a serving candidate back. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-ROLLBACK-RECORD-PROVENANCE`. |
| `RT-OFFLINE-RETRY-CACHED` | Cache candidate and previous images; interrupt rollout; make registry DNS/connect fail; rerun. | Recovery proceeds from verified local digests without a mandatory pull and converges to previous or candidate state. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-OFFLINE-RETRY-CACHED`. A stopped previous generation also pins its image in Docker's store. |
| `RT-CATALOG-DROP-AFTER-RESERVE` | A catalog route is ready on every slot at the start; the pool loses it after the reserve gate of the first replacement. | The candidate, which declares the route from the shared cache but cannot serve it, is rejected and the previous generation restored; the run fails. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-CATALOG-DROP-AFTER-RESERVE`. |
| `RT-CATALOG-ZERO-READY-AT-START` | Every slot declares a catalog route that nothing serves. | `status` lists the route with zero ready routers and fails; `verify-admission` fails. Discovery keeps every declared route, not only the ready ones. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-CATALOG-ZERO-READY-AT-START`. |
| `RT-CATALOG-ROUTE-LOST-BY-CANDIDATE` | Removals are not allowed; a candidate comes up without a protected catalog route (lost cache). | The candidate is rejected and the previous generation restored. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-CATALOG-ROUTE-LOST-BY-CANDIDATE`. |
| `RT-STATUS-STATIC-ZERO-READY` | `VERSIOND_VERSIONS` declares a version the pool does not serve. | `status` names the required version with zero ready routers and fails. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-STATUS-STATIC-ZERO-READY`. |
| `RT-ENDPOINT-MEMBERSHIP-ATOMIC` | Change the endpoint list (or `VERSIOND_HOSTS`) of a running fleet and run `rollout`. | Membership is part of the placement contract: the rolling replacement is refused and `maintenance-rollout` is named, so one consistent-hash ring replaces another instead of two serving at once. The list travels in the generation's environment, so a restored generation keeps the membership it was created with. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-ENDPOINT-MEMBERSHIP-ATOMIC`. |
| `RT-STATIC-HA-REMOVAL` | Withdraw a served version from `VERSIOND_VERSIONS` and run `rollout`. | The withdrawn version does not block its own removal; the fleet rolls to the reduced declaration. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-STATIC-HA-REMOVAL`. |
| `RT-DYNAMIC-CATALOG-REMOVAL` | With `VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS=true` the catalog drops a served route; run `rollout`. | Candidates carry the removal policy and are not required to serve the removed route; the rollout converges. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-DYNAMIC-CATALOG-REMOVAL`. |
| `RT-PREVIOUS-NO-AUTORESTART` | Stop a slot for replacement, then restart the Docker daemon. | The kept previous generation does not start next to its replacement: its restart policy is switched off while it is kept and switched back on when it is restored or started again. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-PREVIOUS-NO-AUTORESTART`. |
| `RT-PG-DROP-BEFORE-COMMIT` | The pool loses a required route after the candidate passed its route gate and before its previous generation is removed. | The admission wait and the commit refuse; the previous generation is put back and still exists. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-PG-DROP-BEFORE-COMMIT`. |
| `RT-PG-DROP-DURING-MAINTENANCE-COMMIT` | During a maintenance rollout the pool loses a required route after the first previous generation was removed. | Past the commit point nothing is rolled back: every slot keeps its serving candidate, the remaining removals are cleanup a rerun of `apply` finishes. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-PG-DROP-DURING-MAINTENANCE-COMMIT`. |
| `RT-REBOOT-BEFORE-STOP` | The Docker daemon restarts between the restart-policy change and the stop of a generation being replaced. | The only generation of the slot comes back (transitional policy unless-stopped); after the stop it stays stopped next to its replacement. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-REBOOT-BEFORE-STOP`. |
| `RT-SIGKILL-DURING-COMMIT-CLEANUP` | `SIGKILL` a maintenance rollout after its commit point while previous generations are still being removed; kill a candidate and drop a route before the retry. | The commit point is a single Docker object (a labelled volume); the retry rolls every slot forward to the committed placement, recreates a dead candidate on the committed image, restores nothing and removes the marker. No mixed ring. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-SIGKILL-DURING-COMMIT-CLEANUP`. |
| `RT-DEAD-CANDIDATE-AT-COMMIT` | A candidate dies, or its catalog becomes unreadable, after its route gate and before the commit. | The commit refuses; the previous generation serves again. An unreadable catalog is never read as "route not declared". | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-DEAD-CANDIDATE-AT-COMMIT`. |
| `RT-COMMIT-CLEANUP-CONFIG-CHANGE` | `SIGKILL` a maintenance rollout mid-cleanup, change the configuration with the same image, retry; then restore the configuration and retry. | The marker carries the committed specification hash; the changed configuration is refused before any record is removed and the way out is named; the committed configuration finishes the cleanup and removes the marker. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-COMMIT-CLEANUP-CONFIG-CHANGE`. |
| `RT-DOWN-CLEARS-COMMIT-MARKER` | With a pending commit marker run `down --maintenance`, then bootstrap a new image. | The marker is removed with the fleet; the bootstrap succeeds and leaves no marker. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-DOWN-CLEARS-COMMIT-MARKER`. |
| `RT-ROUTE-VANISHED-AT-COMMIT` | A protected catalog route disappears from the candidate's catalog after its route gate, before the commit; removals are not allowed. | The commit refuses; the previous generation serves again. Only a removal the catalog made with removals allowed is accepted, never for a required route. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-ROUTE-VANISHED-AT-COMMIT`. |
| `RT-COMMIT-CLEANUP-TAG-MOVED` | `SIGKILL` a maintenance rollout mid-cleanup; the image tag resolves to another digest before the retry. | The committed image ID is compared before any record is removed; the moved tag is refused with the way out named (pin the committed digest); the pinned digest finishes the cleanup, because the marker stores the specification without the image reference next to the image ID and every slot's configuration hash as committed; a candidate is checked before its record is removed. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-COMMIT-CLEANUP-TAG-MOVED`. |
| `RT-ROUTE-REMOVED-AT-COMMIT-ALLOWED` | With removals allowed, the catalog removes a protected route after the route gate of the first slot. | The removal is accepted at the commit and the route leaves the protected set; the rollout carries on through the other slots. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-ROUTE-REMOVED-AT-COMMIT-ALLOWED`. |
| `RT-NONHA-OWNER-DOWN` | A pinned non-HA version's owner is down. | Neither the served-routes gate nor the rollout is blocked; pinned versions are tracked, never required. | Automated regression: `deploy/join/versiond-router-fleet-regression_test.sh --gate RT-NONHA-OWNER-DOWN`. |
| `RT-LONG-LIVED` | Hold requests and SSE streams through each ordinary slot replacement and induced candidate rollback. | Accepted requests finish, new requests use admitted slots, and ready reserve never drops below configured minimum. | Automated: extend `make -C versiond-router test-fleet`; assert reserve continuously, not only before/after. |

## 7. PostgreSQL persistence and capacity scenarios

| ID | Setup and fault injection | Required result | Evidence / status |
| --- | --- | --- | --- |
| `PG-PROV-01` | Copy legacy PGDATA to target, write newer history only to legacy, then restart migration. Both copies retain the same system identifier. | Wrapper refuses the ambiguous histories or proves the target is at least as current; it never silently serves stale target history. | Automated deterministic regression: `deploy/join/devshard-postgres-provenance_acceptance_test.sh --gate PG-PROV-01`; real-PG corroboration required in `deploy/join/devshard-postgres-upgrade_test.sh`. |
| `PG-PROV-02` | Leave a valid-looking sibling `.migrated-from-v4` marker, replace target PGDATA with an unrelated raw PG16 cluster, and remove legacy source. | Startup fails because the marker is cryptographically/structurally bound to this PGDATA and migration identity. | Automated deterministic regression: `deploy/join/devshard-postgres-provenance_acceptance_test.sh --gate PG-PROV-02`; real-PG corroboration required in `deploy/join/devshard-postgres-upgrade_test.sh`. |
| `PGDATA-03` | Corrupt or make `pg_control`/`PG_VERSION` unreadable in source and target; repeat after a partial copy. | Entrypoint fails closed and never initializes over or serves the ambiguous directory. | Automated: `deploy/join/devshard-postgres-entrypoint_test.sh`; real filesystem cases required in `deploy/join/devshard-postgres-upgrade_test.sh`. |
| `PGDATA-04` | Fill target disk during copy/init and reboot the host before marker persistence. | No completion marker is durable for an incomplete cluster; after space recovery the retry resumes safely or demands explicit recovery. | Automated container case plus Manual host-reboot evidence. |
| `CAP-01` | For `R` replicas, `N` HA children per replica, pool size `P`, and overlap of two generations, configure one fewer than `R * (2 * N * (P + 2) + 5)` connections. | Preflight rejects before rollout and reports required/available/reserved capacity. The documented example for `R=2,N=3,P=4` requires at least 82 before external reserve. | Documentation calculation regression: `deploy/join/update-devshard-regression_test.sh --gate OPS-MAX-CONNECTIONS`; automated capacity preflight and real PostgreSQL exhaustion boundary are still required. |
| `CAP-02` | Configure exactly the required capacity plus documented operator reserve and roll all children concurrently with health/fence connections active. | No connection exhaustion, readiness flap, or route loss occurs; observed peak stays at or below the calculated budget. | Automated load/integration test and Manual production-like evidence. |

## 8. Fault-injection phase matrix

The named scenarios above cover known failures. The following matrix prevents
the same class of bug from moving to a different line or phase. Tests should
use deterministic hooks immediately before and after each durable mutation;
timing-only sleeps are not sufficient.

Phases:

| Phase | Mutable boundary |
| --- | --- |
| `P0` | deployment discovery and lock acquisition |
| `P1` | pull, storage proof, disk/capacity preflight, rollback snapshot |
| `P2` | PostgreSQL initialization, copy, marker persistence, start |
| `P3` | HA network preparation and old-upstream attachment |
| `P4` | each router slot stop/create/readiness/admission commit |
| `P5` | policy2, policy1, and public proxy cutover |
| `P6` | each versiond replica stop/create/readiness/admission commit |
| `P7` | public route-aware E2E verification |
| `P8` | old replica decommission and recovery-artifact cleanup |

Required fault matrix:

| Fault | Injection points | Required invariant | Automation |
| --- | --- | --- | --- |
| Updater `SIGTERM`/`SIGKILL` and host reboot | Before and after every `P2`–`P8` mutation. | Retry convergence, rollback fidelity, no early cleanup, storage lineage. | Automated process-kill matrix; Manual reboot at `P2`, `P4`, `P6`, `P8`. |
| Docker daemon unavailable, restart, API error, and API hang | Every Docker query and mutation in `P0`–`P8`; especially `ps`, `inspect`, `tag`, `up --wait`, and `rm`. | Fail closed and bounded; no query failure becomes “container/image absent”; retry converges. | Automated fake-Docker exhaustive failpoint walk plus representative real daemon restart. |
| Registry unavailable or mutable tag changes | Pull/snapshot in `P1`, retry after interruption in `P4`–`P6`. | No anchor loss; cached recovery works; selected digest is explicit and stable for the run. | Automated `DAY2-04` and `RT-OFFLINE-RETRY-CACHED`. |
| PostgreSQL connection refused, crash, reset, blackhole, and read-only failover | Proof in `P1`; migration in `P2`; immediately after every route gate in `P4`/`P6`; final E2E in `P7`. | No silent history switch; never success with a zero-ready route; bounded failure and safe retry. | Automated `PG-01`, `UPD-PROOF-FATAL`, `PG-07`–`PG-09`. |
| Disk full / inode exhaustion | Rollback metadata/tagging in `P1`; PGDATA copy/marker in `P2`; container state in `P4`–`P6`. | An incomplete durable step is never marked complete; old data/anchor remains usable. | Automated container-volume test; Manual filesystem corroboration. |
| DNS/network partition | Old/new router networks in `P3`; proxy-policy ingress in `P5`; remote versiond and catalog throughout `P4`–`P7`. | Dependency failure is explicit; healthy unrelated routes cannot mask a required missing route. | Automated `UPG-02`, `UPD-POLICY-BOOTSTRAP`, `RT-CATALOG-ZERO-READY`. |
| Candidate unhealthy or route-incomplete | Every candidate admission in `P4`–`P6`. | Candidate is never committed; exact prior state and ready reserve remain. | Automated router fleet and updater regression suites. |
| Continuous HTTP and long-lived/SSE traffic | Entire `P3`–`P8` sequence, including rollback. | No accepted stream is truncated; response routing headers identify only admitted generations. | Automated `UPG-05`, `RT-LONG-LIVED`; Manual external-client run. |

Every automated failpoint walk must record:

- updater/fleet exit status and elapsed time;
- service/container IDs, health, images, full effective configuration, and
  deployment/fleet labels before failure and after retry;
- ready counts for every required route sampled throughout the run;
- public HTTP status and routing headers plus completion of open streams;
- PostgreSQL identity, recovery state, migration markers, and sentinel rows;
- rollback-anchor ownership and digest.

## 9. Required commands

The acceptance report must attach output from this complete command set:

```bash
shellcheck \
  deploy/join/deployment-lock.sh \
  deploy/join/update-devshard.sh \
  deploy/join/update-devshard_test.sh \
  deploy/join/update-devshard-regression_test.sh \
  deploy/join/devshard-postgres-provenance_acceptance_test.sh \
  deploy/join/devshard-postgres-*.sh \
  deploy/join/versiond-router-fleet.sh \
  deploy/join/versiond-router-fleet_test.sh \
  deploy/join/versiond-router-fleet-regression_test.sh

deploy/join/versiond-compose-config_test.sh
deploy/join/update-devshard_test.sh
deploy/join/update-devshard-regression_test.sh --gate
deploy/join/devshard-postgres-entrypoint_test.sh
deploy/join/devshard-postgres-upgrade_test.sh
deploy/join/devshard-postgres-provenance_acceptance_test.sh --gate
make -C versiond-router test-render
make -C versiond-router test-fleet
deploy/join/versiond-router-fleet-regression_test.sh --gate
```

The report must list the candidate commit, Docker/Compose/PostgreSQL versions,
the topology and environment used, start/end timestamps, and links to manual
evidence. A generic “CI is green” statement does not replace this report.
