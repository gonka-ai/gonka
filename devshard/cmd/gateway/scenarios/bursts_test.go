package scenarios

import (
	"fmt"
	"maps"
	"net/http"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/funding"
	"devshard/cmd/gateway/liquidity"
	"devshard/cmd/gateway/scheduler"
	"devshard/types"
)

const fullContextAttempts = 2

func seededServing(harness *gatewayHarness) error {
	for _, escrowID := range harness.seededIDs {
		if row, stored := rowOf(harness, escrowID); !stored || !row.Active {
			return fmt.Errorf("seeded escrow %s = %+v, want serving", escrowID, row)
		}
	}
	return nil
}

func machineMoney(harness *gatewayHarness, escrowID string) (costs, reserved uint64, state types.EscrowState, built bool) {
	machine, built := harness.fleet.userMachine(escrowID)
	if !built {
		return 0, 0, types.EscrowState{}, false
	}
	state = machine.SnapshotState()
	for _, stats := range state.HostStats {
		costs += stats.Cost
	}
	for _, inference := range state.Inferences {
		if inference.Status == types.StatusPending || inference.Status == types.StatusStarted {
			reserved += inference.ReservedCost
		}
	}
	return costs, reserved, state, true
}

func plannerReasonsOnly(harness *gatewayHarness, allowed ...funding.Reason) error {
	lines := createLinesOf(harness.loggedEntries())
	for _, created := range harness.chain.createsOf(testModelID) {
		reason := funding.Reason(lines[formatEscrowID(created.escrowID)].reason)
		if !slices.Contains(allowed, reason) {
			return fmt.Errorf("escrow %d created for %q, want one of %v", created.escrowID, reason, allowed)
		}
	}
	return nil
}

// Test flow:
//  1. Boot with four escrows of 363 124 for a target-4 model of that amount, each sized for twelve full-context attempts of 30 252 after the create fee, every participant answering after two seconds.
//  2. Send forty full-context requests in four waves of ten 1.2 s apart, off the instants answers land, and advance two minutes: with the hedges the burst reserves more than the four escrows hold, so they run down through the range that pays one attempt but not two.
//  3. Assert every request was answered 200 or 503, and every 503 left while no routable escrow of the model could pay one attempt: no 503 while some escrow can pay.
//  4. Assert the four seeded escrows still serve and every planner create kept to the bucket (`money identity`, `unsettled budget` and `create reason` at every step).
func TestAFullContextBurstIsServedWithoutAReplacementStorm(t *testing.T) {
	const fullContextAttemptCost = 30_252
	spec := plannerSpec()
	spec.model.targetCount, spec.model.amount = 4, 363_124
	spec.seeded = repeatSeeded(4, 363_124, regularRole)
	spec.behaviours = everyParticipant(4, participantBehaviour{delay: 2 * time.Second})
	for range 4 {
		spec.steps = append(spec.steps, sendRequests{promptBytes: 30_000, maxTokens: 64, count: 10}, advance{by: 1200 * time.Millisecond})
	}
	spec.steps = append(spec.steps,
		advance{by: 2 * time.Minute},
		expectThat{label: "no 503 while some escrow can pay", verify: func(harness *gatewayHarness) error {
			statuses := harness.answeredStatuses()
			if len(statuses) != 40 || slices.ContainsFunc(statuses, func(status int) bool { return status != http.StatusOK && status != http.StatusServiceUnavailable }) {
				return fmt.Errorf("statuses = %v, want forty answers of 200 or 503", statuses)
			}
			for _, largestBalance := range harness.balancesAtUnavailable() {
				if largestBalance >= fullContextAttemptCost {
					return fmt.Errorf("a 503 left while a routable escrow held %d, want every escrow below one attempt of %d", largestBalance, fullContextAttemptCost)
				}
			}
			return nil
		}},
		expectThat{label: "the seeded escrows still serve", verify: seededServing},
		expectPlannerCreatesWithinBucket("creates within the bucket", testModelID),
	)
	runSteps(t, spec)
}

