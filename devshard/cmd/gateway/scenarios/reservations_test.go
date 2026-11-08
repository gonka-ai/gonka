package scenarios

import (
	"fmt"
	"testing"
	"time"

	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/liquidity"
	"devshard/types"
)

type advanceFor struct {
	duration func(*gatewayHarness) time.Duration
}

func (step advanceFor) apply(harness *gatewayHarness) { time.Sleep(step.duration(harness)) }

func hasRecord(harness *gatewayHarness, escrowID string, status types.InferenceStatus) bool {
	_, _, state, built := machineMoney(harness, escrowID)
	if !built {
		return false
	}
	for _, inference := range state.Inferences {
		if inference.Status == status {
			return true
		}
	}
	return false
}

func crossIntoEpochEight() harnessStep {
	return moveChain{move: func(blockchain *fakeChain) { blockchain.startPoC(); blockchain.setNewValidators(3000, 3100) }}
}

func stuckMoneyCounted(label string) expectThat {
	return expectThat{label: label, verify: func(harness *gatewayHarness) error {
		report, found := harness.fundingReport(testModelID)
		if !found || report.Money[liquidity.ClassStuck] == 0 {
			return fmt.Errorf("report money = %v (found %v), want stuck money counted", report.Money, found)
		}
		return seededServing(harness)
	}}
}

