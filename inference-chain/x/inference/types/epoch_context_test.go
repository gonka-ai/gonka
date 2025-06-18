package types_test

import (
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestZeroEpoch(t *testing.T) {
	epochParams := types.EpochParams{
		EpochLength:           100,
		EpochMultiplier:       1,
		EpochShift:            90,
		PocStageDuration:      20,
		PocExchangeDuration:   1,
		PocValidationDelay:    1,
		PocValidationDuration: 10,
	}
	var i = int64(1)
	for i < 10 {
		ec := types.NewEpochContext(nil, epochParams, i)
		require.Equal(t, uint64(0), ec.Epoch)
		require.Equal(t, types.InferencePhase, ec.GetCurrentPhase(i))
		require.Equal(t, -epochParams.EpochShift, ec.PocStartBlockHeight)

		require.False(t, ec.IsStartOfPoc(i))
		require.False(t, ec.IsEndOfPoCStage(i))
		require.False(t, ec.IsStartOfPoCValidationStage(i))
		require.False(t, ec.IsEndOfPoCValidationStage(i))
		require.False(t, ec.IsSetNewValidatorsStage(i))
		require.False(t, ec.IsClaimMoneyStage(i))
		require.False(t, ec.IsStartOfNextPoC(i))

		i++
	}

	ec := types.NewEpochContext(nil, epochParams, i)
	require.Equal(t, uint64(1), ec.Epoch)
	require.Equal(t, types.PoCGeneratePhase, ec.GetCurrentPhase(i))
	require.Equal(t, i, ec.PocStartBlockHeight)

	require.True(t, ec.IsStartOfPoc(i))
}

func Test(t *testing.T) {
	epochParams := types.EpochParams{
		EpochLength:           100,
		EpochMultiplier:       1,
		EpochShift:            90,
		PocStageDuration:      20,
		PocExchangeDuration:   1,
		PocValidationDelay:    1,
		PocValidationDuration: 10,
	}
	epochGroup := types.EpochGroupData{
		PocStartBlockHeight: 110,
		EpochGroupId:        1,
	}

	startOfNexEpochPoc := int64(epochGroup.PocStartBlockHeight) + epochParams.EpochLength
	require.Equal(t, startOfNexEpochPoc, int64(210))

	var i = startOfNexEpochPoc
	for i < startOfNexEpochPoc+epochParams.PocStageDuration {
		ec := types.NewEpochContext(&epochGroup, epochParams, i)
		require.Equal(t, epochGroup.EpochGroupId+1, ec.Epoch)

		currentPhase := ec.GetCurrentPhase(i)
		_ = currentPhase

		i++
	}
}