// Test flow:
//  1. Bill every answer at 5 000 input tokens, so a 20 000-byte prompt reserves about four times what it is charged.
//  2. Send six such requests, each one second past a tick and followed by two ticks, then advance six quiet minutes.
//  3. Assert the model's report holds no returning, late or stuck money and the seeded escrow holds no reservation: every surplus came back at Finish (`money identity` at every step).
func TestInputHeavyRequestsReturnTheirSurplusAtFinish(t *testing.T) {
	spec := plannerSpec()
	spec.behaviours = everyParticipant(4, participantBehaviour{inputTokens: 5_000})
	spec.steps = append(alignedRequests(6, 20_000),
		advance{by: 6 * time.Minute},
		expectThat{label: "returning drained", verify: func(harness *gatewayHarness) error {
			report, found := harness.fundingReport(testModelID)
			if !found {
				return fmt.Errorf("no funding report for %s", testModelID)
			}
			if report.Money[liquidity.ClassReturning] != 0 || report.Money[liquidity.ClassLate] != 0 || report.Money[liquidity.ClassStuck] != 0 {
				return fmt.Errorf("report money = %v, want nothing returning, late or stuck", report.Money)
			}
			if costs, reserved, _, built := machineMoney(harness, harness.seededIDs[0]); !built || reserved != 0 || costs == 0 {
				return fmt.Errorf("seeded escrow costs %d reserved %d (built %v), want charged and nothing reserved", costs, reserved, built)
			}
			return nil
		}},
	)
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with one full escrow and one starved escrow holding 40 000, and let the first tick fund the second full escrow the guarantee wants.
//  2. Send five 264-token requests interleaved with five full-context ones, one after another.
//  3. Assert all ten were answered 200, exactly the five small ones started on the starved escrow, and its balance fell by what they cost while it still serves.
func TestSmallRequestsSpendTheStarvedResidueFirst(t *testing.T) {
	spec := plannerSpec()
	spec.seeded = []seededEscrow{{amount: 1_000_000, role: regularRole}, {amount: 40_000, role: regularRole}}
	spec.steps = []harnessStep{advance{by: 30 * time.Second}}
	for range 5 {
		spec.steps = append(spec.steps,
			sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true},
			sendRequests{promptBytes: 30_000, maxTokens: 64, count: 1, sequential: true},
		)
	}
	spec.steps = append(spec.steps, expectThat{label: "the small requests spent the starved residue", verify: func(harness *gatewayHarness) error {
		if err := everyAnswer(10, http.StatusOK)(harness); err != nil {
			return err
		}
		starved := harness.seededIDs[1]
		if started := harness.fleet.inferencesStartedOn(starved); started != 5 {
			return fmt.Errorf("%d attempts started on the starved escrow, want the 5 small requests", started)
		}
		balance := harness.userBalance(starved)
		if row, stored := rowOf(harness, starved); !stored || !row.Active || balance >= 40_000-100 || balance < 40_000-100-5*1_000 {
			return fmt.Errorf("starved escrow balance %d (row %+v), want serving and lowered by five small requests", balance, row)
		}
		return nil
	}})
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with a guarantee of one: two starved regulars of 20 000 and one full standby; make the chain reject every create before boot, so the capacity create the starved regulars call for (L = 0 < need = slot) cannot land and take the request.
//  2. Advance thirty seconds and assert a create was attempted and none landed.
//  3. Send one full-context request, which only the standby can pay, and advance thirty seconds; assert it was answered 200 and the standby now serves as a regular.
//  4. Let the chain accept creates again and advance ninety seconds, past the breaker's back-off.
//  5. Assert exactly one escrow landed and it was a standby, and the wallet paid for that one create (rejected attempts cost nothing).
func TestAStandbyTakenWhileEveryRegularIsStarvedIsRepaired(t *testing.T) {
	spec := plannerSpec()
	spec.model.reserveCount, spec.model.fullContextSlots = 1, 1
	spec.seeded = []seededEscrow{{amount: 20_000, role: regularRole}, {amount: 20_000, role: regularRole}, {amount: 1_000_000, role: escrow.RoleReserve}}
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		expectThat{label: "precondition: creates attempted, none landed", verify: func(harness *gatewayHarness) error {
			if attempts, landed := harness.chain.broadcastCount(operationBroadcastCreate), len(harness.chain.createsOf(testModelID)); attempts == 0 || landed != 0 {
				return fmt.Errorf("%d create broadcasts, %d landed, want at least one rejected and none landed", attempts, landed)
			}
			return nil
		}},
		sendRequests{promptBytes: 30_000, maxTokens: 64, count: 1, sequential: true},
		advance{by: 30 * time.Second},
		expectThat{label: "the standby served and was promoted", verify: func(harness *gatewayHarness) error {
			if err := everyAnswer(1, http.StatusOK)(harness); err != nil {
				return err
			}
			if row, stored := rowOf(harness, harness.seededIDs[2]); !stored || !row.Active || row.RotationRole != regularRole {
				return fmt.Errorf("former standby %+v, want serving as a regular", row)
			}
			return nil
		}},
		moveChain{move: func(blockchain *fakeChain) { blockchain.rejectCreates(false) }},
		advance{by: 90 * time.Second},
		expectThat{label: "one standby repaired", verify: func(harness *gatewayHarness) error {
			if creates, standbys := len(harness.chain.createsOf(testModelID)), harness.createdWithReason(testModelID, string(funding.ReasonStandby)); creates != 1 || standbys != 1 {
				return fmt.Errorf("%d creates landed, %d of them standbys, want exactly one standby", creates, standbys)
			}
			if wallet := harness.creatorWallet(); wallet != 100_000_000-(1_000_000+1_000) {
				return fmt.Errorf("wallet = %d, want %d: one create paid", wallet, 100_000_000-(1_000_000+1_000))
			}
			return nil
		}},
	}
	runStepsWith(t, spec, func(blockchain *fakeChain) { blockchain.rejectCreates(true) })
}

