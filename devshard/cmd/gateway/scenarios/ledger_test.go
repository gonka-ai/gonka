package scenarios

import (
	"fmt"
	"testing"
	"time"
)

// Test flow:
//  1. Seed an escrow of chain epoch 7 serving its last epoch, and make every participant stall after its receipt.
//  2. Send two requests, so their attempts stay Started, and move inside the settle margin.
//  3. Advance three minutes: the deadline forces the settle, and finalize pays the open records to their executors.
//  4. Assert the escrow settled with costs above zero, the ledger's charged total equals the chain's, and `ledger match` compared at least one ledger total (`ledger match` at every step).
func TestAForcedSettleLeavesTheLedgerChargedAsTheChain(t *testing.T) {
	spec := lastEpochSpec()
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		changeParticipants{change: func(behaviour *participantBehaviour) { behaviour.stall = true }},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 2},
		advance{by: time.Minute},
		moveInsideTheMargin(),
		advance{by: 3 * time.Minute},
		expectThat{label: "the ledger charged what the chain settled", verify: func(harness *gatewayHarness) error {
			record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0]))
			totals, known := harness.currentGateway().Nonces().Book().MoneyTotals(harness.seededIDs[0])
			if !record.settled || record.settledCosts == 0 || !known || totals.Charged != record.settledCosts {
				return fmt.Errorf("settled %v costs %d, ledger known %v charged %d, want settled with costs above zero and the ledger charged the same", record.settled, record.settledCosts, known, totals.Charged)
			}
			if harness.ledgerComparisons == 0 {
				return fmt.Errorf("the ledger match compared no ledger totals: the invariant passed by skipping")
			}
			return nil
		}},
	}
	runStepsWith(t, spec, switchedToEpochEight)
}
