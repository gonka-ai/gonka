package types_test

import (
	"fmt"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestNilEpoch(t *testing.T) {
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

		requireNotAStageBoundary(t, ec, i)

		i++
	}

	ec := types.NewEpochContext(nil, epochParams, i)
	require.Equal(t, uint64(1), ec.Epoch)
	require.Equal(t, types.PoCGeneratePhase, ec.GetCurrentPhase(i))
	require.Equal(t, i, ec.PocStartBlockHeight)
	require.True(t, ec.IsStartOfPoc(i))
	startOfPoc := i

	i++

	for i < startOfPoc+epochParams.GetPoCWinddownStage() {
		ec := types.NewEpochContext(nil, epochParams, i)
		require.Equal(t, uint64(1), ec.Epoch)
		require.Equal(t, types.PoCGeneratePhase, ec.GetCurrentPhase(i))
		requireNotAStageBoundary(t, ec, i)

		i++
	}

	valStart := startOfPoc + epochParams.GetStartOfPoCValidationStage()
	for i < valStart {
		ec := types.NewEpochContext(nil, epochParams, i)
		require.Equal(t, uint64(1), ec.Epoch)
		require.Equal(t, types.PoCGenerateWindDownPhase, ec.GetCurrentPhase(i))

		if i == startOfPoc+epochParams.GetEndOfPoCStage() {
			require.True(t, ec.IsEndOfPoCStage(i))
		} else {
			requireNotAStageBoundary(t, ec, i)
		}

		i++
	}

	// Validation phase starts
	ec = types.NewEpochContext(nil, epochParams, i)
	require.Equal(t, uint64(1), ec.Epoch)
	require.Equal(t, types.PoCValidatePhase, ec.GetCurrentPhase(i))
	require.True(t, ec.IsStartOfPoCValidationStage(i))
	i++

	for i < startOfPoc+epochParams.GetPoCValidationWindownStage() {
		ec = types.NewEpochContext(nil, epochParams, i)
		require.Equal(t, uint64(1), ec.Epoch)
		require.Equal(t, types.PoCValidatePhase, ec.GetCurrentPhase(i))

		requireNotAStageBoundary(t, ec, i)

		i++
	}

	for i < startOfPoc+epochParams.GetEndOfPoCValidationStage() {
		ec = types.NewEpochContext(nil, epochParams, i)
		require.Equal(t, uint64(1), ec.Epoch)
		require.Equal(t, types.PoCValidateWindDownPhase, ec.GetCurrentPhase(i))

		requireNotAStageBoundary(t, ec, i)

		i++
	}

	ec = types.NewEpochContext(nil, epochParams, i)
	require.Equal(t, uint64(1), ec.Epoch)
	require.Equal(t, types.InferencePhase, ec.GetCurrentPhase(i))
	require.True(t, ec.IsEndOfPoCValidationStage(i))
	i++

	ec = types.NewEpochContext(nil, epochParams, i)
	require.Equal(t, uint64(1), ec.Epoch)
	require.Equal(t, types.InferencePhase, ec.GetCurrentPhase(i))
	require.True(t, ec.IsSetNewValidatorsStage(i))
	i++

	assert.Panics(t, func() {
		fmt.Println("About to call NewEpochContext")
		types.NewEpochContext(nil, epochParams, i)
		fmt.Println("Returned from NewEpochContext (no panic?)")
	})

	ec = types.NewEpochContext(&types.EpochGroupData{EpochGroupId: 1, PocStartBlockHeight: uint64(startOfPoc)}, epochParams, i)
	require.Equal(t, uint64(1), ec.Epoch)
	require.Equal(t, types.InferencePhase, ec.GetCurrentPhase(i))
	require.True(t, ec.IsClaimMoneyStage(i))
}

func requireNotAStageBoundary(t *testing.T, ec *types.EpochContext, i int64) {
	require.False(t, ec.IsStartOfPoc(i))
	require.False(t, ec.IsEndOfPoCStage(i))
	require.False(t, ec.IsStartOfPoCValidationStage(i))
	require.False(t, ec.IsEndOfPoCValidationStage(i))
	require.False(t, ec.IsSetNewValidatorsStage(i))
	require.False(t, ec.IsClaimMoneyStage(i))
	require.False(t, ec.IsStartOfNextPoC(i))
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
		PocStartBlockHeight: 2800,
		EpochGroupId:        5,
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
