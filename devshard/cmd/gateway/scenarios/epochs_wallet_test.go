package scenarios

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/funding"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/internal/logkey"
)

func rowsOfRole(harness *gatewayHarness, role string, active bool) int {
	count := 0
	for _, row := range harness.rows() {
		if row.RotationRole == role && row.Active == active {
			count++
		}
	}
	return count
}

func createdWithRole(harness *gatewayHarness, role string) []string {
	lines := createLinesOf(harness.loggedEntries())
	var escrowIDs []string
	for _, created := range harness.chain.createsOf(testModelID) {
		if escrowID := formatEscrowID(created.escrowID); lines[escrowID].role == role {
			escrowIDs = append(escrowIDs, escrowID)
		}
	}
	return escrowIDs
}

func createsAfter(harness *gatewayHarness, moment time.Time) int {
	count := 0
	for _, created := range harness.chain.createsOf(testModelID) {
		if !created.at.Before(moment) {
			count++
		}
	}
	return count
}

// Test flow:
//  1. Boot with two starved regulars of 20 000 and let the first tick fund the two full escrows the guarantee wants.
//  2. Move into the bridge window and advance one minute.
//  3. Assert one temp labelled 8 serves, every regular left service after the full fill, the planner created nothing inside the window, and a full-context request is answered 200 by the temp (`settle deadline`, `unsettled budget` at every step).
func TestTheBridgeWindowOpensOverAStarvedFleet(t *testing.T) {
	spec := plannerSpec()
	spec.seeded = repeatSeeded(2, 20_000, regularRole)
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		expectThat{label: "precondition: the guarantee funded", verify: func(harness *gatewayHarness) error {
			if guards := harness.createdWithReason(testModelID, string(funding.ReasonGuard)); guards != 2 {
				return fmt.Errorf("%d guard creates, want 2", guards)
			}
			return nil
		}},
		moveChain{move: func(blockchain *fakeChain) { blockchain.moveToHeight(1900) }},
		advance{by: time.Minute},
		sendRequests{promptBytes: 30_000, maxTokens: 64, count: 1, sequential: true},
		expectThat{label: "one temp serves the window and every regular left", verify: func(harness *gatewayHarness) error {
			if temps := rowsOfRole(harness, tempRole, true); temps != 1 {
				return fmt.Errorf("%d serving temps, want 1", temps)
			}
			if regulars := rowsOfRole(harness, regularRole, true); regulars != 0 {
				return fmt.Errorf("%d regulars still serving after a full fill, want 0", regulars)
			}
			if creates := len(harness.chain.createsOf(testModelID)); creates != 3 {
				return fmt.Errorf("%d creates, want 3: two guards before the window and its temp", creates)
			}
			return everyAnswer(1, http.StatusOK)(harness)
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with a guarantee of one, a regular and a full standby.
//  2. Move into the bridge window and advance one minute: the bridge funds a temp and retires the regular and the standby.
//  3. Assert no standby was created inside the window.
//  4. Start PoC, advance a tick, reach set_new_validators with requests unblocked, and advance one minute.
//  5. Assert exactly one standby was created, after the switch, with the reason standby, and serves as a reserve labelled 8; assert a regular was created after the switch too, and the temp left service once a regular of the new epoch served.
func TestAStandbyShortfallInsideTheWindowIsRepairedAfterIt(t *testing.T) {
	spec := plannerSpec()
	spec.model.reserveCount, spec.model.fullContextSlots = 1, 1
	spec.seeded = []seededEscrow{{amount: 1_000_000, role: regularRole}, {amount: 1_000_000, role: escrow.RoleReserve}}
	var switchedAt time.Time
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		moveChain{move: func(blockchain *fakeChain) { blockchain.moveToHeight(1900) }},
		advance{by: time.Minute},
		expectThat{label: "no standby inside the window", verify: func(harness *gatewayHarness) error {
			if standbys := harness.createdWithReason(testModelID, string(funding.ReasonStandby)); standbys != 0 || rowsOfRole(harness, escrow.RoleReserve, true) != 0 {
				return fmt.Errorf("%d standby creates, %d serving standbys, want none inside the window", standbys, rowsOfRole(harness, escrow.RoleReserve, true))
			}
			return nil
		}},
		moveChain{move: func(blockchain *fakeChain) { blockchain.startPoC() }},
		advance{by: escrow.TickInterval},
		moveChain{move: func(blockchain *fakeChain) {
			switchedAt = time.Now()
			blockchain.setNewValidators(3000, 3100)
		}},
		advance{by: time.Minute},
		expectThat{label: "repaired after the switch", verify: func(harness *gatewayHarness) error {
			if standbys := harness.createdWithReason(testModelID, string(funding.ReasonStandby)); standbys != 1 || createsAfter(harness, switchedAt) < 2 {
				return fmt.Errorf("%d standby creates, %d creates after the switch, want the standby and a regular after it", standbys, createsAfter(harness, switchedAt))
			}
			lines := createLinesOf(harness.loggedEntries())
			for _, created := range harness.chain.createsOf(testModelID) {
				escrowID := formatEscrowID(created.escrowID)
				if lines[escrowID].reason != string(funding.ReasonStandby) {
					continue
				}
				if row, stored := rowOf(harness, escrowID); created.at.Before(switchedAt) || !stored || !row.Active || row.RotationRole != escrow.RoleReserve || row.RotationEpoch != 8 {
					return fmt.Errorf("standby %s created %s after the switch, row %+v, want created after the switch and serving as a reserve labelled 8", escrowID, created.at.Sub(switchedAt), row)
				}
			}
			if temps := rowsOfRole(harness, tempRole, true); temps != 0 {
				return fmt.Errorf("%d temps still serving, want none once a regular of epoch 8 serves", temps)
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Start PoC and hold it for forty-five minutes before set_new_validators arrives: the bridge funds a temp of chain epoch 7 under label 8 and retires the seeded escrow.
//  2. Assert the seeded escrow settled and the temp serves with chain epoch 7 under label 8.
//  3. Make the chain refuse every create, so no regular of epoch 8 can retire the temp, reach set_new_validators and advance one minute; assert the temp still serves.
//  4. Move inside the settle margin of the switch into epoch 9 and advance three minutes.
//  5. Assert the temp settled while the effective epoch is still 8, then cross the switch into epoch 9 (`settle deadline` at every step).
func TestALateSetNewValidatorsLosesNoEscrow(t *testing.T) {
	spec := plannerSpec()
	temp := func(harness *gatewayHarness) (string, error) {
		temps := createdWithRole(harness, tempRole)
		if len(temps) != 1 {
			return "", fmt.Errorf("createdWithRole(temp) = %v, want the one temp the bridge funded", temps)
		}
		return temps[0], nil
	}
	spec.steps = slices.Concat([]harnessStep{
		advance{by: 30 * time.Second},
		moveChain{move: func(blockchain *fakeChain) { blockchain.startPoC() }},
		advance{by: 45 * time.Minute},
		seededEscrowSettled("the bridge retired the seeded escrow"),
		expectThat{label: "precondition: the temp of chain epoch 7 under label 8 serves", verify: func(harness *gatewayHarness) error {
			escrowID, err := temp(harness)
			if err != nil {
				return err
			}
			if row, stored := rowOf(harness, escrowID); !stored || !row.Active || row.ChainEpoch != 7 || row.RotationEpoch != 8 {
				return fmt.Errorf("rowOf(%s) = %+v, want serving with chain epoch 7 under label 8", escrowID, row)
			}
			return nil
		}},
		moveChain{move: func(blockchain *fakeChain) {
			blockchain.rejectCreates(true)
			blockchain.setNewValidators(4000, 4100)
		}},
		advance{by: time.Minute},
		expectThat{label: "precondition: no regular of epoch 8 retired the temp", verify: func(harness *gatewayHarness) error {
			escrowID, err := temp(harness)
			if err != nil {
				return err
			}
			if row, stored := rowOf(harness, escrowID); !stored || !row.Active {
				return fmt.Errorf("rowOf(%s) = %+v, want still serving after the switch", escrowID, row)
			}
			return nil
		}},
		moveChain{move: func(blockchain *fakeChain) { blockchain.moveToHeight(3600) }},
		advance{by: 3 * time.Minute},
		expectThat{label: "the temp settled inside its last epoch", verify: func(harness *gatewayHarness) error {
			escrowID, err := temp(harness)
			if err != nil {
				return err
			}
			record, _ := harness.chain.escrowRecord(parseEscrowID(escrowID))
			if effective := harness.chain.snapshotEpoch().effective; !record.settled || effective != 8 {
				return fmt.Errorf("escrowRecord(%s).settled = %v at effective epoch %d, want settled while it is 8", escrowID, record.settled, effective)
			}
			return nil
		}},
	}, crossTheNextSwitch())
	runSteps(t, spec)
}

// Test flow:
//  1. Before boot, turn confirmation PoC on; seed one starved escrow and a reserve count of one, so guard and standby are short.
//  2. Advance two minutes and assert nothing was created and the escrow still serves.
//  3. Turn confirmation PoC off, advance one minute, and assert the guarantee and the standby were funded with planner reasons (`create reason` at every step).
func TestConfirmationPoCBlocksEveryPlannerCreate(t *testing.T) {
	spec := plannerSpec()
	spec.model.reserveCount = 1
	spec.seeded = repeatSeeded(1, 20_000, regularRole)
	spec.steps = []harnessStep{
		advance{by: 2 * time.Minute},
		expectThat{label: "nothing while blocked", verify: func(harness *gatewayHarness) error {
			if creates := harness.chain.createsOf(testModelID); len(creates) != 0 {
				return fmt.Errorf("%d creates while confirmation PoC blocked requests, want 0", len(creates))
			}
			return seededServing(harness)
		}},
		moveChain{move: func(blockchain *fakeChain) { blockchain.setConfirmationPoC(false) }},
		advance{by: time.Minute},
		expectThat{label: "funded once unblocked", verify: func(harness *gatewayHarness) error {
			if guards, standbys := harness.createdWithReason(testModelID, string(funding.ReasonGuard)), harness.createdWithReason(testModelID, string(funding.ReasonStandby)); guards == 0 || standbys == 0 {
				return fmt.Errorf("%d guard and %d standby creates after unblocking, want both", guards, standbys)
			}
			return nil
		}},
	}
	runStepsWith(t, spec, func(blockchain *fakeChain) { blockchain.setConfirmationPoC(true) })
}

// Test flow:
//  1. Table-driven: boot with one starved escrow, so the guarantee is short, either inside PoC (latest 8, effective 7) with the switch 450 blocks ahead, outside the bridge window, or before PoC with the switch 200 blocks ahead, inside the bridge window with requests open.
//  2. Assert the snapshot shows exactly the one gate of the case: requests blocked, or the bridge window.
//  3. Send three small requests and advance two minutes; assert the planner created nothing (the bridge's own temp is not the planner's).
//  4. Reach set_new_validators, advance thirty seconds, and assert the guarantee was funded.
func TestMoneyShortDuringPoCCreatesNothing(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		epoch       chainEpoch
		blocked     bool
		toTheSwitch []harnessStep
	}{
		{
			name:        "requests blocked outside the bridge window",
			epoch:       chainEpoch{latest: 8, effective: 7, blockHeight: 2050, pocStart: 2000, setNewValidators: 2500, phase: chain.EpochPhasePoCGenerate},
			blocked:     true,
			toTheSwitch: []harnessStep{moveChain{move: func(blockchain *fakeChain) { blockchain.setNewValidators(3000, 3100) }}},
		},
		{
			name:  "inside the bridge window with requests open",
			epoch: chainEpoch{latest: 7, effective: 7, blockHeight: 1900, pocStart: 2000, setNewValidators: 2100, phase: chain.EpochPhaseInference},
			toTheSwitch: []harnessStep{
				moveChain{move: func(blockchain *fakeChain) { blockchain.startPoC() }},
				advance{by: escrow.TickInterval},
				moveChain{move: func(blockchain *fakeChain) { blockchain.setNewValidators(3000, 3100) }},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			spec := plannerSpec()
			spec.epoch = testCase.epoch
			spec.seeded = repeatSeeded(1, 20_000, regularRole)
			spec.steps = slices.Concat([]harnessStep{
				expectThat{label: "precondition: exactly the case's gate holds", verify: func(harness *gatewayHarness) error {
					snapshot := harness.currentGateway().Observer().Snapshot()
					blocksToSwitch := snapshot.EpochSwitchBlockHeight - snapshot.BlockHeight
					if inWindow := blocksToSwitch >= 0 && blocksToSwitch <= harness.currentGateway().Config().Load().Rotation.PrePoCBlocks; snapshot.RequestsBlocked != testCase.blocked || inWindow == testCase.blocked {
						return fmt.Errorf("snapshot RequestsBlocked %v, in the bridge window %v (%d blocks to the switch), want blocked %v and in the window %v", snapshot.RequestsBlocked, inWindow, blocksToSwitch, testCase.blocked, !testCase.blocked)
					}
					return nil
				}},
				sendRequests{promptBytes: 200, maxTokens: 64, count: 3},
				advance{by: 2 * time.Minute},
				expectThat{label: "nothing planned behind the gate", verify: func(harness *gatewayHarness) error {
					if planned := harness.plannerCreateTimes(testModelID); len(planned) != 0 {
						return fmt.Errorf("plannerCreateTimes(%s) = %d creates behind the gate, want 0 (the bridge's own temp is not the planner's)", testModelID, len(planned))
					}
					return nil
				}},
			}, testCase.toTheSwitch, []harnessStep{
				advance{by: 30 * time.Second},
				expectThat{label: "funded once the gate lifted", verify: func(harness *gatewayHarness) error {
					if guards := harness.createdWithReason(testModelID, string(funding.ReasonGuard)); guards == 0 {
						return fmt.Errorf("createdWithReason(guard) = 0 after the gate lifted, want at least 1")
					}
					return nil
				}},
			})
			runSteps(t, spec)
		})
	}
}

// Test flow:
//  1. Boot inside PoC with a target of two and one full temp labelled 8 bridging it, so after PoC both the guarantee and the spread are short.
//  2. One second past a tick, reach set_new_validators with requests unblocked, and advance one tick.
//  3. Assert exactly two escrows were created (the bucket) and the temp still serves: regulars come first.
//  4. Advance one more tick and assert the temp left service once the spread was met, and nothing more was created.
func TestTheTickAfterPoCFundsRegularsBeforeTempsRetire(t *testing.T) {
	spec := plannerSpec()
	spec.model.targetCount = 2
	spec.epoch = chainEpoch{latest: 8, effective: 7, blockHeight: 2050, pocStart: 2000, setNewValidators: 2100, phase: chain.EpochPhasePoCGenerate}
	spec.seeded = repeatSeeded(1, 1_000_000, tempRole)
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		alignToTick{offset: time.Second},
		moveChain{move: func(blockchain *fakeChain) { blockchain.setNewValidators(3000, 3100) }},
		advance{by: escrow.TickInterval},
		expectThat{label: "regulars first", verify: func(harness *gatewayHarness) error {
			if creates := len(harness.chain.createsOf(testModelID)); creates != 2 || rowsOfRole(harness, tempRole, true) != 1 {
				return fmt.Errorf("%d creates, %d serving temps, want 2 creates and the temp still serving", creates, rowsOfRole(harness, tempRole, true))
			}
			return nil
		}},
		advance{by: escrow.TickInterval},
		expectThat{label: "then the temp", verify: func(harness *gatewayHarness) error {
			if creates := len(harness.chain.createsOf(testModelID)); creates != 2 || rowsOfRole(harness, tempRole, true) != 0 {
				return fmt.Errorf("%d creates, %d serving temps, want 2 creates and no temp serving", creates, rowsOfRole(harness, tempRole, true))
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with a wallet of 500 000, below one create, over a full escrow and a starved one of 40 000.
//  2. Advance two minutes.
//  3. Assert no create was broadcast, the broken guarantee was narrated once, the starved escrow was parked and settled so its refund reached the wallet, and the full escrow still answers a request 200.
func TestAShortWalletParksResidueInsteadOfLooping(t *testing.T) {
	spec := plannerSpec()
	spec.walletBalance = 500_000
	spec.seeded = []seededEscrow{{amount: 1_000_000, role: regularRole}, {amount: 40_000, role: regularRole}}
	spec.steps = []harnessStep{
		advance{by: 2 * time.Minute},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true},
		expectThat{label: "the residue parked instead of a create loop", verify: func(harness *gatewayHarness) error {
			if broadcasts := harness.chain.broadcastCount(operationBroadcastCreate); broadcasts != 0 {
				return fmt.Errorf("%d create broadcasts, want 0: the wallet check refuses before broadcast", broadcasts)
			}
			if lines := harness.logLines("funding guarantee broken"); lines != 1 {
				return fmt.Errorf("%d broken-guarantee lines, want 1", lines)
			}
			record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[1]))
			if !record.settled || harness.creatorWallet() != 500_000+record.refund {
				return fmt.Errorf("starved escrow settled %v refund %d, wallet %d, want its refund back in the wallet", record.settled, record.refund, harness.creatorWallet())
			}
			return everyAnswer(1, http.StatusOK)(harness)
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Seed an escrow serving its last epoch, let the height advance one block every three seconds from 2 100, and script the next settle broadcast never to commit.
//  2. Advance twenty-two minutes, into the margin of the switch at 3 100 (from 2 500); assert one settle broadcast and nothing settled.
//  3. Advance twelve minutes, past the transaction's TTL; assert a second broadcast settled the escrow while the height was still below 3 000 (`settle deadline` at every step).
func TestASettleThatNeverCommitsIsRebroadcastInRealBlockTime(t *testing.T) {
	spec := lastEpochSpec()
	spec.steps = []harnessStep{
		advance{by: 22 * time.Minute},
		expectThat{label: "precondition: one settle broadcast, never committed", verify: func(harness *gatewayHarness) error {
			record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0]))
			if broadcasts := harness.chain.broadcastCount(operationBroadcastSettle); broadcasts != 1 || record.settled {
				return fmt.Errorf("%d settle broadcasts, settled %v, want 1 and unsettled", broadcasts, record.settled)
			}
			return nil
		}},
		advance{by: 12 * time.Minute},
		expectThat{label: "rebroadcast before the deadline", verify: func(harness *gatewayHarness) error {
			record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0]))
			if height := harness.chain.snapshotEpoch().blockHeight; harness.chain.broadcastCount(operationBroadcastSettle) != 2 || !record.settled || height >= 3000 {
				return fmt.Errorf("%d broadcasts, settled %v at height %d, want settled by a second broadcast below 3000", harness.chain.broadcastCount(operationBroadcastSettle), record.settled, height)
			}
			return nil
		}},
	}
	runStepsWith(t, spec, func(blockchain *fakeChain) {
		switchedToEpochEight(blockchain)
		blockchain.advanceHeightsEvery(3 * time.Second)
		blockchain.neverCommitNext(operationBroadcastSettle, 1)
	})
}

// Test flow:
//  1. Seed an escrow serving its last epoch and let the height advance one block every three seconds from 2 100.
//  2. Advance ten minutes, so the block time is measured, then take the public API down: the snapshot freezes near 2 300.
//  3. Advance fifteen minutes: the real height passes 2 500, the margin, while the frozen snapshot never does.
//  4. Assert the escrow settled while the API was down and the projection was narrated once (`settle deadline` at every step).
func TestAStaleSnapshotStillSettlesBeforeTheDeadline(t *testing.T) {
	logged := logcapture.Install(t)
	spec := lastEpochSpec()
	spec.steps = []harnessStep{
		advance{by: 10 * time.Minute},
		moveChain{move: func(blockchain *fakeChain) { blockchain.setPublicAPIDown(true) }},
		advance{by: 15 * time.Minute},
		seededEscrowSettled("stale snapshot: settled while the API was down"),
		expectThat{label: "stale snapshot: narrated once", verify: func(harness *gatewayHarness) error {
			harness.currentGateway().Journal().Flush()
			if lines := countLines(logged, "escrow deadlines read past a stale chain height"); lines != 1 {
				return fmt.Errorf("%d projection lines, want 1", lines)
			}
			return nil
		}},
	}
	runStepsWith(t, spec, func(blockchain *fakeChain) {
		switchedToEpochEight(blockchain)
		blockchain.advanceHeightsEvery(3 * time.Second)
	})
}

func countLines(logged *logcapture.Recorder, message string) int {
	lines := 0
	for _, entry := range logged.All() {
		if entry.Msg == message {
			lines++
		}
	}
	return lines
}

// Test flow:
//  1. Boot with the chain's escrow lookups down before boot.
//  2. Advance ten minutes; assert the seeded row is still active and unresolved: nothing deactivated it.
//  3. Bring the lookups back, advance two ticks, and assert the row resolved chain epoch 7 and amount 1 000 000 (`settle deadline`, `unsettled budget` at every step).
func TestLookupsDownForTenMinutesDeactivateNothing(t *testing.T) {
	spec := plannerSpec()
	spec.steps = []harnessStep{
		advance{by: 10 * time.Minute},
		seededRowUnresolved("unresolved and still serving"),
		moveChain{move: func(blockchain *fakeChain) { blockchain.setLookupsDown(false) }},
		advance{by: 2 * escrow.TickInterval},
		expectThat{label: "resolved later", verify: func(harness *gatewayHarness) error {
			if row, stored := seededRowOf(harness); !stored || !row.Active || row.ChainEpoch != 7 || row.Amount != 1_000_000 {
				return fmt.Errorf("row %+v, want active with chain epoch 7 and amount 1000000", row)
			}
			return nil
		}},
	}
	runStepsWith(t, spec, func(blockchain *fakeChain) { blockchain.setLookupsDown(true) })
}

// Test flow:
//  1. Turn settlement off and seed an escrow serving its last epoch; the scenario is exempt from `settlement match` and `settle deadline`.
//  2. Move inside the margin, advance two minutes, cross the switch into epoch 9 where the chain prunes it, and have a host report it missing.
//  3. Advance one minute.
//  4. Assert the row is inactive and marked gone from chain, the loss was narrated deadline_passed, and the deadline counter counts it (marked, not dropped).
func TestAnEscrowPrunedWithSettlementOffIsMarkedGoneAndCounted(t *testing.T) {
	spec := lastEpochSpec()
	spec.model.fullContextSlots = 2
	spec.allowUnsettled = true
	spec.environment = map[string]string{settlementEnabledEnv: "false"}
	spec.steps = append([]harnessStep{advance{by: 30 * time.Second}, moveInsideTheMargin(), advance{by: 2 * time.Minute}}, crossTheNextSwitch()...)
	spec.steps = append(spec.steps,
		reportEscrowMissing{escrowID: seededID(0)},
		advance{by: time.Minute},
		expectThat{label: "the pruned escrow is marked gone and counted", verify: func(harness *gatewayHarness) error {
			row, stored := seededRowOf(harness)
			if !stored || row.Active || !row.GoneFromChain {
				return fmt.Errorf("row %+v (stored %v), want inactive and gone from chain", row, stored)
			}
			if lines := harness.logLines("escrow deadline passes unsettled"); lines == 0 {
				return fmt.Errorf("no unsettled-deadline line")
			}
			if counted := harness.currentGateway().Manager().DeadlineUnsettledCounts()[escrow.DeadlineUnsettled{Model: testModelID, Reason: "deadline_passed"}]; counted != 1 {
				return fmt.Errorf("deadline_passed counted %d times, want 1", counted)
			}
			return nil
		}},
	)
	runStepsWith(t, spec, switchedToEpochEight)
}

// Test flow:
//  1. Boot two models with a guarantee of one each and nothing seeded, over a wallet that affords one create.
//  2. Advance thirty seconds; assert exactly one escrow was created.
//  3. Fund one more create, advance thirty seconds, and assert each model now has its one escrow: the guarantees were repaired in turn.
func TestTwoModelsSharingAWalletAreRepairedInTurn(t *testing.T) {
	spec := plannerSpec()
	spec.model.fullContextSlots = 1
	spec.otherModels = []modelSpec{{id: "scenario-model-b", maxModelLen: 8192, amount: 1_000_000, targetCount: 1, tempCount: 1, fullContextSlots: 1}}
	spec.seeded = nil
	spec.walletBalance = 1_001_000 + 500
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		expectThat{label: "one create", verify: func(harness *gatewayHarness) error {
			if total := len(harness.chain.createsOf(testModelID)) + len(harness.chain.createsOf("scenario-model-b")); total != 1 {
				return fmt.Errorf("%d creates, want 1: the wallet affords one", total)
			}
			return nil
		}},
		fundWallet{amount: 1_001_000},
		advance{by: 30 * time.Second},
		expectThat{label: "repaired in turn", verify: func(harness *gatewayHarness) error {
			if first, second := len(harness.chain.createsOf(testModelID)), len(harness.chain.createsOf("scenario-model-b")); first != 1 || second != 1 {
				return fmt.Errorf("creates per model %d and %d, want one each", first, second)
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot a model of amount 1 500 000 with two escrows of 1 000 000, full at token price 1 (slot 65 556), and advance thirty seconds.
//  2. Raise the chain's token price to 20 and advance two minutes: the slot becomes 1 310 740, more than either escrow holds, while a fresh escrow of the model still pays it; the two escrows' money above a full request, 1 934 244 at their own price, buys twenty times as much at the new one, far more than the two re-priced slots.
//  3. Assert nothing was created, the planner's last reading still counts both full at the price their sessions were opened with, so the guarantee holds, and the planner's creates kept to the bucket.
func TestATokenPriceRiseReevaluatesWithoutAStorm(t *testing.T) {
	spec := plannerSpec()
	spec.model.amount = 1_500_000
	spec.seeded = repeatSeeded(2, 1_000_000, regularRole)
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		moveChain{move: func(blockchain *fakeChain) { blockchain.setTokenPrice(20) }},
		advance{by: 2 * time.Minute},
		expectThat{label: "no create after the price rise", verify: func(harness *gatewayHarness) error {
			if creates := len(harness.chain.createsOf(testModelID)); creates != 0 {
				return fmt.Errorf("%d creates, want none: every escrow keeps its own price", creates)
			}
			report, found := harness.fundingReport(testModelID)
			if !found || report.Guarantee != 0 || report.Counts[escrow.CountFull] != 2 {
				return fmt.Errorf("fundingReport(%s) = guarantee %d, full %d (found %t), want guarantee 0 with both escrows full", testModelID, report.Guarantee, report.Counts[escrow.CountFull], found)
			}
			return nil
		}},
		expectPlannerCreatesWithinBucket("creates within the bucket", testModelID),
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with one starved escrow and a chain that rejects every create.
//  2. Advance five minutes.
//  3. Assert the breaker held the broadcasts to its back-off (between two and six over twenty ticks), the broken guarantee was narrated once, and the escrow still serves.
func TestAChainThatRejectsEveryCreateIsNotLoopedOn(t *testing.T) {
	spec := plannerSpec()
	spec.seeded = repeatSeeded(1, 20_000, regularRole)
	spec.steps = []harnessStep{
		advance{by: 5 * time.Minute},
		expectThat{label: "the breaker held the rejected creates to its back-off", verify: func(harness *gatewayHarness) error {
			if broadcasts := harness.chain.broadcastCount(operationBroadcastCreate); broadcasts < 2 || broadcasts > 6 {
				return fmt.Errorf("%d create broadcasts in five minutes, want 2 to 6 under the breaker", broadcasts)
			}
			if lines := harness.logLines("funding guarantee broken"); lines != 1 {
				return fmt.Errorf("%d broken-guarantee lines, want 1", lines)
			}
			return seededServing(harness)
		}},
	}
	runStepsWith(t, spec, func(blockchain *fakeChain) { blockchain.rejectCreates(true) })
}

// Test flow:
//  1. Boot with a guarantee of one for a model of amount 100 000 and one seeded escrow of that amount.
//  2. Advance thirty seconds; raise the model's context to 16 384 tokens, so a fresh escrow could never be full, and advance two minutes.
//  3. Assert the misconfiguration was narrated once, nothing was created, and a small request is still answered 200.
func TestAContextRiseMisconfiguresWithoutACreateLoop(t *testing.T) {
	spec := plannerSpec()
	spec.model.amount, spec.model.fullContextSlots = 100_000, 1
	spec.seeded = repeatSeeded(1, 100_000, regularRole)
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		moveChain{move: func(blockchain *fakeChain) { blockchain.setModelLength(testModelID, 16_384) }},
		advance{by: 2 * time.Minute},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true},
		expectThat{label: "the context rise narrated without a create", verify: func(harness *gatewayHarness) error {
			if lines := harness.logLines("model amount cannot fund a full escrow"); lines != 1 {
				return fmt.Errorf("%d misconfiguration lines, want 1", lines)
			}
			if creates := harness.chain.createsOf(testModelID); len(creates) != 0 {
				return fmt.Errorf("%d creates, want 0", len(creates))
			}
			return everyAnswer(1, http.StatusOK)(harness)
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot past set_new_validators into epoch 8 with a guarantee of one, max_unsettled 3, a full escrow and a starved one of 40 000, both of chain epoch 7 under label 8; the scenario is exempt from `settlement match` and `settle deadline` (the chain drops the starved escrow unsettled).
//  2. Stall every participant, send two small requests onto the starved escrow and advance thirty seconds.
//  3. Have a host report the starved escrow missing while the chain still holds it, advance thirty seconds, and assert it still serves: only a confirmed not-found deactivates.
//  4. Drop it from the chain, have a host report it missing again, and advance thirty seconds.
//  5. Assert the starved row is inactive and gone from chain (`inactive escrow unroutable` keeps it out of routing at every step).
//  6. Raise the guarantee to three and advance thirty seconds; assert two guard creates landed, which max_unsettled 3 leaves room for only once the gone escrow left the count.
//  7. Move inside the settle margin of the switch into epoch 9 and advance two minutes; assert no deadline action named the gone escrow: no deadline line, no park, no settle.
func TestAStarvedEscrowGoneFromChainLeavesTheCount(t *testing.T) {
	spec := lastEpochSpec()
	spec.model.fullContextSlots, spec.model.maxUnsettled = 1, 3
	spec.allowUnsettled = true
	spec.seeded = []seededEscrow{{amount: 1_000_000, role: regularRole}, {amount: 40_000, role: regularRole}}
	var reconfiguredAt time.Time
	spec.steps = []harnessStep{
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.stall = true }},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 2},
		advance{by: 30 * time.Second},
		expectThat{label: "precondition: open records on the starved escrow", verify: func(harness *gatewayHarness) error {
			if harness.fleet.inferencesStartedOn(harness.seededIDs[1]) == 0 {
				return fmt.Errorf("no attempt on the starved escrow: the scenario proves nothing")
			}
			return nil
		}},
		reportEscrowMissing{escrowID: seededID(1)},
		advance{by: 30 * time.Second},
		expectThat{label: "a report the chain contradicts leaves the escrow serving", verify: func(harness *gatewayHarness) error {
			if row, stored := rowOf(harness, harness.seededIDs[1]); !stored || !row.Active || row.GoneFromChain {
				return fmt.Errorf("rowOf(%s) = %+v, want still serving while the chain holds it", harness.seededIDs[1], row)
			}
			return nil
		}},
		dropSeededEscrow{index: 1},
		reportEscrowMissing{escrowID: seededID(1)},
		advance{by: 30 * time.Second},
		expectThat{label: "the starved escrow left service", verify: func(harness *gatewayHarness) error {
			row, stored := rowOf(harness, harness.seededIDs[1])
			if !stored || row.Active || !row.GoneFromChain {
				return fmt.Errorf("rowOf(%s) = %+v, want inactive and gone from chain", harness.seededIDs[1], row)
			}
			return nil
		}},
		expectThat{label: "precondition: nothing created yet", verify: func(harness *gatewayHarness) error {
			reconfiguredAt = time.Now()
			if creates := len(harness.chain.createsOf(testModelID)); creates != 0 {
				return fmt.Errorf("createsOf(%s) = %d creates, want 0 before the guarantee rises", testModelID, creates)
			}
			return nil
		}},
		reconfigureModel{change: func(model *modelSpec) { model.fullContextSlots = 3 }},
		advance{by: 30 * time.Second},
		expectThat{label: "the gone escrow left the count", verify: func(harness *gatewayHarness) error {
			if guards, created := harness.createdWithReason(testModelID, string(funding.ReasonGuard)), createsAfter(harness, reconfiguredAt); guards != 2 || created != 2 {
				return fmt.Errorf("createdWithReason(guard) = %d, createsAfter(the rise) = %d, want 2 and 2: room under max_unsettled 3 once the gone escrow is not counted", guards, created)
			}
			return nil
		}},
		moveInsideTheMargin(),
		advance{by: 2 * time.Minute},
		expectThat{label: "no deadline action on the gone escrow", verify: func(harness *gatewayHarness) error {
			goneID := harness.seededIDs[1]
			for _, entry := range harness.loggedEntries() {
				named := logcapture.Field(entry, logkey.Escrow) == goneID
				if named && (entry.Msg == "escrow deadline passes unsettled" || entry.Msg == "escrow parked at its settlement deadline") {
					return fmt.Errorf("log line %q %v names the gone escrow, want no deadline action", entry.Msg, entry.Fields)
				}
			}
			if row, stored := rowOf(harness, goneID); !stored || row.SettlementPending || row.SettleTxHash != "" {
				return fmt.Errorf("rowOf(%s) = %+v, stored %v, want stored, never parked nor settled", goneID, row, stored)
			}
			return nil
		}},
	}
	runStepsWith(t, spec, switchedToEpochEight)
}

// Test flow:
//  1. Boot two models of different amount and context sharing one wallet, a guarantee of one each, nothing seeded.
//  2. Advance one minute.
//  3. Assert each model got one escrow of its own amount and its report shows its guarantee met.
func TestTwoModelsKeepIndependentGuarantees(t *testing.T) {
	spec := plannerSpec()
	spec.model.fullContextSlots = 1
	spec.otherModels = []modelSpec{{id: "scenario-model-b", maxModelLen: 2_048, amount: 200_000, targetCount: 1, tempCount: 1, fullContextSlots: 1}}
	spec.seeded = nil
	spec.steps = []harnessStep{
		advance{by: time.Minute},
		expectThat{label: "each model funded its own guarantee", verify: func(harness *gatewayHarness) error {
			for _, model := range harness.spec.allModels() {
				escrows := harness.chain.escrowsOf(model.id)
				if len(escrows) != 1 || escrows[0].amount != model.amount {
					return fmt.Errorf("model %s escrows %+v, want one of amount %d", model.id, escrows, model.amount)
				}
				if report, found := harness.fundingReport(model.id); !found || report.Guarantee != 0 {
					return fmt.Errorf("model %s guarantee %d (found %v), want 0", model.id, report.Guarantee, found)
				}
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// dropSeededEscrow drops a seeded escrow from the chain by the id the chain gave it; the fake chain numbers escrows from 1001.
type dropSeededEscrow struct{ index int }

func (step dropSeededEscrow) apply(harness *gatewayHarness) {
	harness.chain.dropEscrow(parseEscrowID(harness.seededIDs[step.index]))
}
