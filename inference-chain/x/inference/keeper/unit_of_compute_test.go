package keeper_test

import (
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"strconv"
	"testing"
)

func TestUnitOfComputeProposals(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	for i := 0; i < 10; i++ {
		proposal := &types.UnitOfComputePriceProposal{
			Participant: "participant-" + strconv.Itoa(i),
			Price:       uint64(i),
		}
		keeper.SetUnitOfComputePriceProposal(ctx, proposal)
	}

	for i := 0; i < 10; i++ {
		participant := "participant-" + strconv.Itoa(i)
		proposal, found := keeper.GettUnitOfComputePriceProposal(ctx, participant)
		if !found {
			t.Errorf("Expected to find proposal for participant %s", participant)
		}
		if proposal.Price != uint64(i) {
			t.Errorf("Expected price to be %d, got %d", i, proposal.Price)
		}
	}

	proposals, err := keeper.AllUnitOfComputePriceProposals(ctx)
	if err != nil {
		t.Errorf("Failed to get all proposals: %v", err)
	}
	if len(proposals) != 10 {
		t.Errorf("Expected to find 10 proposals, got %d", len(proposals))
	}
}

func TestUnitOfComputePrice(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	keeper.SetUnitOfComputePrice(ctx, 100, 1)
	keeper.SetUnitOfComputePrice(ctx, 200, 2)

	price, found := keeper.GetUnitOfComputePrice(ctx, 1)
	if !found {
		t.Errorf("Expected to find price for epoch 1")
	}
	if price.Price != 100 {
		t.Errorf("Expected price to be 100, got %d", price.Price)
	}

	price, found = keeper.GetUnitOfComputePrice(ctx, 2)
	if !found {
		t.Errorf("Expected to find price for epoch 2")
	}
	if price.Price != 200 {
		t.Errorf("Expected price to be 200, got %d", price.Price)
	}
}
