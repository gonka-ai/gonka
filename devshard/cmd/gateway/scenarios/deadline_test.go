package scenarios

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/internal/logcapture"
)

const (
	prePoCBlocksEnv      = "DEVSHARD_ESCROW_ROTATION_PRE_POC_BLOCKS"
	rotationEnabledEnv   = "DEVSHARD_ESCROW_ROTATION_ENABLED"
	settlementEnabledEnv = "DEVSHARD_ESCROW_ROTATION_SETTLEMENT_ENABLED"
)

// lastEpochSpec seeds one escrow the chain stamped with epoch 7 during PoC, its row labelled 8; boot it through switchedToEpochEight so it serves its last settle epoch.
func lastEpochSpec() testSpec {
	spec := defaultSpec()
	spec.epoch = chainEpoch{latest: 8, effective: 7, blockHeight: 2050, pocStart: 2000, setNewValidators: 2100, phase: chain.EpochPhasePoCGenerate}
	return spec
}

func switchedToEpochEight(blockchain *fakeChain) { blockchain.setNewValidators(3000, 3100) }

func seededServesItsLastEpoch(label string) expectThat {
	return expectThat{label: label, verify: func(harness *gatewayHarness) error {
		row, stored := seededRowOf(harness)
		if !stored || !row.Active || row.ChainEpoch != 7 || row.RotationEpoch != 8 {
			return fmt.Errorf("row %+v, want serving with chain epoch 7 under label 8", row)
		}
		return nil
	}}
}

func seededEscrowSettled(label string) expectThat {
	return expectThat{label: label, verify: func(harness *gatewayHarness) error {
		record, known := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0]))
		if !known || !record.settled {
			return fmt.Errorf("escrow %s of chain epoch %d unsettled at effective epoch %d, want settled", harness.seededIDs[0], record.epochIndex, harness.chain.snapshotEpoch().effective)
		}
		return nil
	}}
}

func moveInsideTheMargin() harnessStep {
	return moveChain{move: func(blockchain *fakeChain) { blockchain.moveToHeight(2600) }}
}

// crossTheNextSwitch moves the chain into epoch 9, the escrow's epoch + 2, where `settle deadline` requires it settled.
func crossTheNextSwitch() []harnessStep {
	return []harnessStep{
		moveChain{move: func(blockchain *fakeChain) { blockchain.startPoC() }},
		advance{by: escrow.TickInterval},
		moveChain{move: func(blockchain *fakeChain) { blockchain.setNewValidators(4000, 4100) }},
		advance{by: time.Minute},
	}
}

func deadlineLines(harness *gatewayHarness, logged *logcapture.Recorder, reason string) int {
	harness.currentGateway().Journal().Flush()
	lines := 0
	for _, entry := range logged.All() {
		if entry.Msg == "escrow deadline passes unsettled" && logcapture.Field(entry, "reason") == reason {
			lines++
		}
	}
	return lines
}

// Test flow:
//  1. Seed an escrow the chain stamped with epoch 7 during PoC under row label 8, move past set_new_validators, and leave the wallet empty so no bridge can replace it.
//  2. Advance thirty seconds and assert the row resolved chain epoch 7 under label 8 and serves.
//  3. Move into the bridge window, which is also inside the settle margin of the switch into epoch 9, and advance two minutes.
//  4. Assert the escrow settled, then cross the switch into epoch 9 (`settle deadline` holds every step).
func TestAnEscrowCreatedDuringPoCSettlesByItsChainEpoch(t *testing.T) {
	spec := lastEpochSpec()
	spec.walletBalance = 0
	spec.steps = slices.Concat([]harnessStep{
		advance{by: 30 * time.Second},
		seededServesItsLastEpoch("precondition: chain epoch 7 under row label 8"),
		moveChain{move: func(blockchain *fakeChain) { blockchain.moveToHeight(2850) }},
		advance{by: 2 * time.Minute},
		seededEscrowSettled("settled by its chain epoch's deadline"),
	}, crossTheNextSwitch())
	runStepsWith(t, spec, switchedToEpochEight)
}

// Test flow:
//  1. Seed a temp the chain stamped with epoch 7 under label 8, move past set_new_validators, and leave the wallet empty so the finish can create no regular to retire it.
//  2. Advance thirty seconds and assert the temp still serves its last settle epoch.
//  3. Move inside the settle margin of the switch into epoch 9, before the next bridge window, and advance two minutes.
//  4. Assert the temp settled, then cross the switch (`settle deadline` holds every step).
func TestATempThatSurvivedItsBridgeIsRetiredAtItsDeadline(t *testing.T) {
	spec := lastEpochSpec()
	spec.walletBalance = 0
	spec.seeded = []seededEscrow{{amount: 1_000_000, role: tempRole}}
	spec.steps = slices.Concat([]harnessStep{
		advance{by: 30 * time.Second},
		seededServesItsLastEpoch("precondition: the temp survived its bridge"),
		moveInsideTheMargin(),
		advance{by: 2 * time.Minute},
		seededEscrowSettled("retired at its deadline margin"),
	}, crossTheNextSwitch())
	runStepsWith(t, spec, switchedToEpochEight)
}

