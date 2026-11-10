# Devshard HA lifecycle test plan

Prepare the deployment using the [HA setup guide](../../docs/devshard-host-ha-setup.md).
Operator commands for adding, stopping, replacing and removing versiond are in
[section 2.5](../../docs/devshard-host-ha-setup.md#25-operating-versiond-members).

## Scope and preparation

These cases check the v5 versiond pool and router fleet. The purpose is to
verify that an operator can add, replace and remove members while preserving
accepted inference and committed session state.

Use the release checkout with `update-devshard.sh` from
[#1611](https://github.com/gonka-ai/gonka/pull/1611), the independent versiond-router
fleet from [#1610](https://github.com/gonka-ai/gonka/pull/1610), and its public
`proxy-router` and private nginx policy workers from
[#1609](https://github.com/gonka-ai/gonka/pull/1609). Set the tested
`VERSIOND_IMAGE`, `VERSIOND_ROUTER_IMAGE`, `PROXY_ROUTER_IMAGE` and
`PROXY_POLICY_IMAGE` in `config.env`.

The updater's `--check` takes the deployment lock and writes transient
storage challenges. Keep `UPDATE_SKIP_POSTGRES_PROBE` and
`UPDATE_ACCEPT_DATABASE_CHANGE` disabled. An explicit 404 from a legacy storage
proof endpoint is skipped; preflight success does not verify that member's database.
The policy-worker case marked `@continuity_requirement` is a release gate: the
current updater uses ordinary Compose replacement without the public tier's
runtime drain sequence, so its success alone does not establish continuity.

Run the core scenarios with multiple local replicas and with replicas spread across machines.
Repeat addition/removal with three or four members and a mixed local/remote
pool. All HA members use the same participant keys and writable PostgreSQL;
each has its own local data directory. Keep PostgreSQL, ingress and chain
services outside the machine being stopped in the application-host loss test.

Use the actual RC protocol name, images and approved devshardd artifact. Record
the configured announce/drain/stop deadlines. Keep enough healthy survivors
for the configured reserve and load; Docker does not prevent stopping the last
member. Legacy SQLite owners are not candidates for these HA removal tests.

Start real inference through the public endpoint. Identify the serving member
from correlated request/session logs; a successful `/healthz` request does not
prove session failover. Use finite SSE requests that finish within the drain
budget, except in the explicit timeout case. The Gherkin below is a manual
acceptance specification, not an implemented Cucumber test suite.

## Installation and upgrade checks

Run fresh-install and v4 migration cases on separate deployments. Repeat the
fresh-install case with the external-PG override from §2.2: no local PostgreSQL
container is created or required by the replicas. For migration,
record the old PostgreSQL container, source volume, PostgreSQL `system_identifier` and a
committed session before changing the deployment. Keep the source and backup
until the upgraded public route has passed a real inference check.

```gherkin
Feature: Install and upgrade an HA host
  Scenario: Admit a fresh HA installation only when its required routes are ready
    Given an empty installation with explicit PostgreSQL storage for every HA member
    When I install the RC with one required protocol version deliberately unready
    Then a healthy proxy or an unrelated healthy version cannot satisfy admission
    And the router's admin /readyz?version=<required-version> returns 503
    And the required version's backend has no ready upstreams
    When that version becomes ready and I finish admission
    Then the public /devshard/<required-version>/healthz path used by the gateway returns 200
    And the gateway completes its height seed through the public endpoint
    And inference through the public endpoint succeeds with correct accounting
    And every local member returns a nonempty identity and generation targets from /internal/storage-identity
    And the updater --check succeeds with database probes enabled

  Scenario: Detect an incompatible router image and catalog configuration
    Given the versiond-router fleet on a separate test deployment
    When I select the legacy nginx router image for a fleet slot
    Then the slot's admin readiness check fails even if /healthz responds
    When I select the catalog-capable HAProxy image with an explicitly empty catalog URL
    Then a newly approved protocol absent from the bootstrap list is not admitted
    When I supply that image and the filtered catalog URL together
    Then I verify each required version, public admission and real inference

  Scenario: Reject unsafe HA child storage without an updater
    Given GONKA_HA=true and a version that is not pinned to a legacy owner
    When I start its v5 child with auto, sqlite or hybrid storage, or with PGHOST missing
    Then it cannot become an HA serving child
    When I use explicit postgres storage with an unreachable or read-only database
    Then it cannot become ready or fall back to SQLite

  Scenario: Reject unsafe HA storage before changing a running deployment
    Given a healthy HA deployment serving a recorded escrow
    When I run the updater preflight with hybrid storage or a missing PGHOST
    Then it fails before replacing or stopping a serving container
    When I repeat with an unreachable or read-only PostgreSQL target
    Then it fails the write-capable storage check
    And the existing deployment continues serving the recorded escrow

  Scenario: Reject a different writable database during an update
    Given healthy HA members sharing one PostgreSQL database
    And every running member exposes a nonempty per-generation storage proof
    When I point the proposed deployment at an independent writable clone
    And I run the updater preflight
    Then matching copied database identifiers do not satisfy the live storage challenge
    And the update fails before replacing or stopping a serving container
    When one running member's storage proof instead times out or returns a non-404 error
    Then another member's valid proof cannot make the preflight pass

  Scenario: Preserve the bundled v4 PostgreSQL database during migration
    Given a v4 installation with a recorded session in its PostgreSQL volume
    When I run the migration preflight and recreate PostgreSQL in place
    Then the persistent PGDATA preserves the PostgreSQL system_identifier and committed data
    And the source volume remains available for recovery
    When I finish the upgrade and later recreate the PostgreSQL container
    Then the recorded escrow still works with correct committed state and accounting
    And the retained v4 artifact recovers its v4 snapshot and post-snapshot journal
    When I repeat the migration preflight on a copy with insufficient target space
    Then it fails before PostgreSQL is recreated and the source remains unchanged
    When I try to start the copied cluster with a different PostgreSQL major or a glibc-based image
    Then the entrypoint refuses startup without replacing the cluster
    When I repeat on a separate copy with existing installation evidence but neither persistent PGDATA nor a legacy source
    Then startup refuses to initialize an empty database
    And DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true cannot override an existing .pg-bound marker
    When I repeat with the recorded source detached and an interrupted migration copy
    And I attach that exact source through the recovery overlay and restart migration
    Then incomplete PGDATA is never served
    And the recovered cluster has the recorded system_identifier and committed session
    And the original source remains unchanged

  Scenario: Keep v4 sessions separate while adding the v5 protocol
    Given a v4 installation with a recorded v4 escrow and its approved v4 artifact
    And that v4 artifact supports --print-storage-mode and reports postgres in the HA environment
    And its verified flat binary cache matches the unchanged approved name, URL and SHA256
    When I preserve PostgreSQL and the local versiond data and run the fleet updater in its maintenance window
    And I admit the separately approved v5 protocol after the supervisor update
    Then the verified flat install is promoted to the version/archive-SHA256 cache layout
    And the protocol data directory and recorded escrow still work through the v4 route
    And a new v5 escrow works through the v5 route with correct committed state and accounting
    And the v5 child skips stored sessions belonging to v4
    When I offer a binary reporting protocol v5 for the unchanged v4 slot
    Then versiond rejects the protocol mismatch before replacing its serving v4 child

  Scenario: Stop a compatible versiond image update at a failed candidate
    Given a healthy HA deployment with enough surviving capacity
    When the updater replaces a replica with an image that never becomes healthy
    Then it restores that replica's previous image and exits with an error
    And it does not replace the remaining replicas after that failure
    And survivors serve the recorded escrow with correct committed state
    When I fix the candidate and rerun the updater
    Then the update completes and the public route passes real inference
    And a second unchanged run does not replace healthy containers

  Scenario: Cut over a v4-only installation using the fleet updater
    Given multiple pre-v5 supervisors, the original nginx router and recorded PostgreSQL state
    And the retained approved v4 artifact supports the new supervisor's HA storage contract
    And the filtered oracle and required bootstrap routes remain v4-only for the cutover
    When I run the updater with the complete ordered Compose file list
    Then legacy members are reachable from the new fleet before public admission
    And the old router is removed only after public admission succeeds
    And the recorded session works after the database and public-proxy maintenance window
    When I apply the extended oracle filter and wait for the approved v5 route
    Then both retained v4 and new v5 inference succeed
    When a later compatible update is killed during a replica replacement and rerun
    Then it converges using the same topology without replacing healthy unchanged members

  Scenario: Warm a compatible same-name artifact before switching the serving child
    Given a v5 session with a snapshot and a later committed journal tail in shared PostgreSQL
    And a wire-compatible candidate for the unchanged approved protocol exposes recovery_complete
    When versiond starts the overlapping candidate with deliberately delayed recovery
    Then a candidate ready response with recovery_complete=false does not switch the route
    And the predecessor continues serving the recorded escrow
    When recovery completes and the candidate takes over
    Then the snapshot and tail preserve the latest nonce, committed state and accounting
    And the recorded height floor is preserved and the next stamped heartbeat applies
    And sealed inference lookup and subsequent inference work
    And recovery failures are checked separately from recovery_complete
    When I repeat with recovery held beyond VERSIOND_RECOVERY_TIMEOUT
    Then the candidate is stopped and the predecessor continues serving

  Scenario: Preserve readiness compatibility during recovery
    Given a cold restart with committed sessions and a deliberately delayed recovery backlog
    When the child has ready storage and chain connectivity but recovery_complete=false
    Then its admin /ready returns 200 and the supervisor can publish it without waiting for the backlog
    And a request for a recorded escrow recovers its committed state on demand
    When a compatible overlap candidate omits recovery_complete as older binaries do
    Then versiond uses the readiness status and skips the warm-cutover wait
    And a 503 response never becomes ready because its body says recovery_complete=true
```

## Acceptance scenarios

```gherkin
Feature: Versiond and router HA lifecycle
  Background:
    Given the RC version is admitted through the public endpoint and its configured routers
    And every participating HA versiond uses the same writable PostgreSQL
    And a funded escrow has a recorded successful inference, nonce and cost
    And healthy survivors have enough capacity to serve the test load

  Scenario: Gracefully stop a versiond without interrupting accepted inference
    This checks the operator's normal evacuation path, not crash recovery.
    Given a long finite SSE request is being served by the chosen versiond
    When I stop only that versiond with its configured Compose stop grace
    Then it becomes unready and routers withdraw it before admission closes
    And the accepted SSE completes within the drain budget
    And requests after withdrawal use a surviving member
    And work on the same escrow continues with correct results and accounting
    And the stopped versiond leaves no running child processes

  Scenario: Enforce the shutdown deadline when work cannot finish
    This checks that a stuck stream cannot block maintenance indefinitely.
    Given an accepted request remains active beyond the configured drain budget
    When I gracefully stop its versiond
    Then remaining work is terminated within the configured shutdown bounds
    And logs distinguish deadline expiry from a completed graceful drain
    And subsequent work on the escrow recovers without losing committed state

  Scenario: Add a versiond to the pool
    This checks readiness-gated admission and continued access to existing sessions.
    Given inference is running through the existing members
    When I start an additional member with its own data directory
    And I apply the appropriate DNS or explicit-file membership procedure
    Then the new member receives no requests before its version is ready
    And it becomes usable after fresh health checks
    And the existing escrow works through the expanded pool with correct accounting
    And any explicit-file maintenance interruption is recorded separately

  Scenario: Replace a versiond at an existing endpoint
    This checks that a replacement cannot inherit its predecessor's admission.
    Given the replacement preserves the member endpoint and shared storage
    When I gracefully stop the old member and observe its withdrawal
    And I start its replacement with a deliberately delayed readiness response
    Then survivors serve new work while the replacement is unready
    And the replacement joins only after fresh per-version health checks
    And I verify inference before replacing another member

  Scenario: Permanently remove a versiond
    This checks that evacuation survives a later deployment or host restart.
    Given the selected member owns no legacy SQLite versions
    When I gracefully stop it and let accepted inference finish
    And I remove it from the desired deployment and, if used, the endpoint file
    And I apply explicit-file membership maintenance when required
    Then the remaining pool serves the recorded escrow
    And the removed member is not recreated by the next deployment
    And shared session data remains intact

  Scenario: Recover after an abrupt versiond host failure
    This distinguishes crash recovery from guaranteed graceful completion.
    Given an SSE request is served by a remote application host
    When that host disappears without graceful shutdown
    Then its active stream may fail
    And the routers stop sending new requests to it after failure detection
    And subsequent work on the same escrow succeeds on a survivor
    And committed state and charges are preserved without unsafe POST replay
    When the host returns
    Then it rejoins only after fresh per-version health checks

  Scenario: Withdraw a member that loses its PostgreSQL connection
    This checks live storage readiness after successful startup.
    Given the selected member is ready and shares PostgreSQL with its survivors
    When I exhaust its application connection pool while PostgreSQL stays writable
    Then pool saturation alone does not make storage unready
    When I block only that member's access to PostgreSQL
    Then its affected versions become unready and routers withdraw them
    And it does not fall back to local SQLite or accept new HA work
    And survivors continue the recorded escrow with correct committed state
    When I restore access and the affected child restarts if required
    Then the member rejoins only after fresh storage and per-version health checks

  Scenario: Roll the inner router fleet under inference load
    This checks that replacing routers preserves accepted work and serving reserve.
    Given finite SSE and POST requests are running through the inner fleet
    When I apply a compatible router image update without changing membership
    Then router slots are replaced one at a time
    And each candidate is admitted before the next serving slot is replaced
    And accepted requests finish within the configured graceful bounds
    And inference results and charges remain correct
    And a failed candidate does not remove the remaining serving reserve

  Scenario: Lose one inner router
    This checks router redundancy rather than versiond redundancy.
    Given traffic is passing through multiple admitted inner routers
    When I abruptly stop an inner router that carries test traffic
    Then requests on its existing connections may fail
    And new requests reach surviving routers after failure detection
    And the same escrow remains usable with correct committed state
    When I restore the slot with the fleet tooling
    Then it is admitted only after fresh health checks

  @continuity_requirement
  Scenario: Replace a public policy worker without interrupting accepted inference
    Given the public HAProxy tier with two healthy policy workers and admitted inner routers
    And a finite SSE request is being served through the chosen policy worker
    When the updater applies a compatible policy image change
    Then the accepted request completes within the configured graceful bounds
    And new requests use an admitted policy worker throughout the replacement
    And a replacement receives traffic only after fresh health checks
    And final results and accounting remain correct

  Scenario: Change the explicit multi-host endpoint list
    This checks that every router uses one consistent membership generation.
    Given the fleet uses a recorded endpoint file
    When I change the list to add or remove a remote member
    Then editing the source file alone leaves running membership unchanged
    When I perform the acknowledged membership maintenance rollout
    Then every serving router uses the new complete endpoint list
    And inference resumes after the recorded maintenance window
    When I try a maintenance rollout with an invalid endpoint file
    Then it fails before replacing accepted membership
    And the previously admitted pool continues serving inference

  Scenario: Admit a catalog addition independently of other versions
    Given an accepted v4 route and a newly approved protocol absent from the bootstrap list
    And that new protocol is below its ready reserve
    When the router reads the updated filtered catalog
    Then v4 continues serving while the new route remains unpublished
    And a member ready only for v4 receives no requests for the new protocol
    When the new protocol reaches its configured ready reserve
    Then it is admitted and real inference succeeds
    When the catalog becomes unreachable and the router under test restarts with its saved state
    Then accepted routes still serve through the existing running versiond children
    And catalog degradation is distinguishable from route readiness

  Scenario: Withdraw a child that loses its PostgreSQL session fence
    Given a ready child with writable PostgreSQL and accepted work
    When I terminate only its dedicated PostgreSQL fence connection
    Then it becomes unready and cannot silently continue with an unfenced storage session
    And the affected child exits and versiond starts a replacement generation
    And the replacement receives traffic only after fresh storage and per-version readiness checks
    And interrupted work is recorded as a failure rather than successful graceful completion
    And a survivor continues the escrow with committed state and accounting intact

  Scenario: Recover validation ownership after a member crashes before durable submission
    Given a validation result marked submitted in shared PostgreSQL
    And its transaction exists only in that member's volatile mempool
    And the inference remains eligible for validation through the configured TTL and retry interval
    When that member crashes before the validation is committed
    Then a survivor reclaims the stale lease and retries submission after expiry
    And the validation is eventually committed without duplicate accounting
```

## Execution and evidence

For graceful stop, use `docker compose stop <service>` on the member's own
machine with the complete Compose file list. Keep its configured stop grace;
`docker kill` or a short `--timeout` is the separate failure case. Replace only
that service and verify per-version admission before moving to the next.
Permanent removal also changes desired replica settings; stopping alone is
temporary. Explicit endpoint changes require the fleet's maintenance procedure.

Attach the topology, RC image/artifact identifiers, serving-member evidence,
fault/withdrawal/recovery timestamps, SSE completion or expected interruption,
escrow IDs and final results/costs. Distinguish a graceful operation from a
crash or acknowledged membership outage. A passing health check alone is not
a passing inference-continuity test.

For failed installation or update checks, also attach the command exit status,
storage-proof result without credentials, and container identities before and
after failure. Verify the resulting state; an expected error message alone is
not evidence that the serving deployment or database was preserved.
