package scenarios

import (
	"context"
	"fmt"
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/liquidity"
	"devshard/cmd/gateway/nonces"
	"devshard/types"
)

// Test flow:
//  1. Boot with two starved escrows and let the first tick fund the guarantee's two full escrows.
//  2. Restart the gateway and advance two minutes.
//  3. Assert no further escrow was created, the starved ones still serve, and the restarted planner's full count equals the fleet's: the restart rebuilt the state with an empty demand window (`unsettled budget`, `inactive escrow unroutable` at every step).
func TestARestartOverStarvedEscrowsCreatesNoStorm(t *testing.T) {
	spec := plannerSpec()
	spec.seeded = repeatSeeded(2, 20_000, regularRole)
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		restartGateway{},
		advance{by: 2 * time.Minute},
		expectThat{label: "no create after the restart", verify: func(harness *gatewayHarness) error {
			if creates := len(harness.chain.createsOf(testModelID)); creates != 2 {
				return fmt.Errorf("%d creates, want the 2 guards before the restart", creates)
			}
			return seededServing(harness)
		}},
		expectFullCountMatchesThePlanner("the rebuilt planner counts the fleet's full escrows", spec.model),
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Boot with one starved escrow and lose the response to the first create broadcast.
//  2. Advance five seconds, short of the next tick, assert the create is on chain with no row yet, restart the gateway, and advance one minute.
//  3. Assert every escrow the chain created has exactly one active row and the full escrows do not exceed the guarantee: the commitment recovered the lost answer, the planner counted it, and nothing was created twice (`unsettled budget` at every step).
func TestACreateWhoseAnswerALostRestartIsRecoveredOnce(t *testing.T) {
	spec := plannerSpec()
	spec.seeded = repeatSeeded(1, 20_000, regularRole)
	spec.steps = []harnessStep{
		advance{by: 5 * time.Second},
		expectThat{label: "the restart lands between the broadcast and the row", verify: func(harness *gatewayHarness) error {
			creates := harness.chain.createsOf(testModelID)
			if len(creates) != 1 {
				return fmt.Errorf("%d creates before the restart, want the 1 whose answer was lost", len(creates))
			}
			if row, stored := rowOf(harness, formatEscrowID(creates[0].escrowID)); stored {
				return fmt.Errorf("escrow %d already has a row %+v before the restart, want none", creates[0].escrowID, row)
			}
			return nil
		}},
		restartGateway{},
		advance{by: time.Minute},
		expectThat{label: "every created escrow has exactly one active row", verify: func(harness *gatewayHarness) error {
			rows := harness.rows()
			for _, created := range harness.chain.createsOf(testModelID) {
				matching := 0
				for _, row := range rows {
					if row.EscrowID == formatEscrowID(created.escrowID) && row.Active {
						matching++
					}
				}
				if matching != 1 {
					return fmt.Errorf("escrow %d has %d active rows, want 1", created.escrowID, matching)
				}
			}
			if len(rows) != len(harness.chain.createsOf(testModelID))+1 {
				return fmt.Errorf("%d rows for %d creates and one seeded escrow", len(rows), len(harness.chain.createsOf(testModelID)))
			}
			return nil
		}},
		expectThat{label: "nothing created past the guarantee", verify: func(harness *gatewayHarness) error {
			if full := harness.fullEscrows(testModelID); full > spec.model.guaranteeSlots() {
				return fmt.Errorf("%d full escrows, want at most the guarantee's %d: the recovered escrow was not counted", full, spec.model.guaranteeSlots())
			}
			return nil
		}},
	}
	runStepsWith(t, spec, func(blockchain *fakeChain) { blockchain.loseNextResponse(operationBroadcastCreate) })
}

// Test flow:
//  1. Seed an escrow serving its last epoch and lose the response to the next settle broadcast.
//  2. Move inside the margin, advance twenty seconds past the tick that broadcast the settle, assert the broadcast is out and the row still stored, restart the gateway, and advance two minutes.
//  3. Assert the escrow settled once, from one broadcast the restarted gateway reconciled, and its row is gone (`settle deadline` at every step).
func TestASettleWhoseAnswerARestartLostIsNotSettledTwice(t *testing.T) {
	spec := lastEpochSpec()
	spec.model.fullContextSlots = 2
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		moveInsideTheMargin(),
		advance{by: 20 * time.Second},
		expectThat{label: "the restart lands between the broadcast and the reconcile", verify: func(harness *gatewayHarness) error {
			if _, stored := seededRowOf(harness); harness.chain.broadcastCount(operationBroadcastSettle) != 1 || !stored {
				return fmt.Errorf("%d settle broadcasts, row stored %v before the restart, want the lost broadcast and the row", harness.chain.broadcastCount(operationBroadcastSettle), stored)
			}
			return nil
		}},
		restartGateway{},
		advance{by: 2 * time.Minute},
		expectThat{label: "settled once from one broadcast, row gone", verify: func(harness *gatewayHarness) error {
			record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0]))
			if _, stored := seededRowOf(harness); !record.settled || harness.chain.broadcastCount(operationBroadcastSettle) != 1 || stored {
				return fmt.Errorf("settled %v after %d broadcasts, row stored %v, want one broadcast, settled, row gone", record.settled, harness.chain.broadcastCount(operationBroadcastSettle), stored)
			}
			return nil
		}},
	}
	runStepsWith(t, spec, func(blockchain *fakeChain) {
		switchedToEpochEight(blockchain)
		blockchain.loseNextResponse(operationBroadcastSettle)
	})
}