// moneyShortBurstSpec runs a model of amount 100 000 with two full seeded escrows of that amount, hosts answering after thirty seconds, so each escrow reserves two 30 000-byte requests and declines the rest for money.
func moneyShortBurstSpec() testSpec {
	spec := plannerSpec()
	spec.model.amount = 100_000
	spec.seeded = repeatSeeded(2, 100_000, regularRole)
	spec.behaviours = everyParticipant(4, participantBehaviour{delay: 30 * time.Second})
	return spec
}

// walletBalanced checks the creator's wallet against the chain's own record: the starting balance, less every create's amount and fee, plus every settled escrow's refund.
func walletBalanced(harness *gatewayHarness) error {
	expected := harness.spec.walletBalance
	created := map[uint64]bool{}
	for _, model := range harness.spec.modelIDs() {
		for _, create := range harness.chain.createsOf(model) {
			created[create.escrowID] = true
		}
		for _, record := range harness.chain.escrowsOf(model) {
			if created[record.id] {
				expected -= record.amount + harness.chain.shape.txFee
			}
			if record.settled {
				expected += record.refund
			}
		}
	}
	if wallet := harness.creatorWallet(); wallet != expected {
		return fmt.Errorf("creatorWallet() = %d, want %d from the chain's creates and refunds", wallet, expected)
	}
	return nil
}

