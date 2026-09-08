Feature: Height-sync host claims
  Hosts sign response-leg Anchors; the gateway is a courier and never mints a
  height from its own chain read. Default D (DeltaBlocks) is 2. Dispute / Strong
  slash is deferred; this release detects lag, future tips, and fabricated hashes
  via logs, marks, and Prometheus.

  Automated pins:
    In-process (httptest): TestHeightSync_E2E_HostLowerHeightAutoAlignsAndLogs,
      TestHeightSync_E2E_HostFutureHeightBeyondDDetected,
      TestHeightSync_E2E_HostFabricatedHashInsideDReconciles
    testenv citest (Docker, testenv-only Latest() overlay on the solo identity):
      TestContainerE2E_HeightSync_HostLowerHeightAutoAligns,
      TestContainerE2E_HeightSync_HostFutureHeightBeyondD,
      TestContainerE2E_HeightSync_HostFabricatedHashInsideD

  Run:
    go test ./devshard/testenv/scenarios/ -run 'HostLowerHeight|HostFutureHeight|HostFabricated' -count=1
    make -C devshard/testenv build-devshardd citest-height-sync

  Scenario: Host reports a lower height than the roster
    Given an escrow with honest hosts at height H
    And one host whose oracle tip is much lower than H
    When the gateway has already aligned on the higher tip
    And chat is sent to the lagging host
    Then inferences complete without error
    And the floor F is the higher host-signed height
    And the lagging host lifts to F in the log and reports CATCHING_UP
    And operators see negative delta, height_spread, and host_height_lag
    And a Diff stamp below F after alignment is INVALID(height_regression)
      # unit: TestLogPlane_AckBelowFloorRejectedAndLiftAccepted (not this e2e)

  Scenario: Host reports a future height with unknown hash beyond D
    Given D is 2
    And one host claims H+Δ with Δ > D and a hash not in the honest oracle
    When chat continues
    Then chat still returns 200
    And trust_level is untrusted_peer
    And L5a records MARK(l5a_admission) when that height is bound on a heartbeat or ack
    And Strong slash is not required in this release

  Scenario: Host reports a slightly future fabricated hash
    Given one host claims H+1 (Δ ≤ D) with a fabricated block hash
    When honest followers later reach height H+1 and see the canonical hash
    Then hosts log warn "heightsync: untrusted peer tip disagrees with oracle at reconciled height"
    And L6 DEFERRED_FAIL is recorded when Oracle.At(H) is available
      # unit: TestLogPlane_FutureDatedStampDeferredFail; transport warn is the live signal
    And chat was never blocked
