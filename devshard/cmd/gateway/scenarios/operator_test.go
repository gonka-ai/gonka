package scenarios

import (
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/funding"
)

// Test flow:
//  1. Table-driven over a model of amount 100 000 and a guarantee of two, every answer billed at 5 000 input tokens, with one case per repair term.
//  2. For the guarantee alone, seed two escrows of 70 000, just full, beside six starved escrows of 65 000 whose money past a full-context request (about 192 700) keeps the fleet's money over its need (131 112 for the two slots plus half again the two requests' reservations, about 229 000) once both full ones fall.
//  3. For capacity alone, seed three escrows of 100 100 and let the hosts answer after thirty seconds, so in-flight reservations become demand above the fleet's money while at least two escrows stay full.
//  4. Advance thirty seconds and assert nothing was created; send the case's traffic: two sequential 32 500-byte requests, too large for a starved escrow to pay twice, that land on the full escrows in turn, or four concurrent 6 000-byte requests and their hedges.
//  5. Assert the case's precondition (both full escrows fell below full, or at least two escrows are still full) and advance one minute.
//  6. Assert the case's own term repaired the fleet: for the guarantee, exactly its two guard creates and two full escrows; for capacity, one or more capacity creates and no guard create; every planner create within the bucket and judged by `create reason` at every step.
//  7. Let the hosts answer at once, send one full-context request and assert it was answered 200.
func TestEscrowsFallingBelowFullAreRepairedWithinTheBucket(t *testing.T) {
	const slot = 65_556
	for _, testCase := range []struct {
		name         string
		seeded       []seededEscrow
		maxUnsettled int
		answerDelay  time.Duration
		traffic      []harnessStep
		precondition func(harness *gatewayHarness) error
		repaired     func(harness *gatewayHarness) error
	}{
		{
			name:         "the guarantee alone",
			seeded:       append(repeatSeeded(2, 70_000, regularRole), repeatSeeded(6, 65_000, regularRole)...),
			maxUnsettled: 12,
			traffic: []harnessStep{
				sendRequests{promptBytes: 32_500, maxTokens: 64, count: 1, sequential: true},
				sendRequests{promptBytes: 32_500, maxTokens: 64, count: 1, sequential: true},
			},
			precondition: func(harness *gatewayHarness) error {
				for _, escrowID := range harness.seededIDs[:2] {
					if balance := harness.userBalance(escrowID); balance >= slot {
						return fmt.Errorf("userBalance(%s) = %d, want below the slot of %d: the scenario proves nothing", escrowID, balance, slot)
					}
				}
				return nil
			},
			repaired: func(harness *gatewayHarness) error {
				if guardCreates, creates := harness.createdWithReason(testModelID, string(funding.ReasonGuard)), len(harness.chain.createsOf(testModelID)); guardCreates != 2 || creates != 2 {
					return fmt.Errorf("createdWithReason(guard) = %d of %d creates, want exactly the guarantee's 2", guardCreates, creates)
				}
				if full := harness.fullEscrows(testModelID); full != 2 {
					return fmt.Errorf("fullEscrows(%s) = %d, want the guarantee of 2 repaired", testModelID, full)
				}
				return nil
			},
		},
		{
			name:        "capacity alone",
			seeded:      repeatSeeded(3, 100_100, regularRole),
			answerDelay: 30 * time.Second,
			traffic:     []harnessStep{sendRequests{promptBytes: 6_000, maxTokens: 64, count: 4}},
			precondition: func(harness *gatewayHarness) error {
				if full := harness.fullEscrows(testModelID); full < 2 {
					return fmt.Errorf("fullEscrows(%s) = %d, want at least the guarantee of 2 still full: the guard term would want creates too", testModelID, full)
				}
				return nil
			},
			repaired: func(harness *gatewayHarness) error {
				if capacityCreates, guardCreates := harness.createdWithReason(testModelID, string(funding.ReasonCapacity)), harness.createdWithReason(testModelID, string(funding.ReasonGuard)); capacityCreates == 0 || guardCreates != 0 {
					return fmt.Errorf("createdWithReason(capacity) = %d, createdWithReason(guard) = %d, want capacity creates alone", capacityCreates, guardCreates)
				}
				return plannerReasonsOnly(harness, funding.ReasonCapacity)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			spec := plannerSpec()
			spec.model.amount, spec.model.maxUnsettled = 100_000, testCase.maxUnsettled
			spec.seeded = testCase.seeded
			spec.behaviours = everyParticipant(4, participantBehaviour{inputTokens: 5_000, delay: testCase.answerDelay})
			spec.steps = []harnessStep{
				advance{by: 30 * time.Second},
				expectThat{label: "nothing created at boot", verify: func(harness *gatewayHarness) error {
					if creates := len(harness.chain.createsOf(testModelID)); creates != 0 {
						return fmt.Errorf("%d creates at boot, want 0", creates)
					}
					return nil
				}},
			}
			spec.steps = append(spec.steps, testCase.traffic...)
			spec.steps = append(spec.steps,
				expectThat{label: "precondition: the fleet as the case needs it", verify: testCase.precondition},
				advance{by: time.Minute},
				expectThat{label: "repaired by the case's own term", verify: testCase.repaired},
				expectPlannerCreatesWithinBucket("within the bucket", testModelID),
				changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.delay = 0 }},
				advance{by: time.Minute},
				sendRequests{promptBytes: 30_000, maxTokens: 64, count: 1, sequential: true},
				expectThat{label: "a full-context request served", verify: func(harness *gatewayHarness) error {
					statuses := harness.answeredStatuses()
					if len(statuses) == 0 || statuses[len(statuses)-1] != http.StatusOK {
						return fmt.Errorf("statuses = %v, want the full-context request answered 200", statuses)
					}
					return nil
				}},
			)
			runSteps(t, spec)
		})
	}
}