// Test flow:
//  1. Use the money-short burst model: two full escrows of 100 000, hosts answering after thirty seconds, and a planner budget of six unsettled escrows.
//  2. One second past a tick, send a thousand 30 000-byte requests at once: a few are reserved, and every other pick is declined for money.
//  3. Advance two minutes; assert every create was a guard or capacity create within the bucket and the wallet balances against the chain's creates and refunds; `unsettled budget` holds the escrows on chain to the planner's six at every step, while budget pressure parks residue and refills the room.
//  4. Advance to four minutes, long after the burst's last answer, and count the creates.
//  5. Advance sixteen more minutes in one-minute steps, through the ten-minute demand window and the surplus retires after it.
//  6. Assert the planner created nothing in those sixteen minutes and the wallet still balances.
func TestAThousandMoneyShortPicksBuyNoMoreThanTheBucket(t *testing.T) {
	spec := moneyShortBurstSpec()
	convergedCreates := 0
	spec.steps = []harnessStep{
		alignToTick{offset: time.Second},
		sendRequests{promptBytes: 30_000, maxTokens: 64, count: 1_000},
		advance{by: 2 * time.Minute},
		expectPlannerCreatesWithinBucket("creates within the bucket", testModelID),
		expectThat{label: "guard or capacity creates only", verify: func(harness *gatewayHarness) error {
			return plannerReasonsOnly(harness, funding.ReasonGuard, funding.ReasonCapacity)
		}},
		expectThat{label: "the wallet balances", verify: walletBalanced},
		advance{by: 2 * time.Minute},
		expectThat{label: "count the creates once the planner has converged", verify: func(harness *gatewayHarness) error {
			convergedCreates = len(harness.chain.createsOf(testModelID))
			return nil
		}},
	}
	for range 16 {
		spec.steps = append(spec.steps, advance{by: time.Minute})
	}
	spec.steps = append(spec.steps,
		expectThat{label: "no create once converged", verify: func(harness *gatewayHarness) error {
			if creates := len(harness.chain.createsOf(testModelID)); creates != convergedCreates {
				return fmt.Errorf("%d creates at twenty minutes, want the %d of four minutes", creates, convergedCreates)
			}
			return nil
		}},
		expectThat{label: "the wallet still balances", verify: walletBalanced},
	)
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with a guarantee of one, one full escrow and one holding 20 000, below its model's 32 778 floor.
//  2. Send one 264-token request.
//  3. Assert it was answered 200 on the 20 000 escrow, which still serves with its balance lowered by what the request cost.
func TestAnEscrowBelowItsFloorServesWhatItCanPay(t *testing.T) {
	spec := plannerSpec()
	spec.model.fullContextSlots = 1
	spec.seeded = []seededEscrow{{amount: 1_000_000, role: regularRole}, {amount: 20_000, role: regularRole}}
	spec.steps = []harnessStep{
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true},
		expectThat{label: "the escrow below its floor served the request", verify: func(harness *gatewayHarness) error {
			if err := everyAnswer(1, http.StatusOK)(harness); err != nil {
				return err
			}
			starved := harness.seededIDs[1]
			balance := harness.userBalance(starved)
			if row, stored := rowOf(harness, starved); harness.fleet.inferencesStartedOn(starved) != 1 || !stored || !row.Active || balance >= 20_000-100 {
				return fmt.Errorf("escrow %s: %d attempts, row %+v, balance %d, want the request served on it and the escrow still serving", starved, harness.fleet.inferencesStartedOn(starved), row, balance)
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Use the money-short burst model: two full escrows of 100 000, hosts answering after thirty seconds.
//  2. One second past a tick, send fifty 30 000-byte requests 200 ms apart: four are reserved, and each of the other forty-six marks the model money-short and wakes the tick.
//  3. Advance five seconds and assert at most two escrows were created in those fifteen seconds, whatever the number of wakeups.
//  4. The design's "D_peak sampled once" is not asserted here: the report does not expose demand samples. It is pinned by escrow.TestOnlyAScheduledTickSamplesDemand and listed as unasserted in the hand-off.
func TestFiftyWakeupsInFifteenSecondsBuyAtMostTwo(t *testing.T) {
	spec := moneyShortBurstSpec()
	var alignedAt time.Time
	spec.steps = []harnessStep{
		alignToTick{offset: time.Second},
		expectThat{label: "mark the burst", verify: func(*gatewayHarness) error {
			alignedAt = time.Now()
			return nil
		}},
	}
	for range 50 {
		spec.steps = append(spec.steps, sendRequests{promptBytes: 30_000, maxTokens: 64, count: 1}, advance{by: 200 * time.Millisecond})
	}
	spec.steps = append(spec.steps,
		advance{by: 5 * time.Second},
		expectThat{label: "at most two creates in fifteen seconds", verify: func(harness *gatewayHarness) error {
			inWindow := 0
			for _, created := range harness.chain.createsOf(testModelID) {
				if !created.at.Before(alignedAt) && created.at.Before(alignedAt.Add(escrow.TickInterval)) {
					inWindow++
				}
			}
			if inWindow > 2 {
				return fmt.Errorf("%d creates within fifteen seconds of fifty wakeups, want at most 2", inWindow)
			}
			return nil
		}},
	)
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with a guarantee of one and take every participant offline, so every attempt is refused and replaced on the same escrow until the race's attempt limit, one per host, stops it.
//  2. Send two 264-token requests at once, so their nonces interleave on the escrow's slots, and advance two minutes.
//  3. Assert neither request was answered 200 and each started exactly four attempts on the seeded escrow, one on each of its hosts, so the reservations each race made are four attempts' worth; log the count against the two attempts a slot is priced for (`money identity` at every step).
func TestRefusedAttemptsReplacedOnOneEscrowStayWithinItsHosts(t *testing.T) {
	spec := plannerSpec()
	spec.model.fullContextSlots = 1
	spec.behaviours = everyParticipant(4, participantBehaviour{offline: true})
	spec.steps = []harnessStep{
		sendRequests{promptBytes: 200, maxTokens: 64, count: 2},
		advance{by: 2 * time.Minute},
		expectThat{label: "the replacements stayed within the escrow's hosts", verify: func(harness *gatewayHarness) error {
			if statuses := harness.answeredStatuses(); len(statuses) != 2 || slices.Contains(statuses, http.StatusOK) {
				return fmt.Errorf("statuses = %v, want two answers that are not 200: every host refused", statuses)
			}
			_, reserved, state, built := machineMoney(harness, harness.seededIDs[0])
			executorsByRequest := map[string]map[uint32]int{}
			for _, inference := range state.Inferences {
				prompt := string(inference.PromptHash)
				if executorsByRequest[prompt] == nil {
					executorsByRequest[prompt] = map[uint32]int{}
				}
				executorsByRequest[prompt][inference.ExecutorSlot]++
			}
			harness.t.Logf("%d attempts reserving %d on one escrow for two requests; slot is priced for %d", len(state.Inferences), reserved, fullContextAttempts)
			if !built || len(executorsByRequest) != 2 {
				return fmt.Errorf("escrow %s: attempts of %d requests, want both requests' attempts", harness.seededIDs[0], len(executorsByRequest))
			}
			for _, executors := range executorsByRequest {
				if len(executors) != 4 || slices.Max(slices.Collect(maps.Values(executors))) != 1 {
					return fmt.Errorf("a request's attempts per executor slot = %v, want exactly one on each of the escrow's 4 hosts", executors)
				}
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with a guarantee of one, one full escrow and one of 1 100, whose 1 000 pays both attempts of one small request (452 each), so routing prefers it as residue, but not a third attempt.
//  2. Send three small requests at once: a pick that reads the 1 000 before both earlier reservations landed takes it, and its nonce start fails for balance.
//  3. Advance one minute; assert every request was answered 200, by a retry elsewhere where its start failed, and the nearly empty escrow still serves and was never parked: the planner parks nothing for a failed nonce start (`money identity` at every step).
//  4. Whether a pick reads the balance before a reservation lands is up to the goroutine scheduler, so boot afresh until a run's start failed for balance and reached the planner as an ignored mark, at most forty runs; assert one did, else the scenario proves nothing.
func TestPicksRacingANearlyEmptyEscrowParkNothing(t *testing.T) {
	const (
		requests    = 3
		maximumRuns = 40
	)
	raced := false
	for run := 0; run < maximumRuns && !raced; run++ {
		spec := plannerSpec()
		spec.model.fullContextSlots = 1
		spec.seeded = []seededEscrow{{amount: 1_000_000, role: regularRole}, {amount: 1_100, role: regularRole}}
		spec.steps = []harnessStep{
			sendRequests{promptBytes: 200, maxTokens: 64, count: requests},
			advance{by: time.Minute},
			expectThat{label: "the racing picks parked nothing", verify: func(harness *gatewayHarness) error {
				if err := everyAnswer(requests, http.StatusOK)(harness); err != nil {
					return err
				}
				if row, stored := rowOf(harness, harness.seededIDs[1]); !stored || !row.Active || row.SettlementPending {
					return fmt.Errorf("nearly empty escrow = %+v, want still serving and never parked", row)
				}
				report, found := harness.fundingReport(testModelID)
				raced = found && report.IgnoredMarks[string(scheduler.ExhaustionInsufficientBalance)] > 0
				return nil
			}},
		}
		runSteps(t, spec)
	}
	if !raced {
		t.Fatalf("no run of %d had a nonce start fail for balance on the nearly empty escrow, want one: the scenario proves nothing", maximumRuns)
	}
}

// Test flow:
//  1. Table-driven over a 40 000-byte prompt on a model of 8 192 tokens (over four bytes a token) and a 2 000-byte prompt on a model whose chain context length is 0.
//  2. Send the request and wait for it.
//  3. Assert the gateway did not answer 400: it refuses nothing at ingest by its own byte estimate.
func TestALargeOrUnmeasuredPromptIsNotRefusedAtIngest(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		maxModelLen uint64
		promptBytes int
	}{
		{name: "a body over four times the context", maxModelLen: 8_192, promptBytes: 40_000},
		{name: "a model with no context length", maxModelLen: 0, promptBytes: 2_000},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			spec := plannerSpec()
			spec.model.maxModelLen = testCase.maxModelLen
			spec.steps = []harnessStep{
				sendRequests{promptBytes: testCase.promptBytes, maxTokens: 64, count: 1, sequential: true},
				expectThat{label: "the prompt was not refused at ingest", verify: func(harness *gatewayHarness) error {
					if statuses := harness.answeredStatuses(); len(statuses) != 1 || statuses[0] == http.StatusBadRequest {
						return fmt.Errorf("statuses = %v, want one answer that is not 400", statuses)
					}
					return nil
				}},
			}
			runSteps(t, spec)
		})
	}
}

// Test flow:
//  1. Boot a model of amount 150 000 with one seeded escrow of that amount, billing every answer one token per two prompt bytes.
//  2. Send three bursts of eight 20 000-byte requests a minute apart, then advance three minutes.
//  3. Assert at least two escrows are full again, so the guarantee held or was repaired, every planner create kept to the bucket, and at rest the harness's full count equals the planner's; `create reason` held at every step though returning overestimated the surplus.
func TestTokenDenseInputKeepsTheGuarantee(t *testing.T) {
	spec := plannerSpec()
	spec.model.amount = 150_000
	spec.seeded = repeatSeeded(1, 150_000, regularRole)
	spec.behaviours = everyParticipant(4, participantBehaviour{inputTokens: 10_000})
	for range 3 {
		spec.steps = append(spec.steps, sendRequests{promptBytes: 20_000, maxTokens: 64, count: 8}, advance{by: time.Minute})
	}
	spec.steps = append(spec.steps,
		advance{by: 3 * time.Minute},
		expectThat{label: "the guarantee holds", verify: func(harness *gatewayHarness) error {
			if full := harness.fullEscrows(testModelID); full < 2 {
				return fmt.Errorf("%d full escrows, want at least the guarantee of 2", full)
			}
			return nil
		}},
		expectPlannerCreatesWithinBucket("creates within the bucket", testModelID),
		expectFullCountMatchesThePlanner("the planner's full count matches the fleet at rest", spec.model),
	)
	runSteps(t, spec)
}

const (
	contextRefusalMissLabel = "defect: the refusing host is charged a miss for an engine error after the receipt is signed"
	refusingExecutorSlot    = 1
	shorterHostContext      = 4_096
)

// Test flow:
//  1. Boot with a guarantee of one, every host running the model's whole 8 192-token context and refusing a longer prompt the way devshardd relays vLLM's 400, and send one 40 000-byte prompt.
//  2. Advance three minutes; assert the client was answered 400 and the race stopped at the first refusal: one attempt on the first executor, Started and holding the escrow's only reservation.
//  3. Advance forty one-minute steps, past the session's execution timeout of 1 920 seconds and its margins.
//  4. Assert the execution timeout returned that reservation and the wallet balances against the chain (`money identity` at every step).
//  5. Pin the host defect: the refusing host alone is charged one miss for its engine error after the signed receipt.
func TestAnOverContextPromptIsAnsweredAfterTheFirstRefusal(t *testing.T) {
	behaviours := everyParticipant(4, participantBehaviour{contextLimit: 8_192})
	runSteps(t, contextRefusalSpec(behaviours, 40_000, http.StatusBadRequest, 1))
}

// Test flow:
//  1. Boot with a guarantee of one, the first executor running a 4 096-token context under the model's 8 192 and the other hosts the whole length, and send one 20 000-byte prompt that fits the model.
//  2. Advance three minutes; assert the race retried the refusal on a longer host and the client was answered 200: two attempts, the refused one Started and holding the escrow's only reservation.
//  3. Advance forty one-minute steps, past the session's execution timeout.
//  4. Assert the execution timeout returned the refused attempt's reservation and the wallet balances (`money identity` at every step).
//  5. Pin the host defect: the refusing host alone is charged one miss.
func TestAShorterHostsContextRefusalIsRetriedOnALongerHost(t *testing.T) {
	runSteps(t, contextRefusalSpec(withAShorterFirstExecutor(), 20_000, http.StatusOK, 2))
}

// Test flow:
//  1. Boot with a guarantee of one, the first executor running a 4 096-token context under the model's 8 192 and the other hosts the whole length, and send one 40 000-byte prompt larger than the model's context.
//  2. Advance three minutes; assert the client was answered 400 without a retry: one attempt on the shorter host, Started and holding the escrow's only reservation.
//  3. Advance forty one-minute steps, past the session's execution timeout.
//  4. Assert the execution timeout returned that reservation and the wallet balances (`money identity` at every step).
//  5. Pin the host defect: the refusing host alone is charged one miss.
func TestAPromptOverTheModelIsNotRetriedPastAShorterHost(t *testing.T) {
	runSteps(t, contextRefusalSpec(withAShorterFirstExecutor(), 40_000, http.StatusBadRequest, 1))
}

func withAShorterFirstExecutor() map[int]participantBehaviour {
	behaviours := everyParticipant(4, participantBehaviour{contextLimit: 8_192})
	behaviours[refusingExecutorSlot] = participantBehaviour{contextLimit: shorterHostContext}
	return behaviours
}

// contextRefusalSpec sends one prompt at hosts that refuse past their context, and follows the refused attempt to its execution timeout.
func contextRefusalSpec(behaviours map[int]participantBehaviour, promptBytes, wantStatus, wantAttempts int) testSpec {
	spec := plannerSpec()
	spec.model.fullContextSlots = 1
	spec.behaviours = behaviours
	spec.pins = map[string]string{
		contextRefusalMissLabel: "the host signs its receipt before its engine refuses and publishes no Finish over the refusal (host/host.go:1111-1115), so only an execution-timeout vote settles the record, and it marks the executor missed",
	}
	spec.steps = []harnessStep{
		sendRequests{promptBytes: promptBytes, maxTokens: 64, count: 1, sequential: true},
		advance{by: 3 * time.Minute},
		expectThat{label: "the client is answered once", verify: everyAnswer(1, wantStatus)},
		expectThat{label: "only the refused attempt holds a reservation", verify: func(harness *gatewayHarness) error {
			escrowID := harness.seededIDs[0]
			_, reserved, state, built := machineMoney(harness, escrowID)
			if started := harness.fleet.inferencesStartedOn(escrowID); !built || started != wantAttempts || len(state.Inferences) != wantAttempts {
				return fmt.Errorf("escrow %s started %d attempts with %d records, want %d", escrowID, started, len(state.Inferences), wantAttempts)
			}
			refused, recorded := state.Inferences[1]
			if !recorded || refused.ExecutorSlot != refusingExecutorSlot || refused.Status != types.StatusStarted || reserved != refused.ReservedCost {
				return fmt.Errorf("first record %+v with %d reserved, want it Started on slot %d holding the only reservation", refused, reserved, refusingExecutorSlot)
			}
			return nil
		}},
	}
	for range 40 {
		spec.steps = append(spec.steps, advance{by: time.Minute})
	}
	spec.steps = append(spec.steps,
		expectThat{label: "the execution timeout returned the refused attempt's reservation", verify: func(harness *gatewayHarness) error {
			_, reserved, state, _ := machineMoney(harness, harness.seededIDs[0])
			if refused, recorded := state.Inferences[1]; !recorded || refused.Status != types.StatusTimedOut || reserved != 0 {
				return fmt.Errorf("first record %+v with %d reserved, want it timed out and nothing reserved", refused, reserved)
			}
			return walletBalanced(harness)
		}},
		expectThat{label: contextRefusalMissLabel, verify: func(harness *gatewayHarness) error {
			_, _, state, _ := machineMoney(harness, harness.seededIDs[0])
			for slot, stats := range state.HostStats {
				wantMissed := uint32(0)
				if slot == refusingExecutorSlot {
					wantMissed = 1
				}
				if stats.Missed != wantMissed {
					return nil
				}
			}
			return fmt.Errorf("slot %d was charged one miss for the prompt its engine refused", refusingExecutorSlot)
		}},
	)
	return spec
}
