package scenarios

import (
	"fmt"
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
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

// Test flow:
//  1. Serve requests on the seeded escrow, let them finish, and move inside the settle margin so the idle escrow is settled through a rehydrated session.
//  2. Assert the escrow settled.
//  3. Assert the ledger counted the finalize nonce and left no nonce of the escrow unclassified.
func TestASettledIdleEscrowLeavesNoNonceUnclassified(t *testing.T) {
	spec := lastEpochSpec()
	spec.steps = []harnessStep{
		advance{by: 30 * time.Second},
		sendRequests{promptBytes: 200, maxTokens: 64, count: 2},
		advance{by: time.Minute},
		moveInsideTheMargin(),
		advance{by: 3 * time.Minute},
		expectThat{label: "the ledger classified every nonce of the settled escrow", verify: func(harness *gatewayHarness) error {
			if record, _ := harness.chain.escrowRecord(parseEscrowID(harness.seededIDs[0])); !record.settled {
				return fmt.Errorf("the seeded escrow did not settle")
			}
			var unclassified, finalized uint64
			for _, record := range harness.currentGateway().Nonces().Book().Query(accounting.QueryFilter{}) {
				unclassified += record.Unobserved
				finalized += record.ServiceNonces[accounting.ServiceFinalize]
			}
			if unclassified != 0 || finalized == 0 {
				return fmt.Errorf("unclassified %d, finalize nonces %d, want none unclassified and a finalize counted", unclassified, finalized)
			}
			return nil
		}},
	}
	runStepsWith(t, spec, switchedToEpochEight)
}