// Test flow:
//  1. Boot with a guarantee of one and take participant 0 offline for everything, so the attempt it is given stays Pending with no receipt while the race hedges and answers.
//  2. Send eight requests one after another, then restart the gateway at once, before any refusal vote at +65 s.
//  3. Advance ten minutes; assert a Pending record outlived the retry ladder, the report counts stuck money, and the escrow still serves: nothing waited on it.
//  4. Cross into epoch 8, move inside the settle margin, advance three minutes, and assert the escrow settled with the stuck reservation paid out at finalize (`settlement match` at every step).
func TestAPendingRecordWhoseLadderARestartCutIsStuckNotWaitedOn(t *testing.T) {
	spec := plannerSpec()
	spec.model.fullContextSlots = 1
	spec.behaviours = map[int]participantBehaviour{0: {offline: true}}
	spec.steps = []harnessStep{
		sendRequests{promptBytes: 200, maxTokens: 64, count: 8, sequential: true},
		restartGateway{},
		advance{by: 10 * time.Minute},
		expectThat{label: "precondition: a Pending record outlived the ladder", verify: func(harness *gatewayHarness) error {
			if !hasRecord(harness, harness.seededIDs[0], types.StatusPending) {
				return fmt.Errorf("no Pending record on the seeded escrow: the scenario proves nothing")
			}
			return nil
		}},
		stuckMoneyCounted("stuck, counted, not waited on"),
		crossIntoEpochEight(),
		moveInsideTheMargin(),
		advance{by: 3 * time.Minute},
		seededEscrowSettled("settled with the stuck reservation paid out"),
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with a guarantee of one and make every participant stall after its receipt.
//  2. Send one request and advance six minutes; assert the report counts the Started reservations as late and the escrow serves, never held.
//  3. Advance thirty-five minutes; assert no Started record is left and the escrow was charged nothing for them: swept after the deadline and refunded (`money identity`, `active escrow routes` at every step).
func TestAStalledExecutorIsLateThenSwept(t *testing.T) {
	spec := plannerSpec()
	spec.model.fullContextSlots = 1
	spec.steps = []harnessStep{
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.stall = true }},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1},
		advance{by: 6 * time.Minute},
		expectThat{label: "late, not held", verify: func(harness *gatewayHarness) error {
			report, _ := harness.fundingReport(testModelID)
			if report.Money[liquidity.ClassLate] == 0 {
				return fmt.Errorf("report money = %v, want the Started reservations late", report.Money)
			}
			return seededServing(harness)
		}},
		advance{by: 35 * time.Minute},
		expectThat{label: "swept and refunded", verify: func(harness *gatewayHarness) error {
			costs, _, _, _ := machineMoney(harness, harness.seededIDs[0])
			if hasRecord(harness, harness.seededIDs[0], types.StatusStarted) || costs != 0 {
				return fmt.Errorf("Started left %v, costs %d, want swept and nothing charged", hasRecord(harness, harness.seededIDs[0], types.StatusStarted), costs)
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Widen the admission limit to 50 requests per 10 000 weight (the default would answer half the burst with 429), make every participant stall after its receipt, and send two hundred requests at one instant across the seeded escrow and the one the guarantee funds: a request whose escrow's 64-waiter dispatch buffer is full is offered to the other escrow.
//  2. Advance one minute and count the Started records.
//  3. Restart the gateway: the races and the timeout votes they owed die with the old process, so only the sweep can clear the records; assert the restart severed the open sessions and every counted record is still Started.
//  4. Advance past the execution timeout, its buffer and the sweep grace, plus one tick per eight records and two more.
//  5. Assert no Started record is left on any escrow: the 8-vote sweep budget drained the backlog in time (`active escrow routes` at every step).
func TestTwoHundredOverdueRecordsDrainWithinTheSweepBudget(t *testing.T) {
	spec := plannerSpec()
	spec.environment = map[string]string{"GATEWAY_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT": "50"}
	started := 0
	spec.steps = []harnessStep{
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.stall = true }},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 200},
		advance{by: time.Minute},
		expectThat{label: "precondition: the backlog", verify: func(harness *gatewayHarness) error {
			for _, escrowID := range harness.fleet.builtEscrowIDs() {
				_, _, state, _ := machineMoney(harness, escrowID)
				for _, inference := range state.Inferences {
					if inference.Status == types.StatusStarted {
						started++
					}
				}
			}
			if started < 200 {
				return fmt.Errorf("%d Started records, want at least 200: the scenario proves nothing", started)
			}
			return nil
		}},
		restartGateway{},
		expectThat{label: "precondition: only the sweep is left to clear the backlog", verify: func(harness *gatewayHarness) error {
			stillStarted := 0
			for _, escrowID := range harness.fleet.builtEscrowIDs() {
				_, _, state, _ := machineMoney(harness, escrowID)
				for _, inference := range state.Inferences {
					if inference.Status == types.StatusStarted {
						stillStarted++
					}
				}
			}
			if harness.severedSessions == 0 || stillStarted != started {
				return fmt.Errorf("%d sessions severed and %d of %d records still Started after the restart, want the old sessions severed and every record Started", harness.severedSessions, stillStarted, started)
			}
			return nil
		}},
		advanceFor{duration: func(*gatewayHarness) time.Duration {
			ticks := (started+7)/8 + 2
			return (executionTimeoutSeconds+timeoutBufferSeconds+sweepGraceSeconds)*time.Second + time.Duration(ticks)*escrow.TickInterval
		}},
		expectThat{label: "every record swept", verify: func(harness *gatewayHarness) error {
			for _, escrowID := range harness.fleet.builtEscrowIDs() {
				if hasRecord(harness, escrowID, types.StatusStarted) {
					return fmt.Errorf("escrow %s still holds a Started record", escrowID)
				}
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot over sixteen participants and a group of sixteen, take participant 0 offline and seven others' votes offline, so a refusal vote reaches 8 of the 11 it needs.
//  2. Send sixteen requests one after another and advance ten minutes; assert a Pending record outlived the ladder, the report counts it stuck, and the escrow serves.
//  3. Cross into epoch 8, move inside the margin, advance three minutes, and assert the escrow settled: the loss is accounted, not waited on (`settlement match` at every step).
func TestARefusalVoteShortOfQuorumIsAccountedStuck(t *testing.T) {
	spec := plannerSpec()
	spec.participants, spec.groupSize, spec.model.fullContextSlots = 16, 16, 1
	spec.behaviours = map[int]participantBehaviour{0: {offline: true}}
	for participant := 1; participant <= 7; participant++ {
		spec.behaviours[participant] = participantBehaviour{votesOffline: true}
	}
	spec.steps = []harnessStep{
		sendRequests{promptBytes: 200, maxTokens: 64, count: 16, sequential: true},
		advance{by: 10 * time.Minute},
		expectThat{label: "precondition: a Pending record outlived the ladder", verify: func(harness *gatewayHarness) error {
			if !hasRecord(harness, harness.seededIDs[0], types.StatusPending) {
				return fmt.Errorf("no Pending record on the seeded escrow: the scenario proves nothing")
			}
			return nil
		}},
		stuckMoneyCounted("stuck and counted"),
		crossIntoEpochEight(),
		moveInsideTheMargin(),
		advance{by: 3 * time.Minute},
		seededEscrowSettled("settled with the loss accounted"),
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with a guarantee of one and make every participant answer after 400 seconds.
//  2. Send one request and advance 330 seconds, before the answer lands; assert the report counts its reservation late.
//  3. Advance forty minutes; assert the request ended (`request ends`) and no Started record is left on the seeded escrow.
func TestALongAnswerIsLateThenLeavesTheOpenSet(t *testing.T) {
	spec := plannerSpec()
	spec.model.fullContextSlots = 1
	spec.behaviours = everyParticipant(4, participantBehaviour{delay: 400 * time.Second})
	spec.steps = []harnessStep{
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1},
		advance{by: 330 * time.Second},
		expectThat{label: "late", verify: func(harness *gatewayHarness) error {
			report, _ := harness.fundingReport(testModelID)
			if report.Money[liquidity.ClassLate] == 0 {
				return fmt.Errorf("report money = %v, want the long answer's reservation late", report.Money)
			}
			return nil
		}},
		advance{by: 40 * time.Minute},
		expectThat{label: "ended, nothing open", verify: func(harness *gatewayHarness) error {
			if statuses := harness.answeredStatuses(); len(statuses) != 1 || hasRecord(harness, harness.seededIDs[0], types.StatusStarted) {
				return fmt.Errorf("statuses %v, Started left %v, want the request ended and nothing open", statuses, hasRecord(harness, harness.seededIDs[0], types.StatusStarted))
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// challengedResidueSpec runs the planner with a guarantee of one over a full escrow and a starved one that small requests spend first; participant 1 votes everything invalid, and two valid votes never pass the threshold of 2, so its challenges stay Challenged.
func challengedResidueSpec(starvedAmount uint64) testSpec {
	spec := plannerSpec()
	spec.model.fullContextSlots = 1
	spec.seeded = []seededEscrow{{amount: 1_000_000, role: regularRole}, {amount: starvedAmount, role: regularRole}}
	spec.validationRate = everyValidatorChecks
	spec.behaviours = map[int]participantBehaviour{1: {votesInvalid: true}}
	return spec
}

func challengedOn(index int) expectThat {
	return expectThat{label: "precondition: a Challenged record", verify: func(harness *gatewayHarness) error {
		if !hasRecord(harness, harness.seededIDs[index], types.StatusChallenged) {
			return fmt.Errorf("no Challenged record on escrow %s: the scenario proves nothing", harness.seededIDs[index])
		}
		return nil
	}}
}

// Test flow:
//  1. Use the challenged residue model: a full escrow and a starved one of 60 000, participant 1 voting every validation invalid.
//  2. Send three rounds of four small requests ten seconds apart, so the starved escrow holds a Challenged record.
//  3. Bill 5 000 input tokens an answer, send six 6 000-byte requests one after another and advance two minutes.
//  4. Assert the starved escrow still serves, was never held, and the report counts the challenged money stuck (`settle deadline`, `active escrow routes` at every step).
func TestAChallengedRecordIsStuckMoney(t *testing.T) {
	spec := challengedResidueSpec(60_000)
	spec.steps = append(requestRounds(3, 4, 10*time.Second),
		challengedOn(1),
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.inputTokens = 5_000 }},
		sendRequests{promptBytes: 6_000, maxTokens: 64, count: 6, sequential: true},
		advance{by: 2 * time.Minute},
		stuckMoneyCounted("the challenged money counted stuck"),
	)
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with three escrows of 78 500 under a guarantee of two, full but holding little above it: their money past one full-context request each (about 133 500) covers the two slots the model needs (131 112) with about two thousand to spare, while participant 1 validates everything and votes it invalid.
//  2. Send six rounds of four small requests ten seconds apart and advance two minutes.
//  3. Assert the escrows hold Challenged records, nothing was created although their reservations would push the model's need past its money if they counted as demand, and the report counts the challenged money stuck (`create reason` at every step).
func TestChallengedMoneyDrivesNoCreate(t *testing.T) {
	spec := plannerSpec()
	spec.seeded = repeatSeeded(3, 78_500, regularRole)
	spec.validationRate = everyValidatorChecks
	spec.behaviours = map[int]participantBehaviour{1: {votesInvalid: true}}
	spec.steps = append(requestRounds(6, 4, 10*time.Second),
		advance{by: 2 * time.Minute},
		expectThat{label: "no create for challenged money", verify: func(harness *gatewayHarness) error {
			challenged := 0
			for _, escrowID := range harness.seededIDs {
				if hasRecord(harness, escrowID, types.StatusChallenged) {
					challenged++
				}
			}
			if challenged == 0 {
				return fmt.Errorf("no seeded escrow holds a Challenged record: the scenario proves nothing")
			}
			report, _ := harness.fundingReport(testModelID)
			if creates := harness.chain.createsOf(testModelID); len(creates) != 0 || report.Money[liquidity.ClassStuck] == 0 {
				return fmt.Errorf("%d creates, report money %v, want none and challenged money stuck", len(creates), report.Money)
			}
			return nil
		}},
	)
	runSteps(t, spec)
}

// Test flow:
//  1. Seed an escrow serving its last epoch, every validator checking everything, and send two rounds of four requests five seconds apart, so validations are still in flight.
//  2. Stall every participant, so no validation completes and no execution returns, send two more requests and advance five seconds; assert the escrow is busy with them (else the scenario proves nothing).
//  3. Move inside the settle margin and advance three minutes.
//  4. Assert the escrow settled inside its window while both stalled requests were still open: the deadline settle crossed the in-flight work instead of waiting out the thirty-two-minute execution timeout (`settle deadline` at every step).
func TestAnEscrowSettlesInsideItsWindowWithValidationsUndone(t *testing.T) {
	spec := lastEpochSpec()
	spec.model.fullContextSlots = 2
	spec.validationRate = everyValidatorChecks
	spec.steps = append([]harnessStep{advance{by: 30 * time.Second}}, requestRounds(2, 4, 5*time.Second)...)
	spec.steps = append(spec.steps,
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.stall = true }},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 2},
		advance{by: 5 * time.Second},
		expectThat{label: "precondition: the escrow is busy at its margin", verify: func(harness *gatewayHarness) error {
			if !harness.currentGateway().Escrows().IsBusy(harness.seededIDs[0]) {
				return fmt.Errorf("IsBusy(%s) = false, want the stalled requests holding it: the scenario proves nothing", harness.seededIDs[0])
			}
			return nil
		}},
		moveInsideTheMargin(),
		advance{by: 3 * time.Minute},
		seededEscrowSettled("settled inside its window"),
		expectThat{label: "settled past the in-flight work", verify: func(harness *gatewayHarness) error {
			if open := harness.openRequestCount(); open != 2 {
				return fmt.Errorf("openRequestCount() = %d, want the 2 stalled requests still open when it settled", open)
			}
			return nil
		}},
	)
	runStepsWith(t, spec, switchedToEpochEight)
}

// Test flow:
//  1. Use the challenged residue model with a starved escrow of 40 000.
//  2. Send three rounds of four small requests ten seconds apart, so the starved escrow holds a Challenged record, then twelve quiet minutes.
//  3. Assert the starved escrow was retired as idle and settled on chain, its refund its amount less costs and fees (`settlement match`), while the full one still serves.
func TestAStarvedEscrowIdleButForAChallengeIsRetired(t *testing.T) {
	spec := challengedResidueSpec(40_000)
	spec.steps = append(requestRounds(3, 4, 10*time.Second),
		challengedOn(1),
		advance{by: 12 * time.Minute},
		expectThat{label: "the idle starved escrow retired and settled", verify: func(harness *gatewayHarness) error {
			record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[1]))
			if !record.settled || record.refund == 0 {
				return fmt.Errorf("starved escrow settled %v refund %d, want retired as idle and settled", record.settled, record.refund)
			}
			if row, stored := rowOf(harness, harness.seededIDs[0]); !stored || !row.Active {
				return fmt.Errorf("full escrow %+v, want still serving", row)
			}
			return nil
		}},
	)
	runSteps(t, spec)
}

// Test flow:
//  1. Boot over eight participants and a group of sixteen in which participant 0 holds nine slots and votes every validation invalid.
//  2. Send six rounds of four requests ten seconds apart and advance two minutes.
//  3. Assert a challenge happened and log which outcome the hosts reached: whether a challenged record is invalidated depends on whether an honest host validates after it saw the challenge (host.go:1475), an order of host goroutines the gateway does not control.
//  4. Where a record stayed Challenged, assert the gateway counts its money stuck, none of it returning or late; an invalidated record's refund is held to `money identity` at every step.
//  5. Settle the escrow, advance one minute, and assert it settled and the wallet balances against the chain's creates and refunds.
func TestAMajorityHolderVotingInvalidIsHandledWhicheverWayTheHostsResolveIt(t *testing.T) {
	spec := plannerSpec()
	spec.participants, spec.groupSize, spec.model.fullContextSlots = 8, 16, 1
	spec.slotOwners = []int{0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 2, 3, 4, 5, 6, 7}
	spec.validationRate = 150_000
	spec.behaviours = map[int]participantBehaviour{0: {votesInvalid: true}}
	spec.steps = append(requestRounds(6, 4, 10*time.Second),
		advance{by: 2 * time.Minute},
		expectThat{label: "the host outcome handled", verify: func(harness *gatewayHarness) error {
			escrowID := harness.seededIDs[0]
			invalidated := false
			for _, stats := range slotStats(harness, escrowID) {
				if stats != nil && stats.Invalid > 0 {
					invalidated = true
				}
			}
			stuck := hasRecord(harness, escrowID, types.StatusChallenged)
			harness.t.Logf("outcome: invalidated %t, stuck Challenged %t", invalidated, stuck)
			if !invalidated && !stuck {
				return fmt.Errorf("no record of escrow %s challenged: the scenario proves nothing", escrowID)
			}
			if !stuck {
				return nil
			}
			report, found := harness.fundingReport(testModelID)
			if !found || report.Money[liquidity.ClassStuck] == 0 || report.Money[liquidity.ClassReturning] != 0 || report.Money[liquidity.ClassLate] != 0 {
				return fmt.Errorf("report money = %v (found %t), want the challenged money stuck and nothing returning or late", report.Money, found)
			}
			return nil
		}},
		operatorSettles{escrowID: seededID(0)},
		advance{by: time.Minute},
		seededEscrowSettled("settled whatever the hosts resolved"),
		expectThat{label: "the wallet balances", verify: walletBalanced},
	)
	runSteps(t, spec)
}
