package poc

import (
	"testing"

	"decentralized-api/cosmosclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestSampleDuplicateScanIndices_BoundsAndNoDuplicates(t *testing.T) {
	indices, err := sampleDuplicateScanIndices(10_000)
	require.NoError(t, err)
	require.Len(t, indices, duplicateScanSampleSize)

	seen := make(map[uint32]bool, len(indices))
	for _, idx := range indices {
		require.Less(t, idx, uint32(10_000))
		require.False(t, seen[idx], "duplicate index: %d", idx)
		seen[idx] = true
	}
}

func TestSampleDuplicateScanIndices_UsesRandomOrderForFullSample(t *testing.T) {
	indices, err := sampleDuplicateScanIndices(20)
	require.NoError(t, err)
	require.Len(t, indices, 20)

	sorted := true
	for i, idx := range indices {
		if idx != uint32(i) {
			sorted = false
			break
		}
	}
	require.False(t, sorted, "duplicate scan chunks must not be based on sorted indexes")
}

func TestPoCValidationCoordinator_ReleaseDueRequiresScanOK(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("SubmitPocValidationsV2", mock.MatchedBy(func(msg *types.MsgSubmitPocValidationsV2) bool {
		return validationWeight(msg) == -1
	})).Return(nil).Once()

	coordinator := NewPoCValidationCoordinator(recorder, nil)
	require.NoError(t, coordinator.HandleValidationResult(100, "participant-a", "model-a", 10))

	recorder.AssertExpectations(t)
}

func TestPoCValidationCoordinator_ReleasesValidWhenScanOK(t *testing.T) {
	recorder := &cosmosclient.MockCosmosMessageClient{}
	recorder.On("SubmitPocValidationsV2", mock.MatchedBy(func(msg *types.MsgSubmitPocValidationsV2) bool {
		return validationWeight(msg) == 10
	})).Return(nil).Once()

	coordinator := NewPoCValidationCoordinator(recorder, nil)
	key := pocValidationKey{pocHeight: 100, participant: "participant-a", modelID: "model-a"}
	coordinator.scans[key] = &duplicateScanState{status: duplicateScanOK}

	require.NoError(t, coordinator.HandleValidationResult(100, "participant-a", "model-a", 10))
	recorder.AssertExpectations(t)
}

func validationWeight(msg *types.MsgSubmitPocValidationsV2) int64 {
	if msg == nil || len(msg.Validations) != 1 {
		return 0
	}
	return msg.Validations[0].ValidatedWeight
}