// Test flow:
//  1. Boot with two full escrows and three starved ones of 30 000, 40 000 and 50 000, five escrows the chain holds.
//  2. Lower max_unsettled to 3 and advance two minutes.
//  3. Assert nothing was created, the 30 000 and 40 000 escrows were parked and settled in that order at least a tick apart, and the full escrows and the 50 000 one still serve.
func TestALoweredBudgetParksTheLeastFreeStarvedOnePerTick(t *testing.T) {
	spec := plannerSpec()
	spec.seeded = []seededEscrow{
		{amount: 1_000_000, role: regularRole}, {amount: 1_000_000, role: regularRole},
		{amount: 30_000, role: regularRole}, {amount: 40_000, role: regularRole}, {amount: 50_000, role: regularRole},
	}
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		reconfigureModels{change: func(entries []map[string]any) { entries[0]["max_unsettled"] = 3 }},
		advance{by: 2 * time.Minute},
		expectThat{label: "the least free starved escrows parked one per tick", verify: func(harness *gatewayHarness) error {
			if creates := harness.chain.createsOf(testModelID); len(creates) != 0 {
				return fmt.Errorf("%d creates over a lowered budget, want 0", len(creates))
			}
			first, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[2]))
			second, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[3]))
			if !first.settled || !second.settled || second.settledAt.Sub(first.settledAt) < escrow.TickInterval {
				return fmt.Errorf("30 000 settled %v at %s, 40 000 settled %v at %s, want both, a tick apart, least free first", first.settled, first.settledAt, second.settled, second.settledAt)
			}
			for _, index := range []int{0, 1, 4} {
				if row, stored := rowOf(harness, harness.seededIDs[index]); !stored || !row.Active {
					return fmt.Errorf("escrow %s = %+v, want still serving", harness.seededIDs[index], row)
				}
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with four full escrows of a target-4 model and send one small request every thirty seconds for eleven minutes, so the demand window fills.
//  2. Lower target_count to 2 and keep the traffic for two minutes.
//  3. Assert at least one escrow left service as surplus, retires came at least a tick apart, nothing was created, and the guarantee's two full escrows remain.
func TestALoweredTargetRetiresSurplusOnePerTick(t *testing.T) {
	spec := plannerSpec()
	spec.model.targetCount = 4
	spec.seeded = repeatSeeded(4, 1_000_000, regularRole)
	for range 22 {
		spec.steps = append(spec.steps, sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true}, advance{by: 30 * time.Second})
	}
	spec.steps = append(spec.steps, reconfigureModels{change: func(entries []map[string]any) { entries[0]["target_count"] = 2 }})
	for range 4 {
		spec.steps = append(spec.steps, sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true}, advance{by: 30 * time.Second})
	}
	spec.steps = append(spec.steps, expectThat{label: "surplus retired one per tick", verify: func(harness *gatewayHarness) error {
		var settled []time.Time
		for _, record := range harness.chain.escrowsOf(testModelID) {
			if record.settled {
				settled = append(settled, record.settledAt)
			}
		}
		if len(settled) == 0 || len(harness.chain.createsOf(testModelID)) != 0 {
			return fmt.Errorf("%d surplus settles, %d creates, want at least one settle and no create", len(settled), len(harness.chain.createsOf(testModelID)))
		}
		slices.SortFunc(settled, func(left, right time.Time) int { return left.Compare(right) })
		for index := 1; index < len(settled); index++ {
			if settled[index].Sub(settled[index-1]) < escrow.TickInterval {
				return fmt.Errorf("two surplus retires %s apart, want at least a tick", settled[index].Sub(settled[index-1]))
			}
		}
		if full := harness.fullEscrows(testModelID); full < 2 {
			return fmt.Errorf("%d full escrows, want the guarantee of 2 kept", full)
		}
		return nil
	}})
	runSteps(t, spec)
}