// Test flow:
//  1. Boot with a guarantee of one, stall every participant after its receipt, send two requests and advance six minutes; assert the report counts them late.
//  2. Restart the gateway, advance thirty seconds, and assert the report counts them late again: the classes were rebuilt from storage.
//  3. Advance thirty-five minutes and assert no Started record is left: the deadlines survived the restart (`active escrow routes` at every step).
func TestARestartRebuildsTheClassesOfStartedRecords(t *testing.T) {
	spec := plannerSpec()
	spec.model.fullContextSlots = 1
	late := func(label string) expectThat {
		return expectThat{label: label, verify: func(harness *gatewayHarness) error {
			report, _ := harness.fundingReport(testModelID)
			if report.Money[liquidity.ClassLate] == 0 {
				return fmt.Errorf("report money = %v, want the Started reservations late", report.Money)
			}
			return nil
		}}
	}
	spec.steps = []harnessStep{
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.stall = true }},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 2},
		advance{by: 6 * time.Minute},
		late("late before the restart"),
		restartGateway{},
		advance{by: 30 * time.Second},
		late("late after the restart"),
		advance{by: 35 * time.Minute},
		expectThat{label: "swept", verify: func(harness *gatewayHarness) error {
			if hasRecord(harness, harness.seededIDs[0], types.StatusStarted) {
				return fmt.Errorf("a Started record outlived its deadline across the restart")
			}
			return nil
		}},
	}
	runSteps(t, spec)
}

// Test flow:
//  1. Seed a temp the chain stamped with epoch 7 under label 8, move past set_new_validators, and leave the wallet empty so no bridge or regular can retire it.
//  2. Advance thirty seconds and assert the temp serves its last settle epoch.
//  3. Start PoC at 3 000, inside the margin of the switch at 3 100, restart the gateway at once, and advance two minutes.
//  4. Assert the restarted gateway settled the temp while the effective epoch is still 8.
//  5. Reach set_new_validators into epoch 9 and advance two minutes (`settle deadline` at every step).
func TestARestartAcrossTheEpochSwitchKeepsTheDeadline(t *testing.T) {
	spec := lastEpochSpec()
	spec.model.fullContextSlots = 2
	spec.walletBalance = 0
	spec.seeded = []seededEscrow{{amount: 1_000_000, role: tempRole}}
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		seededServesItsLastEpoch("precondition: the temp serves its last settle epoch"),
		moveChain{move: func(blockchain *fakeChain) { blockchain.startPoC() }},
		restartGateway{},
		advance{by: 2 * time.Minute},
		expectThat{label: "the restarted gateway settled the temp inside its last epoch", verify: func(harness *gatewayHarness) error {
			record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0]))
			if effective := harness.chain.snapshotEpoch().effective; !record.settled || effective != 8 {
				return fmt.Errorf("escrowRecord(%s).settled = %v at effective epoch %d, want settled while it is 8", harness.seededIDs[0], record.settled, effective)
			}
			return nil
		}},
		moveChain{move: func(blockchain *fakeChain) { blockchain.setNewValidators(4000, 4100) }},
		advance{by: 2 * time.Minute},
	}
	runStepsWith(t, spec, switchedToEpochEight)
}

