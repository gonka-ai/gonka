package scenarios

import (
	"fmt"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/store"
)

func seededRowOf(harness *gatewayHarness) (store.DevshardRecord, bool) {
	for _, row := range harness.rows() {
		if row.EscrowID == harness.seededIDs[0] {
			return row, true
		}
	}
	return store.DevshardRecord{}, false
}

func seededRowUnresolved(label string) expectThat {
	return expectThat{label: label, verify: func(harness *gatewayHarness) error {
		row, stored := seededRowOf(harness)
		if !stored || !row.Active || row.ChainEpoch != 0 || row.Amount != 0 {
			return fmt.Errorf("row %+v, want active with chain epoch and amount still unresolved", row)
		}
		return nil
	}}
}

// Test flow:
//  1. Take the chain's escrow lookups down before boot, so the seeded row's new columns cannot be resolved.
//  2. Advance one minute and assert the row is still unresolved and serving.
//  3. Restart the gateway, advance one minute and assert the same.
//  4. Bring the lookups back, advance two ticks and assert the row now carries chain epoch 7 and amount 1 000 000 (`settle deadline` and `unsettled budget` hold every step).
func TestARestartBeforeTheNewColumnsAreResolvedResolvesThemLazily(t *testing.T) {
	spec := defaultSpec()
	spec.steps = []harnessStep{
		advance{by: time.Minute},
		seededRowUnresolved("precondition: the lookups are down, so the row is unresolved"),
		restartGateway{},
		advance{by: time.Minute},
		seededRowUnresolved("still unresolved after the restart, and still serving"),
		moveChain{move: func(blockchain *fakeChain) { blockchain.setLookupsDown(false) }},
		advance{by: 2 * escrow.TickInterval},
		expectThat{label: "resolved lazily", verify: func(harness *gatewayHarness) error {
			row, stored := seededRowOf(harness)
			if !stored || !row.Active || row.ChainEpoch != 7 || row.Amount != 1_000_000 {
				return fmt.Errorf("row %+v, want active with chain epoch 7 and amount 1000000", row)
			}
			return nil
		}},
	}
	runStepsWith(t, spec, func(blockchain *fakeChain) { blockchain.setLookupsDown(true) })
}

// Test flow:
//  1. Turn settlement off and give a target-1, temp-1 model a budget of 3, then seed one serving regular and two parked rows that will never settle: the budget is full.
//  2. Boot inside the bridge window and advance one minute.
//  3. Assert no escrow was created and the regular still serves, relabelled temp: the partial fill was an error, so the regular was promoted, not retired.
func TestABridgeWithoutRoomKeepsItsRegulars(t *testing.T) {
	spec := defaultSpec()
	spec.model.maxUnsettled = 3
	spec.epoch = chainEpoch{latest: 7, effective: 7, blockHeight: 1900, pocStart: 2000, setNewValidators: 2100, phase: chain.EpochPhaseInference}
	spec.seeded = []seededEscrow{
		{amount: 1_000_000, role: "regular"},
		{amount: 1_000_000, role: "regular", parked: true},
		{amount: 1_000_000, role: "regular", parked: true},
	}
	spec.environment = map[string]string{"DEVSHARD_ESCROW_ROTATION_SETTLEMENT_ENABLED": "false"}
	spec.steps = []harnessStep{
		advance{by: time.Minute},
		expectThat{label: "no create, and the regular kept serving as a temp", verify: func(harness *gatewayHarness) error {
			if creates := harness.chain.createsOf(testModelID); len(creates) != 0 {
				return fmt.Errorf("%d escrows created over a full budget, want 0", len(creates))
			}
			row, stored := seededRowOf(harness)
			if !stored || !row.Active || row.RotationRole != tempRole {
				return fmt.Errorf("regular %+v, want still serving, relabelled temp", row)
			}
			return nil
		}},
	}
	runSteps(t, spec)
}
