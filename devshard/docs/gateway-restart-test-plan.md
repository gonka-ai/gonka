# Gateway restart test plan

Cases where the gateway process restarts while the chain or a host is not in its normal state. Each case says what has to hold, what was measured last, and which test checks it.

---

## Gateway restart while a participant is absent on chain

**Setup.** Escrow rotation on. One participant of the active escrows' group is absent on chain (the Participant query answers NotFound). The gateway restarts during the absence.

**What happens at start-up.** `buildGatewayRuntimes` loads the escrow as usual. `openHostClients` gives the absent participant a `lazyHostClient` (`bridge.ErrParticipantNotFound` opens the same way as an unresolved URL), the chat picker skips it through `ChatBudgetWait`, and the escrow serves on the rest of its group. The budget poll and the catalog probe retry the lookup once `transport.DefaultLazyRetry` passes, so the host rejoins without another restart. This came to `devshard/v7` with the v6 merge 6c02bdf5c (#2080); before it, start-up parked the escrow until the participant returned.

**Expected.**

- [ ] Only the requests in flight at the restart fail. The escrow keeps serving on the other hosts while the participant is absent.
- [ ] The absent host is taken back without another restart once the chain lists it again.
- [ ] A running gateway (no restart) rides out the same absence.

**Last measured** (devshard-gateway `mainnet-v0.2.16-v6.0.0-rc1`, before #2080, load harness, 8 rps): with the participant absent for 5 min and a restart 16 s in, the gateway started with 0 runtimes and answered every request 502 "no devshard runtimes available" from the restart until 18 s after the participant returned. A running gateway rode out a 45 s absence (`faults-gateway-restart`) with 0% errors. The load scenario below has not been re-run on `devshard/v7` yet.

**Automated tests.**

```bash
# Gateway level: after a restart with one participant absent, the escrow is loaded, not skipped, chosen for the model,
# and only the absent host is blocked for chat. Fails on devshard/v7 before 6c02bdf5c (escrow skipped as participant_missing).
cd devshard && go test ./cmd/devshardctl/ -run 'TestGatewayRestartKeepsServingAnEscrowWhileAParticipantIsAbsent|TestAHostWhoseURLIsUnresolvedIsThrottled' -count=1

# Session level: the absent host opens lazy, refuses calls, and is rebuilt after the retry window.
cd devshard && go test ./user/ -run 'TestNewHTTPSession_ParticipantNotFoundOpensWithLazyHost|TestLazyHostClient_BudgetPollRetriesAfterWindow' -count=1
```

Load scenario `testenv/loadtest/scenarios/faults-gateway-restart-participant-absent.yaml`: 10 min at 8 rps, versiond-5 absent from 3m to 8m, gateway restart at 3m15s, `error_rate: 0.05`. A gateway that stops serving for the absence fails about half of the run. The loader on `devshard/v7` does not read `events` yet (the loader with events and cases was #1989, closed), so today the run is plain load.

`faults-gateway-restart.yaml` also restarts the gateway during an absence, but only 10 s into a 45 s one and with `error_rate: 0.12` for the whole run, so it does not catch this.