// Test flow:
//  1. Boot with four full escrows of a target-1 model, restart the gateway after one minute, and send one small request every thirty seconds.
//  2. After nine minutes of traffic, assert all four still serve: the demand window is not full, so no surplus retire.
//  3. After four more minutes of traffic, assert at least one left service as surplus.
func TestNoSurplusRetireBeforeTheDemandWindowFills(t *testing.T) {
	spec := plannerSpec()
	spec.seeded = repeatSeeded(4, 1_000_000, regularRole)
	spec.steps = []harnessStep{advance{by: time.Minute}, restartGateway{}}
	traffic := func(minutes int) []harnessStep {
		steps := make([]harnessStep, 0, 4*minutes)
		for range 2 * minutes {
			steps = append(steps, sendRequests{promptBytes: 200, maxTokens: 64, count: 1, sequential: true}, advance{by: 30 * time.Second})
		}
		return steps
	}
	spec.steps = append(spec.steps, traffic(9)...)
	spec.steps = append(spec.steps, expectThat{label: "no surplus retire yet", verify: seededServing})
	spec.steps = append(spec.steps, traffic(4)...)
	spec.steps = append(spec.steps, expectThat{label: "surplus once the window filled", verify: func(harness *gatewayHarness) error {
		if err := seededServing(harness); err == nil {
			return fmt.Errorf("all four escrows still serve thirteen minutes after the restart, want a surplus retire")
		}
		return nil
	}})
	runSteps(t, spec)
}

// Test flow:
//  1. Table-driven: boot with one starved escrow over a chain whose snapshot lacks an epoch, either with no epoch index at all or naming epoch 8 with no block height yet, the escrow then of chain epoch 7 under label 8 with the switch 500 blocks ahead, inside its settle margin were the epoch known.
//  2. Advance two minutes.
//  3. Assert no deadline action: the escrow still serves, nothing was parked or broadcast for settlement, and no deadline was narrated.
//  4. Assert nothing was created.
func TestAColdStartWithoutAnEpochCreatesNothing(t *testing.T) {
	for _, testCase := range []struct {
		name  string
		epoch chainEpoch
	}{
		{name: "no epoch index", epoch: chainEpoch{latest: 0, effective: 0, blockHeight: 0, pocStart: 2000, setNewValidators: 2100, phase: chain.EpochPhaseInference}},
		{name: "no block height", epoch: chainEpoch{latest: 8, effective: 7, blockHeight: 0, pocStart: 400, setNewValidators: 500, phase: chain.EpochPhaseInference}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			spec := plannerSpec()
			spec.epoch = testCase.epoch
			spec.seeded = repeatSeeded(1, 20_000, regularRole)
			spec.steps = []harnessStep{
				advance{by: 2 * time.Minute},
				expectThat{label: "no deadline action without an epoch", verify: func(harness *gatewayHarness) error {
					row, stored := seededRowOf(harness)
					if !stored || !row.Active || row.SettlementPending || row.SettleTxHash != "" {
						return fmt.Errorf("seededRowOf() = %+v, stored %v, want stored, serving and never parked", row, stored)
					}
					if broadcasts := harness.chain.broadcastCount(operationBroadcastSettle); broadcasts != 0 {
						return fmt.Errorf("broadcastCount(settle) = %d, want 0", broadcasts)
					}
					if parked, narrated := harness.logLines("escrow parked at its settlement deadline"), harness.logLines("escrow deadline passes unsettled"); parked != 0 || narrated != 0 {
						return fmt.Errorf("logLines(parked at deadline) = %d, logLines(deadline passes unsettled) = %d, want 0 and 0", parked, narrated)
					}
					return seededServing(harness)
				}},
				expectThat{label: "no create without an epoch", verify: func(harness *gatewayHarness) error {
					if creates := harness.chain.createsOf(testModelID); len(creates) != 0 {
						return fmt.Errorf("createsOf(%s) = %d creates, want 0", testModelID, len(creates))
					}
					return nil
				}},
			}
			runSteps(t, spec)
		})
	}
}

const (
	participantInputTokens = 300
	slowAnswer             = 15 * time.Second
	beforeTheAnswers       = 12 * time.Second
	untilTheAnswersLand    = 4 * time.Second
	afterTheRestart        = 30 * time.Second
)

type ledgerTokens struct {
	input, output, counted, estimatedError uint64
}

