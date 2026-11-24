package funding

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const needSeveralEscrows = 4

// Test flow:
//  1. Plan the healthy model with demand peaks that put the need at several whole escrows, and with a misconfigured amount and an unpriced model.
//  2. Read the decision's NeedEscrows.
//  3. Assert it is the need divided by one escrow's liquid money, rounded up, and zero where an escrow holds nothing or the model is unpriced.
func TestADecisionCountsTheEscrowsItsNeedCallsFor(t *testing.T) {
	t.Parallel()
	state := healthyModel()
	perEscrow := capacityPerEscrow(state)
	state.DemandPeak = 3 * perEscrow
	decision := Plan(state)
	require.Equal(t, int(ceilDivide(decision.Need, perEscrow)), decision.NeedEscrows, "need escrows")
	require.Greater(t, decision.NeedEscrows, needSeveralEscrows, "a need of several escrows")
	state.Amount = state.CreateFee
	require.Zero(t, Plan(state).NeedEscrows, "a misconfigured amount")
	state = healthyModel()
	state.DemandPeak, state.Priced = 3*perEscrow, false
	require.Zero(t, Plan(state).NeedEscrows, "an unpriced model")
}