// Test flow:
//  1. Seed an escrow of chain epoch 7 under label 8, move past set_new_validators, hide the effective epoch, and turn the bridge window off.
//  2. Advance thirty seconds and assert the snapshot's effective epoch is unknown and the escrow serves.
//  3. Move inside the settle margin and advance two minutes.
//  4. Assert the escrow settled before the switch, then cross it (`settle deadline` holds every step).
func TestAnUnknownEffectiveEpochOutsidePoCStillSettlesBeforeTheSwitch(t *testing.T) {
	spec := lastEpochSpec()
	spec.environment = map[string]string{prePoCBlocksEnv: "0"}
	spec.steps = slices.Concat([]harnessStep{
		advance{by: 30 * time.Second},
		expectThat{label: "precondition: the effective epoch is unknown", verify: func(harness *gatewayHarness) error {
			if effective := harness.currentGateway().Observer().Snapshot().EffectiveEpochIndex; effective != 0 {
				return fmt.Errorf("snapshot effective epoch %d, want unknown (0): the scenario proves nothing", effective)
			}
			return nil
		}},
		seededServesItsLastEpoch("precondition: serving its last settle epoch"),
		moveInsideTheMargin(),
		advance{by: 2 * time.Minute},
		seededEscrowSettled("settled before the switch"),
	}, crossTheNextSwitch())
	runStepsWith(t, spec, func(blockchain *fakeChain) {
		switchedToEpochEight(blockchain)
		blockchain.hideEffectiveEpoch()
	})
}

// Test flow:
//  1. Seed an escrow of chain epoch 7 under label 8 inside PoC (latest 8, effective really 7), hide the effective epoch, and turn the bridge window off.
//  2. Advance thirty seconds.
//  3. Assert the escrow was parked at a deadline and settled while the chain's effective epoch is still 7: an unknown effective epoch reads as the latest, so the deadline is read one epoch early.
//  4. Move through set_new_validators and the switch after it (`settle deadline` holds every step).
func TestAnUnknownEffectiveEpochReadsDeadlinesEarly(t *testing.T) {
	logged := logcapture.Install(t)
	spec := lastEpochSpec()
	spec.environment = map[string]string{prePoCBlocksEnv: "0"}
	spec.steps = slices.Concat([]harnessStep{
		advance{by: 30 * time.Second},
		expectThat{label: "read early and settled", verify: func(harness *gatewayHarness) error {
			if effective := harness.currentGateway().Observer().Snapshot().EffectiveEpochIndex; effective != 0 {
				return fmt.Errorf("snapshot effective epoch %d, want unknown (0): the scenario proves nothing", effective)
			}
			harness.currentGateway().Journal().Flush()
			if _, found := logged.Find("escrow parked at its settlement deadline"); !found {
				return errors.New("no escrow parked at its settlement deadline")
			}
			record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0]))
			if effective := harness.chain.snapshotEpoch().effective; !record.settled || effective != 7 {
				return fmt.Errorf("settled %v at chain effective epoch %d, want settled while it is still 7", record.settled, effective)
			}
			return nil
		}},
		moveChain{move: switchedToEpochEight},
		advance{by: time.Minute},
	}, crossTheNextSwitch())
	runStepsWith(t, spec, func(blockchain *fakeChain) { blockchain.hideEffectiveEpoch() })
}

// Test flow:
//  1. Seed an escrow of chain epoch 7 under label 8, move past set_new_validators, and turn rotation off.
//  2. Move inside the settle margin and advance two minutes.
//  3. Assert the escrow settled, then cross the switch (`settle deadline` holds every step).
func TestTheDeadlineSettleRunsWithRotationOff(t *testing.T) {
	spec := lastEpochSpec()
	spec.environment = map[string]string{rotationEnabledEnv: "false"}
	spec.steps = slices.Concat([]harnessStep{
		advance{by: 30 * time.Second},
		seededServesItsLastEpoch("precondition: serving its last settle epoch"),
		moveInsideTheMargin(),
		advance{by: 2 * time.Minute},
		seededEscrowSettled("settled with rotation off"),
	}, crossTheNextSwitch())
	runStepsWith(t, spec, switchedToEpochEight)
}

// Test flow:
//  1. Seed an escrow of chain epoch 7 under label 8, move past set_new_validators, and set the models list to a value that does not parse.
//  2. Move inside the settle margin and advance two minutes.
//  3. Assert the escrow settled although the list never parsed, then cross the switch (`settle deadline` holds every step).
func TestTheDeadlineSettleRunsWithAnUnparsableModelsList(t *testing.T) {
	spec := lastEpochSpec()
	spec.environment = map[string]string{"DEVSHARD_ESCROW_ROTATION_MODELS_JSON": "not json"}
	spec.steps = slices.Concat([]harnessStep{
		advance{by: 30 * time.Second},
		seededServesItsLastEpoch("precondition: serving its last settle epoch"),
		moveInsideTheMargin(),
		advance{by: 2 * time.Minute},
		seededEscrowSettled("settled with no parsed models"),
	}, crossTheNextSwitch())
	runStepsWith(t, spec, switchedToEpochEight)
}

