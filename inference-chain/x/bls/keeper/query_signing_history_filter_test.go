package keeper

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/bls/types"
)

// TestSigningHistory_FilterFillsPage pins that epoch/status filters are
// applied before the page limit: a page of `limit` must contain up to
// `limit` matching requests, and count_total must count matches, not every
// stored request. With query.Paginate the limit is consumed by skipped keys,
// so a filtered query can return an empty page with a next_key and the total
// of the whole store. Partial signatures are still rehydrated for the
// requests on the page.
func TestSigningHistory_FilterFillsPage(t *testing.T) {
	k, ctx := setupBlsKeeperForRetryTests(t)

	// 30 requests of epoch 1 and 3 of epoch 2, keys interleaved by request id.
	for i := 0; i < 33; i++ {
		epoch := uint64(1)
		if i%11 == 10 {
			epoch = 2
		}
		req := &types.ThresholdSigningRequest{
			RequestId:      []byte{byte(i), 0xAA},
			CurrentEpochId: epoch,
			Status:         types.ThresholdSigningStatus_THRESHOLD_SIGNING_STATUS_COMPLETED,
		}
		if i == 10 {
			req.PartialSignatures = []types.PartialSignature{
				{ParticipantAddress: "p1", SlotIndices: []uint32{1, 2}, Signature: make([]byte, 48)},
			}
		}
		require.NoError(t, k.StoreThresholdSigningRequest(ctx, req))
	}

	resp, err := k.SigningHistory(ctx, &types.QuerySigningHistoryRequest{
		CurrentEpochId: 2,
		Pagination:     &query.PageRequest{Limit: 5, CountTotal: true},
	})
	require.NoError(t, err)
	require.Len(t, resp.SigningRequests, 3)
	var withPartials int
	for _, r := range resp.SigningRequests {
		require.Equal(t, uint64(2), r.CurrentEpochId)
		if len(r.PartialSignatures) > 0 {
			withPartials++
			require.Equal(t, []byte{10, 0xAA}, r.RequestId)
			require.Equal(t, "p1", r.PartialSignatures[0].ParticipantAddress)
			require.Equal(t, []uint32{1, 2}, r.PartialSignatures[0].SlotIndices)
		}
	}
	require.Equal(t, 1, withPartials)
	require.Equal(t, uint64(3), resp.Pagination.Total)
	require.Nil(t, resp.Pagination.NextKey)

	// Page through epoch 1 two at a time: every page is full until the end.
	var got int
	var next []byte
	for pages := 0; pages < 20; pages++ {
		resp, err = k.SigningHistory(ctx, &types.QuerySigningHistoryRequest{
			CurrentEpochId: 1,
			Pagination:     &query.PageRequest{Key: next, Limit: 2},
		})
		require.NoError(t, err)
		if resp.Pagination.NextKey != nil {
			require.Len(t, resp.SigningRequests, 2)
		}
		got += len(resp.SigningRequests)
		next = resp.Pagination.NextKey
		if next == nil {
			break
		}
	}
	require.Equal(t, 30, got)

	// No filter: behaviour unchanged.
	resp, err = k.SigningHistory(ctx, &types.QuerySigningHistoryRequest{
		Pagination: &query.PageRequest{Limit: 100, CountTotal: true},
	})
	require.NoError(t, err)
	require.Len(t, resp.SigningRequests, 33)
	require.Equal(t, uint64(33), resp.Pagination.Total)
}
