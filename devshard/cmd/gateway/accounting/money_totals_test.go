package accounting

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// Test flow:
//  1. Open an escrow, observe a Pending, a Started, a Challenged and a Finished record and two slots' costs.
//  2. Assert the totals read 150 reserved in flight, 30 challenged and 75 charged.
//  3. Observe an empty set of live records and assert nothing is in flight or challenged any more, while the charges stay.
func TestMoneyTotalsReadTheLastObservation(t *testing.T) {
	book := NewBook(nil)
	require.NoError(t, book.OpenEscrow(EscrowMetadata{EscrowID: "5", Model: "qwen", Slots: []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "gonka1aaa"}, {SlotID: 1, ValidatorAddress: "gonka1bbb"}}}))
	require.NoError(t, book.ObserveInferences("5", map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusPending, ReservedCost: 100},
		2: {Status: types.StatusStarted, ReservedCost: 50},
		3: {Status: types.StatusChallenged, ReservedCost: 90, ActualCost: 30},
		4: {Status: types.StatusFinished, ReservedCost: 80, ActualCost: 45},
	}))
	require.NoError(t, book.ObserveHostStats("5", 0, types.HostStats{Cost: 70}))
	require.NoError(t, book.ObserveHostStats("5", 1, types.HostStats{Cost: 5}))

	totals, known := book.MoneyTotals("5")

	require.True(t, known)
	require.Equal(t, MoneyTotals{InFlightReserved: 150, Challenged: 30, Charged: 75}, totals)
	require.NoError(t, book.ObserveInferences("5", map[uint64]*types.InferenceRecord{}))
	totals, _ = book.MoneyTotals("5")
	require.Equal(t, MoneyTotals{Charged: 75}, totals)
}
