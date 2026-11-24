package scenarios

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
)

// Test flow:
//  1. Boot in a relaxed PoC bridge with one starved temp already satisfying temp_count.
//  2. Let the lifecycle run, then send a request too large for that temp.
//  3. Assert one funded temp was added, the old temp survives, and the request succeeds.
func TestAStarvedBridgeRefillsAndServesWithoutChangingTempCount(t *testing.T) {
	spec := defaultSpec()
	spec.seeded = []seededEscrow{{amount: 20_000, role: tempRole}}
	spec.epoch = chainEpoch{latest: 8, effective: 7, blockHeight: 1900, pocStart: 1800, setNewValidators: 2100, phase: chain.EpochPhasePoCGenerate}
	spec.environment = map[string]string{"DEVSHARD_POC_REQUEST_MODE": "relaxed"}
	spec.steps = []harnessStep{
		advance{by: time.Minute},
		sendRequests{promptBytes: 30_000, maxTokens: 64, count: 1, sequential: true},
		advance{by: time.Minute},
		expectThat{label: "the starved bridge gained usable liquidity", verify: func(harness *gatewayHarness) error {
			if creates := len(createdWithRole(harness, tempRole)); creates != 1 {
				return fmt.Errorf("%d temp creates, want one refill", creates)
			}
			if temps := rowsOfRole(harness, tempRole, true); temps != 2 {
				return fmt.Errorf("%d active temps, want the old temp and its funded refill", temps)
			}
			return everyAnswer(1, http.StatusOK)(harness)
		}},
	}
	runSteps(t, spec)
}