func tokensOf(records []accounting.ParticipantRecord) ledgerTokens {
	var tokens ledgerTokens
	for _, record := range records {
		tokens.input += record.InputTokens
		tokens.output += record.OutputTokens
		tokens.counted += record.CountedNonces
		tokens.estimatedError += record.EstimatedError
	}
	return tokens
}

func ledgerTokensOf(harness *gatewayHarness) ledgerTokens {
	return tokensOf(harness.currentGateway().Nonces().Book().Query(accounting.QueryFilter{}))
}

func savedLedgerTokens(harness *gatewayHarness) (ledgerTokens, error) {
	saved, err := accounting.OpenStore(nonces.DatabasePath(harness.storageDir))
	if err != nil {
		return ledgerTokens{}, err
	}
	defer func() { _ = saved.Close() }()
	snapshot, err := saved.Load(context.Background())
	if err != nil {
		return ledgerTokens{}, err
	}
	book := accounting.NewBook(time.Now)
	if err := book.Restore(snapshot); err != nil {
		return ledgerTokens{}, err
	}
	return tokensOf(book.Query(accounting.QueryFilter{})), nil
}

func finishedRecordCount(harness *gatewayHarness) int {
	machine, built := harness.fleet.userMachine(harness.seededIDs[0])
	if !built {
		return 0
	}
	finished := 0
	for _, record := range machine.SnapshotState().Inferences {
		if record.Status == types.StatusFinished {
			finished++
		}
	}
	return finished
}

// Test flow:
//  1. Boot with one funded escrow whose participants answer after fifteen seconds and report 300 input tokens, send four requests at once and advance twelve seconds.
//  2. Assert the ledger's last sweep read the requests started: nothing counted yet, their byte estimates held as estimated error.
//  3. Let the answers land, then send four more requests one after another without advancing the clock, so the escrow holds finishes no sweep has read.
//  4. Restart the gateway on the same store without advancing the clock; while it is down, assert the ledger the shutdown saved counts the finishes.
//  5. Assert the restarted ledger, after its first sweep and after thirty more seconds, holds exactly the saved totals: nothing was lost or counted twice.
func TestARestartKeepsTheLedgersTokenCounts(t *testing.T) {
	spec := defaultSpec()
	spec.behaviours = everyParticipant(spec.participants, participantBehaviour{inputTokens: participantInputTokens, delay: slowAnswer})
	var saved ledgerTokens
	sameAsSaved := func(harness *gatewayHarness) error {
		if restarted := ledgerTokensOf(harness); restarted != saved {
			return fmt.Errorf("restarted ledger tokens %+v, want the %+v the shutdown saved", restarted, saved)
		}
		return nil
	}
	spec.steps = []harnessStep{
		sendRequests{promptBytes: 200, maxTokens: 64, count: 4},
		advance{by: beforeTheAnswers},
		expectThat{label: "the last sweep read the requests started", verify: func(harness *gatewayHarness) error {
			if read := ledgerTokensOf(harness); read.counted != 0 || read.estimatedError == 0 {
				return fmt.Errorf("ledger tokens %+v while the answers are outstanding, want nothing counted and an estimated error held", read)
			}
			return nil
		}},
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.delay = 0 }},
		advance{by: untilTheAnswersLand},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 4, sequential: true},
		expectThat{label: "the escrow holds finishes no sweep has read", verify: func(harness *gatewayHarness) error {
			if read, finished := ledgerTokensOf(harness), finishedRecordCount(harness); read.counted != 0 || finished == 0 {
				return fmt.Errorf("ledger counted %d with %d finished records on the escrow, want 0 counted and some finished", read.counted, finished)
			}
			return nil
		}},
		restartGateway{whileDown: expectThat{label: "the shutdown saved the finishes", verify: func(harness *gatewayHarness) error {
			var err error
			if saved, err = savedLedgerTokens(harness); err != nil {
				return err
			}
			if saved.counted == 0 || saved.input != saved.counted*participantInputTokens {
				return fmt.Errorf("saved ledger tokens %+v, want every finish counted with its %d input tokens", saved, participantInputTokens)
			}
			return nil
		}}},
		expectThat{label: "the restarted ledger holds the saved tokens", verify: sameAsSaved},
		advance{by: afterTheRestart},
		expectThat{label: "the restarted ledger still holds the saved tokens", verify: sameAsSaved},
	}
	runSteps(t, spec)
}
