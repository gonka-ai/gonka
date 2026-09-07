# Devshard HA lifecycle test plan

Prepare the deployment using the [HA setup guide](devshard-host-ha-setup.md).
Operator commands for adding, stopping, replacing and removing versiond are in
[section 2.5](devshard-host-ha-setup.md#25-operating-versiond-members).

## Scope and preparation

These cases check the v5 versiond pool and router fleet. The purpose is to
verify that an operator can add, replace and remove members while preserving
accepted inference and committed session state. New edge-api HA is outside
this release's scope.

Run the core scenarios on two local replicas and on one replica per machine.
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

## Acceptance scenarios

```gherkin
Feature: Versiond and router HA lifecycle
  Background:
    Given the RC version is admitted through the public router and inner fleet
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