// Test flow:
//  1. Table-driven over a model of amount 1 000 000 with a guarantee of two, both seeded escrows full at the default answer cap (slot 65 556): two escrows of 500 000 with the cap raised to 300 000 tokens (slot 600 020, which a fresh escrow still pays with its answer), and two escrows of 1 000 000 with the cap raised to 600 000 (slot 1 200 020, which no escrow of the amount pays).
//  2. Advance thirty seconds and assert nothing was created; raise the cap and advance two minutes.
//  3. Assert, for the payable slot, that the seeded escrows fell below the grown slot, the re-evaluated guarantee bought exactly its two guard creates within the bucket and the fleet then counts two full escrows; for the unpayable slot, that the misconfiguration was narrated once and nothing was created.
//  4. Advance two more minutes and assert no further create: the re-evaluated fleet does not storm.
func TestARaisedAnswerCapReevaluatesWithoutAStorm(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		seededAmount uint64
		raisedCap    int64
		verify       func(harness *gatewayHarness) error
	}{
		{name: "a fresh escrow pays the grown slot", seededAmount: 500_000, raisedCap: 300_000, verify: func(harness *gatewayHarness) error {
			for _, escrowID := range harness.seededIDs {
				if balance := harness.userBalance(escrowID); balance >= 600_020 {
					return fmt.Errorf("userBalance(%s) = %d, want below the grown slot of 600 020: the scenario proves nothing", escrowID, balance)
				}
			}
			if guardCreates, creates := harness.createdWithReason(testModelID, string(funding.ReasonGuard)), len(harness.chain.createsOf(testModelID)); guardCreates != 2 || creates != 2 {
				return fmt.Errorf("createdWithReason(guard) = %d of %d creates, want exactly the guarantee's 2", guardCreates, creates)
			}
			if full := harness.fullEscrows(testModelID); full != 2 {
				return fmt.Errorf("fullEscrows(%s) = %d, want the guarantee of 2 at the grown slot", testModelID, full)
			}
			return bucketViolation(harness.plannerCreateTimes(testModelID), harness.bootedAt)
		}},
		{name: "no escrow of the amount pays the grown slot", seededAmount: 1_000_000, raisedCap: 600_000, verify: func(harness *gatewayHarness) error {
			if lines := harness.logLines("model amount cannot fund a full escrow"); lines != 1 {
				return fmt.Errorf("logLines(model amount cannot fund a full escrow) = %d, want 1", lines)
			}
			if creates := len(harness.chain.createsOf(testModelID)); creates != 0 {
				return fmt.Errorf("%d creates, want 0 while the model is misconfigured", creates)
			}
			return nil
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			settledCreates := 0
			spec := plannerSpec()
			spec.seeded = repeatSeeded(2, testCase.seededAmount, regularRole)
			spec.steps = []harnessStep{
				advance{by: 30 * time.Second},
				expectThat{label: "nothing created before the rise", verify: func(harness *gatewayHarness) error {
					if creates := len(harness.chain.createsOf(testModelID)); creates != 0 {
						return fmt.Errorf("%d creates before the rise, want 0", creates)
					}
					return nil
				}},
				reconfigure{change: func(configuration *config.Config) { configuration.Limits.MaxTokensCap = testCase.raisedCap }},
				advance{by: 2 * time.Minute},
				expectThat{label: "the guarantee re-evaluated at the grown slot", verify: testCase.verify},
				expectThat{label: "count the creates", verify: func(harness *gatewayHarness) error {
					settledCreates = len(harness.chain.createsOf(testModelID))
					return nil
				}},
				advance{by: 2 * time.Minute},
				expectThat{label: "no storm after the re-evaluation", verify: func(harness *gatewayHarness) error {
					if creates := len(harness.chain.createsOf(testModelID)); creates != settledCreates {
						return fmt.Errorf("%d creates, want the %d of two minutes earlier", creates, settledCreates)
					}
					return nil
				}},
			}
			runSteps(t, spec)
		})
	}
}

