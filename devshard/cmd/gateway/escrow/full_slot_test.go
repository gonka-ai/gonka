package escrow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/scheduler"
)

// Test flow:
//  1. Price the full-context slot the way routing does: the attempts a race funds times one request at the retirement reserve, at the escrow's own price.
//  2. Read the model with its one escrow holding exactly that slot, and again holding one unit less.
//  3. Assert the planner reads the first as full and the second as starved, so a full escrow is one the pick can pay a full-context request on.
func TestAnEscrowIsFullExactlyWhenItHoldsWhatRoutingPricesAFullContextRaceAt(t *testing.T) {
	manager, _, _, _ := planningManager(t)
	configuration := manager.config.Load()
	models, err := rotationModels(configuration.Rotation)
	require.NoError(t, err)
	snapshot := planningSnapshot()
	reserve := configuration.For("qwen").Limits.RetirementReserve(snapshot.Models["qwen"].MaxModelLen)
	oneRequest, priced := scheduler.RequestCost(reserve, snapshot.TokenPrice, snapshot.FeePerNonce)
	require.True(t, priced)
	slot := oneRequest * uint64(scheduler.AttemptsToFund(configuration.Engine.MaxAttemptsPerRequest))
	funds := manager.funds.(fakeFunds)

	for _, testCase := range []struct {
		name        string
		balance     uint64
		wantFull    bool
		wantStarved bool
	}{
		{name: "the slot", balance: slot, wantFull: true},
		{name: "one unit below the slot", balance: slot - 1, wantStarved: true},
	} {
		money := funds["5"]
		money.Balance = testCase.balance
		funds["5"] = money
		rows, err := manager.store.ListDevshards(context.Background())
		require.NoError(t, err)

		reading := manager.readModel(models[0], rows, nil, snapshot, configuration, manager.now())

		require.Len(t, reading.state.Escrows, 1, testCase.name)
		require.Equal(t, testCase.wantFull, reading.state.Escrows[0].Money.Full, "full at %s", testCase.name)
		require.Equal(t, testCase.wantStarved, reading.state.Escrows[0].Money.Starved, "starved at %s", testCase.name)
	}
}
