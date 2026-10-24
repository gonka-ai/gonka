# Gateway restart test plan

Cases where the gateway process restarts while the chain or a host is not in its normal state. Each case says what has to hold, what was measured last, and which test checks it.

---

## Gateway restart while a participant is absent on chain

**Setup.** Escrow rotation on. One participant of the active escrows' group is absent on chain (the Participant query answers NotFound). The gateway restarts during the absence.

**What happens at start-up.** `buildGatewayRuntimes` skips every active escrow whose group holds the missing participant (`bridge.ErrParticipantNotFound`, "keeping it active") and marks it as waiting for the participant (`awaitParticipants`). `retryAwaitingParticipants` starts it again once the participant record is back. A waiting escrow no longer counts toward the rotation target, but nothing creates a replacement before the next epoch switch or a depletion.

**Expected.**

- [ ] Only the requests in flight at the restart fail. The model keeps serving while the participant is absent, through a replacement escrow if every active escrow is waiting.
- [ ] The skipped escrow stays active and starts again without another restart once the participant returns.
- [ ] A running gateway (no restart) rides out the same absence.

**Last measured** (devshard-gateway `mainnet-v0.2.16-v6.0.0-rc1`, load harness, 8 rps): with the participant absent for 5 min and a restart 15 s in, the gateway started with 0 runtimes and answered every request 502 "no devshard runtimes available" for the whole absence. The runtimes came back 18 s after the participant did, and no replacement escrow was created. A running gateway rode out the same absence with 0% errors. The second and the third points hold; the first does not.

**Automated tests.**

```bash
# Unit: a replacement is created while the only active escrow waits for an absent participant.
# Skipped while the issue is open; DEVSHARD_KNOWN_ISSUES=1 runs it (fails today).
cd devshard && DEVSHARD_KNOWN_ISSUES=1 go test ./cmd/devshardctl/ -run TestGatewayReplacesAnEscrowWaitingForAnAbsentParticipant -count=1

# Unit: the waiting escrow takes no rotation target, starts once the record appears, stops waiting once the escrow is gone.
cd devshard && go test ./cmd/devshardctl/ -run TestAnEscrowWaitingForAParticipant -count=1
```

Load scenario `testenv/loadtest/scenarios/faults-gateway-restart-participant-absent.yaml`: 10 min at 8 rps, versiond-5 absent from 3m to 8m, gateway restart at 3m15s, `error_rate: 0.05`. A gateway that stops serving for the absence fails about half of the run. It needs the scenario loader with events and cases (#1989); without it the events are dropped and the run is plain load.

`faults-gateway-restart.yaml` also restarts the gateway during an absence, but only 10 s into a 45 s one and with `error_rate: 0.12` for the whole run, so it does not catch this.
