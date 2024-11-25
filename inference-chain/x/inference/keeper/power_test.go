package keeper_test

import (
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"testing"
)

func Test(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	addresses := []string{"participant-1", "participant-2", "participant-3"}
	for _, address := range addresses {
		keeper.SetUpcomingPower(ctx, types.Power{
			ParticipantAddress:       address,
			Power:                    10,
			PocStageStartBlockHeight: 240,
			ReceivedAtBlockHeight:    301,
		})
	}

	if len(keeper.AllUpcomingPower(ctx)) != 3 {
		t.Errorf("Expected to retrieve 3 power values")
	}

	keeper.RemoveAllUpcomingPower(ctx)

	if len(keeper.AllUpcomingPower(ctx)) != 0 {
		t.Errorf("Expected to retrieve 0 power values")
	}
}
