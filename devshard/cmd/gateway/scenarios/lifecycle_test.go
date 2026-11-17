package scenarios

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/scheduler"
	"devshard/types"
)

// everyValidatorChecks makes each of the three non-executor validators of a four-host group check every inference: ShouldValidate divides by (groupSize-1)*10000 (state/validation.go:81-82).
const everyValidatorChecks = 30000

func slotStats(harness *gatewayHarness, escrowID string) map[uint32]*types.HostStats {
	machine, built := harness.fleet.userMachine(escrowID)
	if !built {
		return nil
	}
	return machine.SnapshotState().HostStats
}

// requestRounds sends in rounds because a validation landing after the record is Challenged carries no weight (host.go:1482).
func requestRounds(rounds, requestsPerRound int, gap time.Duration) []harnessStep {
	steps := make([]harnessStep, 0, 2*rounds)
	for range rounds {
		steps = append(steps, sendRequests{promptBytes: 200, maxTokens: 64, count: requestsPerRound, sequential: true}, advance{by: gap})
	}
	return steps
}

// Test flow:
//  1. Take participant 0 offline for everything, so the nonces it executes never get a receipt.
//  2. Send eight requests one after another on the seeded escrow, then advance four minutes.
//  3. Assert slot 0 was charged a miss: its refusal was voted through and the reservation returned (`money identity` holds every step).
func TestARefusedNonceIsRefundedByTheVote(t *testing.T) {
	spec := defaultSpec()
	spec.behaviours = map[int]participantBehaviour{0: {offline: true}}
	spec.steps = []harnessStep{
		sendRequests{promptBytes: 200, maxTokens: 64, count: 8, sequential: true},
		advance{by: 4 * time.Minute},
		expectThat{label: "slot 0 charged a refusal", verify: func(harness *gatewayHarness) error {
			if stats := slotStats(harness, harness.seededIDs[0]); stats[0] == nil || stats[0].Missed == 0 {
				return fmt.Errorf("slot 0 stats = %+v, want Missed > 0", stats[0])
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Make validators 1, 2 and 3 vote every validation invalid.
//  2. Send 24 rounds of 4 sequential requests 10 seconds apart so the challenges and votes reach the session.
//  3. Assert slot 0 was invalidated at least once, so its cost went back to the escrow (`money identity` holds every step).
func TestAnInvalidatedInferenceReturnsItsCost(t *testing.T) {
	spec := defaultSpec()
	spec.validationRate = 10000
	spec.behaviours = map[int]participantBehaviour{
		1: {votesInvalid: true},
		2: {votesInvalid: true},
		3: {votesInvalid: true},
	}
	spec.steps = append(requestRounds(24, 4, 10*time.Second),
		expectThat{label: "slot 0 invalidated", verify: func(harness *gatewayHarness) error {
			if stats := slotStats(harness, harness.seededIDs[0]); stats[0] == nil || stats[0].Invalid == 0 {
				return fmt.Errorf("slot 0 stats = %+v, want Invalid > 0", stats[0])
			}
			return nil
		}},
	)
	runSteps(t, spec)
}

// Test flow:
//  1. Seed one escrow below its model's floor and make the chain lose the response to the next create broadcast.
//  2. Boot, so the first tick plans the guard create the starved escrow leaves short and broadcasts it, then advance two minutes.
//  3. Assert exactly one escrow was created and its row is active in the store: the commitment recovered the lost answer.
func TestAReplacementWhoseAnswerWasLostIsRecovered(t *testing.T) {
	spec := defaultSpec()
	spec.seeded = []seededEscrow{{amount: 20_000, role: "regular"}}
	spec.steps = []harnessStep{
		advance{by: 2 * time.Minute},
		expectThat{label: "one replacement, registered", verify: func(harness *gatewayHarness) error {
			creates := harness.chain.createsOf(testModelID)
			if len(creates) != 1 {
				return fmt.Errorf("%d creates, want 1", len(creates))
			}
			for _, row := range harness.rows() {
				if row.EscrowID == formatEscrowID(creates[0].escrowID) && row.Active {
					return nil
				}
			}
			return fmt.Errorf("escrow %d created but not active in the store", creates[0].escrowID)
		}},
	}
	runStepsWith(t, spec, func(blockchain *fakeChain) { blockchain.loseNextResponse(operationBroadcastCreate) })
}

// Test flow:
//  1. Send six requests one after another on the seeded escrow.
//  2. Move the chain into the bridge window before PoC, so the next tick funds a temp and retires the seeded escrow.
//  3. Advance three minutes, so the gateway finalizes the escrow, builds its settlement and broadcasts it.
//  4. Assert the chain settled the seeded escrow with costs above zero (`settlement match` compares its costs and fees with the session every step).
func TestAnEscrowThatServedTrafficSettles(t *testing.T) {
	spec := defaultSpec()
	spec.steps = []harnessStep{
		sendRequests{promptBytes: 200, maxTokens: 64, count: 6, sequential: true},
		moveChain{move: func(blockchain *fakeChain) { blockchain.moveToHeight(1900) }},
		advance{by: 3 * time.Minute},
		expectThat{label: "the escrow that served traffic settled", verify: func(harness *gatewayHarness) error {
			record, known := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0]))
			if !known || !record.settled || record.settledCosts == 0 {
				return fmt.Errorf("escrow %s settled %v with costs %d fees %d, want settled with costs above 0", harness.seededIDs[0], record.settled, record.settledCosts, record.settledFees)
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Table-driven: a bridge window that opens before PoC starts, one a PoC longer than the window holds entirely, and one the chain leaves at exactly set_new_validators, with requests still blocked or already unblocked; each starts with one regular escrow and a temp count of 1.
//  2. Boot and advance thirty seconds, then assert the first tick funded exactly one temp, labelled 8: the epoch it bridges into.
//  3. Move the chain on (PoC start, later in the same PoC, or to exactly set_new_validators, where the switch has happened) and advance one minute.
//  4. While the bridge has not finished, assert one temp still covers the window and it is still labelled 8: no second set labelled 9 is funded at the switch.
//  5. With requests unblocked at set_new_validators, assert instead the bridge finished: two creates in all (the temp and the regular the planner funds for the spread), no row labelled 9, no temp serving, and one regular labelled 8 serving.
func TestABridgeWindowCrossingPoCStartFundsOneTempSet(t *testing.T) {
	testCases := []struct {
		name           string
		epoch          chainEpoch
		move           func(*fakeChain)
		bridgeFinished bool
	}{
		{
			name:  "the window opens before PoC starts",
			epoch: chainEpoch{latest: 7, effective: 7, blockHeight: 1000, pocStart: 1150, setNewValidators: 1250, phase: chain.EpochPhaseInference},
			move:  func(blockchain *fakeChain) { blockchain.startPoC() },
		},
		{
			name:  "a PoC longer than the window holds all of it",
			epoch: chainEpoch{latest: 8, effective: 7, blockHeight: 1000, pocStart: 900, setNewValidators: 1250, phase: chain.EpochPhasePoCGenerate},
			move:  func(blockchain *fakeChain) { blockchain.moveToHeight(1100) },
		},
		{
			name:  "the chain stands at exactly set_new_validators with requests blocked",
			epoch: chainEpoch{latest: 8, effective: 7, blockHeight: 1000, pocStart: 900, setNewValidators: 1250, phase: chain.EpochPhasePoCGenerate},
			move:  func(blockchain *fakeChain) { blockchain.reachSetNewValidators(chain.EpochPhasePoCGenerate) },
		},
		{
			name:           "the chain stands at exactly set_new_validators with requests unblocked",
			epoch:          chainEpoch{latest: 8, effective: 7, blockHeight: 1000, pocStart: 900, setNewValidators: 1250, phase: chain.EpochPhasePoCGenerate},
			move:           func(blockchain *fakeChain) { blockchain.reachSetNewValidators(chain.EpochPhaseInference) },
			bridgeFinished: true,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			spec := defaultSpec()
			spec.epoch = testCase.epoch
			oneTempLabelledEight := func(label string) expectThat {
				return expectThat{label: label, verify: func(harness *gatewayHarness) error {
					if temps := countTempRows(harness); temps != 1 {
						return fmt.Errorf("%d temp escrows, want 1", temps)
					}
					for _, row := range harness.rows() {
						if row.RotationRole == tempRole && row.RotationEpoch != 8 {
							return fmt.Errorf("temp %s labelled %d, want 8: the epoch it bridges into", row.EscrowID, row.RotationEpoch)
						}
					}
					return nil
				}}
			}
			spec.steps = []harnessStep{
				advance{by: 30 * time.Second},
				oneTempLabelledEight("precondition: one temp funded in the window"),
				moveChain{move: testCase.move},
				advance{by: time.Minute},
			}
			if !testCase.bridgeFinished {
				spec.steps = append(spec.steps, oneTempLabelledEight("one temp labelled 8 still covers the window"))
			} else {
				spec.steps = append(spec.steps, expectThat{label: "the bridge finished at set_new_validators", verify: func(harness *gatewayHarness) error {
					if creates := len(harness.chain.createsOf(testModelID)); creates != 2 {
						return fmt.Errorf("%d creates, want 2: the temp and the regular the planner funds for the spread", creates)
					}
					servingRegulars := 0
					for _, row := range harness.rows() {
						if row.RotationEpoch == 9 {
							return fmt.Errorf("escrow %s labelled 9, want none: the bridge window closed at the switch", row.EscrowID)
						}
						if row.RotationRole == tempRole && row.Active {
							return fmt.Errorf("temp %s still active, want parked: the switch has happened", row.EscrowID)
						}
						if row.RotationRole == regularRole && row.Active && row.RotationEpoch == 8 {
							servingRegulars++
						}
					}
					if servingRegulars != 1 {
						return fmt.Errorf("%d active regulars labelled 8, want 1 the planner funds for the spread", servingRegulars)
					}
					return nil
				}})
			}
			runSteps(t, spec)
		})
	}
}

func countTempRows(harness *gatewayHarness) int {
	temps := 0
	for _, row := range harness.rows() {
		if row.RotationRole == tempRole {
			temps++
		}
	}
	return temps
}

// Test flow:
//  1. Seed four escrows of a target-4 model already below the model's floor.
//  2. Boot and advance one minute.
//  3. Assert the first tick funded at most two escrows and the minute exactly two (the guarantee of two full escrows), the four starved escrows still serve, and the wallet paid for exactly two creates.
func TestDepletedEscrowsInOneTickFundAtMostTwoEscrows(t *testing.T) {
	spec := plannerSpec()
	spec.model.targetCount = 4
	spec.seeded = repeatSeeded(4, 20_000, regularRole)
	spec.steps = []harnessStep{
		advance{by: time.Minute},
		expectThat{label: "at most two escrows funded in the first tick and two in the minute", verify: func(harness *gatewayHarness) error {
			creates, inFirstTick := harness.chain.createsOf(testModelID), 0
			for _, created := range creates {
				if created.at.Before(harness.bootedAt.Add(escrow.TickInterval)) {
					inFirstTick++
				}
			}
			if inFirstTick > 2 || len(creates) != 2 {
				return fmt.Errorf("%d creates in the first tick and %d in the minute, want at most 2 and exactly 2", inFirstTick, len(creates))
			}
			for _, escrowID := range harness.seededIDs {
				if row, stored := rowOf(harness, escrowID); !stored || !row.Active {
					return fmt.Errorf("starved escrow %s = %+v, want still serving its residue", escrowID, row)
				}
			}
			if wallet := harness.creatorWallet(); wallet != 100_000_000-2*(1_000_000+1_000) {
				return fmt.Errorf("wallet = %d, want %d: two creates of 1 000 000 and their fees", wallet, 100_000_000-2*(1_000_000+1_000))
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Seed one escrow just above its floor while one request is in flight, make the chain refuse every create so the fleet stays this one escrow, and slow every participant to four seconds so the race hedges.
//  2. Align to one second past a tick, send one request and wait for it.
//  3. Assert the race hedged on the seeded escrow and its balance is still at or above the model's idle floor, then advance two ticks.
//  4. Assert nothing was created, no balance mark reached the planner and the escrow still serves: the hedge reported nothing.
func TestAHedgeDoesNotRetireAnEscrowThatCanStillPay(t *testing.T) {
	spec := defaultSpec()
	spec.seeded = []seededEscrow{{amount: 37_200, role: "regular"}}
	spec.steps = []harnessStep{
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.delay = 4 * time.Second }},
		alignToTick{offset: time.Second},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true},
		expectThat{label: "precondition: the race hedged on the seeded escrow", verify: func(harness *gatewayHarness) error {
			if started := harness.fleet.inferencesStartedOn(harness.seededIDs[0]); started < 2 {
				return fmt.Errorf("%d inferences started on the seeded escrow, want at least 2: no hedge, the scenario proves nothing", started)
			}
			return nil
		}},
		expectThat{label: "precondition: the seeded escrow can still pay at the idle floor", verify: func(harness *gatewayHarness) error {
			snapshot := harness.currentGateway().Observer().Snapshot()
			idleFloor := max(uint64(4096), 4*harness.spec.model.maxModelLen)*snapshot.TokenPrice + snapshot.FeePerNonce
			if balance := harness.userBalance(harness.seededIDs[0]); balance < idleFloor {
				return fmt.Errorf("the seeded escrow holds %d, below the idle floor %d: retiring it is right, the scenario proves nothing", balance, idleFloor)
			}
			return nil
		}},
		advance{by: 2 * escrow.TickInterval},
		expectThat{label: "the hedge created nothing and marked nothing", verify: func(harness *gatewayHarness) error {
			if creates := harness.chain.createsOf(testModelID); len(creates) != 0 {
				return fmt.Errorf("%d escrows created, want 0", len(creates))
			}
			if report, found := harness.fundingReport(testModelID); !found || report.IgnoredMarks[string(scheduler.ExhaustionBalanceFloor)] != 0 {
				return fmt.Errorf("ignored balance-floor marks = %v (found %t), want none: the hedge reported its escrow", report.IgnoredMarks, found)
			}
			for _, row := range harness.rows() {
				if row.EscrowID == harness.seededIDs[0] && row.Active {
					return nil
				}
			}
			return fmt.Errorf("escrow %s no longer serving", harness.seededIDs[0])
		}},
	}
	runStepsWith(t, spec, func(blockchain *fakeChain) { blockchain.rejectCreates(true) })
}

// Test flow:
//  1. Seed one escrow whose balance pays a twenty-kilobyte request and then its hedge, make the chain refuse every create so the fleet stays this one escrow, capture the gateway's log, and slow every participant to four seconds so the race hedges.
//  2. Align to one second past a tick, send one such request and wait for it.
//  3. Assert the request was answered, the hedge was sent rather than declined as `ErrPinnedEscrowShort`, and the escrow still covers every reservation it took.
func TestAHedgeTheEscrowCanPayIsSent(t *testing.T) {
	logged := logcapture.Install(t)
	spec := defaultSpec()
	spec.seeded = []seededEscrow{{amount: 60_200, role: "regular"}}
	spec.steps = []harnessStep{
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.delay = 4 * time.Second }},
		alignToTick{offset: time.Second},
		sendRequests{promptBytes: 19_900, maxTokens: 64, count: 1, sequential: true},
		expectThat{label: "the hedge the escrow could pay was sent", verify: func(harness *gatewayHarness) error {
			escrowID := harness.seededIDs[0]
			if statuses := harness.answeredStatuses(); len(statuses) != 1 || statuses[0] != http.StatusOK {
				return fmt.Errorf("statuses = %v, want [200]", statuses)
			}
			if declines := pinnedShortDeclines(harness, logged); declines != 0 {
				return fmt.Errorf("%d hedges declined as ErrPinnedEscrowShort, want 0: the balance already net of the first attempt paid the hedge", declines)
			}
			if started := harness.fleet.inferencesStartedOn(escrowID); started != 2 {
				return fmt.Errorf("%d inferences started on %s, want 2: the first attempt and its hedge", started, escrowID)
			}
			return nil
		}},
	}
	runStepsWith(t, spec, func(blockchain *fakeChain) { blockchain.rejectCreates(true) })
}

// alignedRequests sends each request one second past a tick, waits for it, then lets two ticks pass, so the heartbeat's drain sequences its Finish before the next request.
func alignedRequests(count, promptBytes int) []harnessStep {
	steps := make([]harnessStep, 0, 3*count)
	for range count {
		steps = append(steps,
			alignToTick{offset: time.Second},
			sendRequests{promptBytes: promptBytes, maxTokens: 64, count: 1, sequential: true},
			advance{by: 2 * escrow.TickInterval},
		)
	}
	return steps
}

// pinnedShortDeclines counts the escalations the journal logged as unfilled because the pinned escrow could not pay them.
func pinnedShortDeclines(harness *gatewayHarness, logged *logcapture.Recorder) int {
	harness.currentGateway().Journal().Flush()
	declines := 0
	for _, entry := range logged.All() {
		if refusal, isError := logcapture.Field(entry, "error").(error); entry.Msg == "escalation unfilled" && isError && errors.Is(refusal, scheduler.ErrPinnedEscrowShort) {
			declines++
		}
	}
	return declines
}

// Test flow:
//  1. Seed one escrow whose balance pays a twenty-kilobyte request but not its hedge, make the chain refuse every create so the fleet stays this one escrow, capture the gateway's log, and slow every participant to four seconds so every race hedges.
//  2. Send fifty such requests, each one second past a tick, waited for, and followed by two ticks, then advance one more tick.
//  3. Assert every request was answered with one attempt and every race's hedge was declined as `ErrPinnedEscrowShort`.
//  4. Assert nothing was created, the escrow was never marked for replacement and it still serves.
func TestAPinnedHedgeThatCannotPayNeverMarksItsEscrow(t *testing.T) {
	const races = 50
	logged := logcapture.Install(t)
	spec := defaultSpec()
	spec.seeded = []seededEscrow{{amount: 40_300, role: "regular"}}
	spec.steps = slices.Concat(
		[]harnessStep{changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.delay = 4 * time.Second }}},
		alignedRequests(races, 19_900),
		[]harnessStep{
			advance{by: escrow.TickInterval},
			expectThat{label: "every hedge declined and the escrow never marked", verify: func(harness *gatewayHarness) error {
				escrowID := harness.seededIDs[0]
				if started := harness.fleet.inferencesStartedOn(escrowID); started != races {
					return fmt.Errorf("%d inferences started on %s, want %d: one attempt per request, every hedge declined", started, escrowID, races)
				}
				if declines := pinnedShortDeclines(harness, logged); declines != races {
					return fmt.Errorf("%d hedges declined as ErrPinnedEscrowShort, want %d: one per race", declines, races)
				}
				statuses := harness.answeredStatuses()
				if len(statuses) != races || slices.ContainsFunc(statuses, func(status int) bool { return status != http.StatusOK }) {
					return fmt.Errorf("statuses = %v, want %d answers of 200", statuses, races)
				}
				if creates := harness.chain.createsOf(testModelID); len(creates) != 0 {
					return fmt.Errorf("%d escrows created, want 0", len(creates))
				}
				if marked, found := logged.Find("escrow marked for replacement"); found {
					return fmt.Errorf("escrow marked for replacement: %v", marked.Fields)
				}
				if report, found := harness.fundingReport(testModelID); !found || report.IgnoredMarks[string(scheduler.ExhaustionBalanceFloor)] != 0 {
					return fmt.Errorf("ignored balance-floor marks = %v (found %t), want none: the hedge reported its escrow", report.IgnoredMarks, found)
				}
				for _, row := range harness.rows() {
					if row.EscrowID == escrowID && row.Active {
						return nil
					}
				}
				return fmt.Errorf("escrow %s no longer serving", escrowID)
			}},
		},
	)
	runStepsWith(t, spec, func(blockchain *fakeChain) { blockchain.rejectCreates(true) })
}

func absentSlotBurns(harness *gatewayHarness, escrowID string) float64 {
	families, err := harness.currentGateway().Telemetry().Registry().Gather()
	if err != nil {
		return 0
	}
	burned := 0.0
	for _, family := range families {
		if family.GetName() != "devshard_gateway_ghost_nonces_burned_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["devshard_id"] == escrowID && labels["reason"] == "participant_absent_from_chain_no_send" {
				burned += metric.GetCounter().GetValue()
			}
		}
	}
	return burned
}

// Test flow:
//  1. Leave participant 1 unlisted on the chain, so the seeded escrow opens with a dead slot for it.
//  2. Send twelve requests one after another and assert the escrow burned nonces on that slot as absent from chain.
//  3. List participant 1 again and advance four minutes, past the refusal vote that keeps the escrow's only session busy.
//  4. Send twelve more requests and assert every request was answered, the escrow burned no further absent-slot nonce and participant 1 was sent to again.
func TestASlotAbsentAtOpenServesOnceItsHostIsListedAgain(t *testing.T) {
	spec := defaultSpec()
	spec.behaviours = map[int]participantBehaviour{1: {unlisted: true}}
	var burnedWhileAbsent float64
	var sentBeforeListing int
	spec.steps = []harnessStep{
		sendRequests{promptBytes: 200, maxTokens: 64, count: 12, sequential: true},
		expectThat{label: "the dead slot burns", verify: func(harness *gatewayHarness) error {
			if burnedWhileAbsent = absentSlotBurns(harness, harness.seededIDs[0]); burnedWhileAbsent == 0 {
				return errors.New("no absent-slot burn on the seeded escrow, want at least one")
			}
			sentBeforeListing = harness.fleet.sendsTo(harness.fleet.order[1])
			return nil
		}},
		changeParticipants{indexes: []int{1}, change: func(behaviour *participantBehaviour) { behaviour.unlisted = false }},
		advance{by: 4 * time.Minute},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 12, sequential: true},
		expectThat{label: "the listed host serves again", verify: func(harness *gatewayHarness) error {
			for _, status := range harness.answeredStatuses() {
				if status != http.StatusOK {
					return fmt.Errorf("answered statuses %v, want every one %d", harness.answeredStatuses(), http.StatusOK)
				}
			}
			if burned := absentSlotBurns(harness, harness.seededIDs[0]); burned != burnedWhileAbsent {
				return fmt.Errorf("absent-slot burns %v after the host was listed again, want %v", burned, burnedWhileAbsent)
			}
			if sent := harness.fleet.sendsTo(harness.fleet.order[1]); sent == sentBeforeListing {
				return fmt.Errorf("participant 1 sent to %d times, as many as before it was listed again", sent)
			}
			return nil
		}},
	}
	runSteps(t, spec)
}