// Test flow:
//  1. Seed an escrow serving its last epoch under a guarantee of two with an empty wallet, and advance thirty seconds; assert the guarantee's second escrow is still missing: nothing was created and the broken guarantee was narrated.
//  2. Remove every model from rotation, then fund the wallet, so a create would now succeed if the model were still planned.
//  3. Send one small request, move inside the margin and advance three minutes.
//  4. Assert the request was answered 200, nothing was created after the removal, and the deadline rule settled the escrow (`settle deadline` at every step).
func TestAModelRemovedFromRotationServesThenSettlesAtItsDeadline(t *testing.T) {
	spec := lastEpochSpec()
	spec.model.fullContextSlots = 2
	spec.walletBalance = 0
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		expectThat{label: "precondition: the guarantee is short when the model is removed", verify: func(harness *gatewayHarness) error {
			if creates, broken := len(harness.chain.createsOf(testModelID)), harness.logLines("funding guarantee broken"); creates != 0 || broken != 1 {
				return fmt.Errorf("%d creates and %d broken-guarantee lines, want 0 and 1: the scenario proves nothing", creates, broken)
			}
			return nil
		}},
		reconfigure{change: func(configuration *config.Config) { configuration.Rotation.ModelsJSON = "[]" }},
		fundWallet{amount: 100_000_000},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true},
		moveInsideTheMargin(),
		advance{by: 3 * time.Minute},
		seededEscrowSettled("settled at its deadline"),
		expectThat{label: "nothing created after the removal and the request answered", verify: func(harness *gatewayHarness) error {
			if creates := len(harness.chain.createsOf(testModelID)); creates != 0 {
				return fmt.Errorf("%d creates after the removal, want none", creates)
			}
			return everyAnswer(1, http.StatusOK)(harness)
		}},
	}
	runStepsWith(t, spec, switchedToEpochEight)
}

// Test flow:
//  1. Seed an escrow serving its last epoch with a guarantee of one, and have the operator deactivate it.
//  2. Advance thirty seconds; assert the row is inactive, not parked, and still counted in the model's report.
//  3. Move inside the margin and advance one minute; assert it was narrated operator_deactivated once and left unsettled (`unsettled budget` at every step).
func TestAnOperatorDeactivatedEscrowIsCountedAndNarratedAtItsMargin(t *testing.T) {
	spec := lastEpochSpec()
	spec.model.fullContextSlots = 1
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		operatorDeactivates{escrowID: seededID(0)},
		advance{by: 30 * time.Second},
		expectThat{label: "counted", verify: func(harness *gatewayHarness) error {
			row, stored := seededRowOf(harness)
			report, _ := harness.fundingReport(testModelID)
			if !stored || row.Active || row.SettlementPending || report.Counts[escrow.CountInactive] != 1 {
				return fmt.Errorf("row %+v, inactive count %d, want deactivated, not parked, and counted", row, report.Counts[escrow.CountInactive])
			}
			return nil
		}},
		moveInsideTheMargin(),
		advance{by: time.Minute},
		expectThat{label: "narrated", verify: func(harness *gatewayHarness) error {
			counted := harness.currentGateway().Manager().DeadlineUnsettledCounts()[escrow.DeadlineUnsettled{Model: testModelID, Reason: "operator_deactivated"}]
			if record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0])); record.settled || counted != 1 {
				return fmt.Errorf("settled %v, operator_deactivated counted %d, want unsettled and narrated once", record.settled, counted)
			}
			return nil
		}},
	}
	runStepsWith(t, spec, switchedToEpochEight)
}