// Test flow:
//  1. Capture the log, seed an escrow of chain epoch 7 under label 8, move past set_new_validators, and turn settlement off.
//  2. Move inside the settle margin and advance two minutes.
//  3. Assert the escrow still serves, is not settled, and was narrated as settlement_disabled exactly once.
func TestWithSettlementOffAnEscrowAtItsMarginIsNarratedNotParked(t *testing.T) {
	logged := logcapture.Install(t)
	spec := lastEpochSpec()
	spec.environment = map[string]string{settlementEnabledEnv: "false"}
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		moveInsideTheMargin(),
		advance{by: 2 * time.Minute},
		expectThat{label: "narrated settlement_disabled once, not parked", verify: func(harness *gatewayHarness) error {
			row, stored := seededRowOf(harness)
			record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0]))
			if !stored || !row.Active || row.SettlementPending || record.settled {
				return fmt.Errorf("row %+v settled %v, want still serving and unsettled", row, record.settled)
			}
			if lines := deadlineLines(harness, logged, "settlement_disabled"); lines != 1 {
				return fmt.Errorf("%d settlement_disabled deadline lines, want 1", lines)
			}
			return nil
		}},
	}
	runStepsWith(t, spec, switchedToEpochEight)
}

// Test flow:
//  1. Seed an escrow of chain epoch 7 under label 8, move past set_new_validators, and script the chain to answer the next settle broadcast without ever committing it.
//  2. Move inside the settle margin, advance two minutes, and assert one settle was broadcast and nothing settled.
//  3. Advance twelve minutes, past the transaction's TTL, and assert a second settle was broadcast and the escrow settled.
//  4. Cross the switch (`settle deadline` holds every step).
func TestASettleThatNeverCommitsIsRebroadcastBeforeTheDeadline(t *testing.T) {
	spec := lastEpochSpec()
	spec.steps = slices.Concat([]harnessStep{
		advance{by: 30 * time.Second},
		moveInsideTheMargin(),
		advance{by: 2 * time.Minute},
		expectThat{label: "precondition: one settle broadcast, never committed", verify: func(harness *gatewayHarness) error {
			record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0]))
			if broadcasts := harness.chain.broadcastCount(operationBroadcastSettle); broadcasts != 1 || record.settled {
				return fmt.Errorf("%d settle broadcasts, settled %v, want 1 and unsettled", broadcasts, record.settled)
			}
			return nil
		}},
		advance{by: 12 * time.Minute},
		expectThat{label: "rebroadcast after its TTL", verify: func(harness *gatewayHarness) error {
			if broadcasts := harness.chain.broadcastCount(operationBroadcastSettle); broadcasts != 2 {
				return fmt.Errorf("%d settle broadcasts, want 2", broadcasts)
			}
			return nil
		}},
		seededEscrowSettled("settled before the deadline"),
	}, crossTheNextSwitch())
	runStepsWith(t, spec, func(blockchain *fakeChain) {
		switchedToEpochEight(blockchain)
		blockchain.neverCommitNext(operationBroadcastSettle, 1)
	})
}

// Test flow:
//  1. Capture the log, seed an escrow of chain epoch 7 under label 8, move past set_new_validators, and script the chain never to commit a settle, the bridge temp's included; the scenario is exempt from `settlement match` and `settle deadline`.
//  2. Move inside the settle margin, advance two minutes, and cross the switch into epoch 9, where the chain prunes it.
//  3. Advance fifteen minutes.
//  4. Assert the loss was narrated as deadline_passed, the escrow is pruned unsettled, and its row is dropped.
func TestASettleTheChainNeverTakesIsNarratedAndItsRowDropped(t *testing.T) {
	logged := logcapture.Install(t)
	spec := lastEpochSpec()
	spec.allowUnsettled = true
	spec.steps = slices.Concat([]harnessStep{
		advance{by: 30 * time.Second},
		moveInsideTheMargin(),
		advance{by: 2 * time.Minute},
	}, crossTheNextSwitch(), []harnessStep{
		advance{by: 15 * time.Minute},
		expectThat{label: "exempt, narrated and dropped", verify: func(harness *gatewayHarness) error {
			if lines := deadlineLines(harness, logged, "deadline_passed"); lines != 1 {
				return fmt.Errorf("%d deadline_passed lines, want 1", lines)
			}
			record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0]))
			if _, stored := seededRowOf(harness); stored || !record.pruned || record.settled {
				return fmt.Errorf("row stored %v, pruned %v, settled %v, want the row dropped and the escrow pruned unsettled", stored, record.pruned, record.settled)
			}
			return nil
		}},
	})
	runStepsWith(t, spec, func(blockchain *fakeChain) {
		switchedToEpochEight(blockchain)
		blockchain.neverCommitNext(operationBroadcastSettle, 1000)
	})
}
