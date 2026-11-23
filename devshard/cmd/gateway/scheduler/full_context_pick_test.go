package scheduler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// Test flow:
//  1. Price the model's full-context request the way the funding planner prices a slot: the attempts a race funds times one request at the retirement reserve.
//  2. Pick an escrow for a request as large as that reserve, for escrows holding the slot, one request, and one unit less than one request.
//  3. Assert the escrow holding the slot and the escrow holding one request are picked, and the one below one request is refused with `ErrNoEscrowCapacity` and `ErrInsufficientBalance`.
func TestAnEscrowHoldingTheFullContextSlotServesAFullContextRequest(t *testing.T) {
	t.Parallel()
	const tokenPrice = 3
	snapshot := plannerSnapshot()
	probe, _, _ := plannerScheduler()
	reserve := probe.retirementReserve(modelA, snapshot)
	oneRequest, priced := RequestCost(reserve, tokenPrice, 0)
	require.True(t, priced)
	slot := oneRequest * uint64(probe.attemptsToFund(modelA))
	profile := RequestProfile{Model: modelA, InputBytes: int(reserve)}

	for _, testCase := range []struct {
		name    string
		balance uint64
		wantErr bool
	}{
		{name: "the slot", balance: slot},
		{name: "one request", balance: oneRequest},
		{name: "one unit below one request", balance: oneRequest - 1, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			scheduler, _, _ := plannerScheduler(candidate{id: "regular", weight: 1, latestNonce: 1, balance: testCase.balance, tokenPrice: tokenPrice})

			picked, err := pickWith(scheduler, profile, snapshot)

			if testCase.wantErr {
				require.ErrorIs(t, err, ErrNoEscrowCapacity)
				require.ErrorIs(t, err, types.ErrInsufficientBalance)
				return
			}
			require.NoError(t, err)
			require.Equal(t, "regular", picked.ID)
		})
	}
}

// Test flow:
//  1. Hold one escrow at exactly the full-context slot of its model, so the funding planner reads it as full.
//  2. Pick an escrow for a request one reserve larger than the attempts the slot funds, so it costs more than the slot.
//  3. Assert the pick refuses it with `ErrNoEscrowCapacity` and `ErrInsufficientBalance`: full promises a request up to the retirement reserve and no more than the slot pays.
func TestAFullEscrowRefusesARequestCostingMoreThanItsSlot(t *testing.T) {
	t.Parallel()
	const tokenPrice = 3
	snapshot := plannerSnapshot()
	probe, _, _ := plannerScheduler()
	reserve := probe.retirementReserve(modelA, snapshot)
	oneRequest, priced := RequestCost(reserve, tokenPrice, 0)
	require.True(t, priced)
	scheduler, _, _ := plannerScheduler(candidate{id: "full", weight: 1, latestNonce: 1, balance: oneRequest * uint64(probe.attemptsToFund(modelA)), tokenPrice: tokenPrice})

	_, err := pickWith(scheduler, RequestProfile{Model: modelA, InputBytes: int(reserve) * (probe.attemptsToFund(modelA) + 1)}, snapshot)

	require.ErrorIs(t, err, ErrNoEscrowCapacity)
	require.ErrorIs(t, err, types.ErrInsufficientBalance)
}