// Test flow:
//  1. Boot over the seeded row the operator registered with no chain epoch and no amount.
//  2. Advance one minute.
//  3. Assert the row resolved chain epoch 7 and amount 1 000 000 and the model's report counts it.
func TestAnOperatorRegisteredRowResolvesLazilyAndIsCounted(t *testing.T) {
	spec := plannerSpec()
	spec.steps = []harnessStep{
		advance{by: time.Minute},
		expectThat{label: "the operator's row resolved and counted", verify: func(harness *gatewayHarness) error {
			row, stored := seededRowOf(harness)
			report, _ := harness.fundingReport(testModelID)
			if !stored || row.ChainEpoch != 7 || row.Amount != 1_000_000 || report.Counts[escrow.CountFull] == 0 {
				return fmt.Errorf("row %+v, report counts %v, want resolved and counted", row, report.Counts)
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with two full escrows, then have the operator create one escrow and force-settle the second seeded one.
//  2. Advance one minute.
//  3. Assert the operator's create was narrated operator (`create reason` exempts it), the second seeded escrow settled from one broadcast, and the planner created nothing (`create reason` at every step).
func TestOperatorActionsBesideThePlanner(t *testing.T) {
	spec := plannerSpec()
	spec.seeded = repeatSeeded(2, 1_000_000, regularRole)
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		operatorCreates{},
		operatorSettles{escrowID: seededID(1), force: true},
		advance{by: time.Minute},
		expectThat{label: "one operator create, no planner create, one settle broadcast", verify: func(harness *gatewayHarness) error {
			if operators := harness.createdWithReason(testModelID, "operator"); operators != 1 || len(harness.plannerCreateTimes(testModelID)) != 0 {
				return fmt.Errorf("%d operator creates, %d planner creates, want one and none", operators, len(harness.plannerCreateTimes(testModelID)))
			}
			record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[1]))
			if !record.settled || harness.chain.broadcastCount(operationBroadcastSettle) != 1 {
				return fmt.Errorf("settled %v after %d settle broadcasts, want one", record.settled, harness.chain.broadcastCount(operationBroadcastSettle))
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with one full escrow and one starved escrow of 60 000, every answer billed at 300 input tokens, and let the first tick fund the guarantee's second full escrow.
//  2. Send one small request every thirty seconds for two hours: about 350 a request, enough to spend the residue inside the run.
//  3. Assert the guarantee's two full escrows remain, the starved residue was spent (below 1 000 whether or not it settled: an idle retire refunds it unspent), and the budget never saturated: at most five unsettled escrows (`unsettled budget` at every step).
func TestTwoHoursOfSmallTrafficKeepTheGuaranteeAndSpendTheResidue(t *testing.T) {
	spec := plannerSpec()
	spec.seeded = []seededEscrow{{amount: 1_000_000, role: regularRole}, {amount: 60_000, role: regularRole}}
	spec.behaviours = everyParticipant(4, participantBehaviour{inputTokens: 300})
	spec.steps = []harnessStep{advance{by: 30 * time.Second}}
	for range 240 {
		spec.steps = append(spec.steps, sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true}, advance{by: 30 * time.Second})
	}
	spec.steps = append(spec.steps, expectThat{label: "the guarantee kept and the residue spent", verify: func(harness *gatewayHarness) error {
		if full := harness.fullEscrows(testModelID); full < 2 {
			return fmt.Errorf("%d full escrows, want the guarantee of 2", full)
		}
		machine, built := harness.fleet.userMachine(harness.seededIDs[1])
		if !built || machine.Balance() >= 1_000 {
			record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[1]))
			return fmt.Errorf("starved escrow built %v, settled %v, holds %d, want its residue spent below 1 000 whether or not it settled", built, record.settled, harness.userBalance(harness.seededIDs[1]))
		}
		unsettled := 0
		for _, held := range harness.chain.escrowsOf(testModelID) {
			if !held.settled && !held.pruned {
				unsettled++
			}
		}
		if unsettled > 5 {
			return fmt.Errorf("%d unsettled escrows, want at most 5", unsettled)
		}
		return nil
	}})
	runSteps(t, spec)
}
