package types_test

import (
	"fmt"
	"github.com/productscience/inference/x/inference/types"
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
		PocValidationDelay:    2,
		PocValidationDuration: 10,
	}
	initialBlockHeight := int64(1)
	startOfPoc := int64(10)
	var initialEpoch *types.Epoch = nil

	test(t, epochParams, initialBlockHeight, startOfPoc, initialEpoch)
}

func Test(t *testing.T) {
	epochParams := types.EpochParams{
		EpochLength:           2000,
		EpochMultiplier:       1,
		EpochShift:            90,
		PocStageDuration:      60,
		PocExchangeDuration:   1,
		PocValidationDelay:    2,
		PocValidationDuration: 20,
	}
	epoch := types.Epoch{
		Index:               5,
		PocStartBlockHeight: 2800,
	}

	startOfNexEpochPoc := epoch.PocStartBlockHeight + epochParams.EpochLength
	test(t, epochParams, startOfNexEpochPoc-15, startOfNexEpochPoc, &epoch)
}

func getEpochId(initialEpoch *types.Epoch) uint64 {
	if initialEpoch == nil {
		return 0
	} else {
		return initialEpoch.Index
	}
}

func test(t *testing.T, epochParams types.EpochParams, initialBlockHeight int64, startOfPoc int64, initialEpoch *types.Epoch) {
	var i = initialBlockHeight
	for i < startOfPoc {
		ec := types.NewEpochContext(initialEpoch, epochParams, i)
		require.Equal(t, getEpochId(initialEpoch), ec.EpochIndex)
		require.Equal(t, types.InferencePhase, ec.GetCurrentPhase(i))

		require.False(t, ec.IsPoCExchangeWindow(i))
		requireNotAStageBoundary(t, ec, i)

		i++
	}

	ec := types.NewEpochContext(initialEpoch, epochParams, i)
	require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
	require.Equal(t, types.PoCGeneratePhase, ec.GetCurrentPhase(i))
	require.Equal(t, i, ec.PocStartBlockHeight)
	require.True(t, ec.IsStartOfPocStage(i))
	require.False(t, ec.IsPoCExchangeWindow(i))

	i++

	for i < startOfPoc+epochParams.GetPoCWinddownStage() {
		ec := types.NewEpochContext(initialEpoch, epochParams, i)
		require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
		require.Equal(t, types.PoCGeneratePhase, ec.GetCurrentPhase(i))
		require.True(t, ec.IsPoCExchangeWindow(i))
		requireNotAStageBoundary(t, ec, i)

		i++
	}

	valStart := startOfPoc + epochParams.GetStartOfPoCValidationStage()
	for i < valStart {
		ec := types.NewEpochContext(initialEpoch, epochParams, i)
		require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
		require.Equal(t, types.PoCGenerateWindDownPhase, ec.GetCurrentPhase(i))
		require.True(t, ec.IsPoCExchangeWindow(i))

		if i == startOfPoc+epochParams.GetEndOfPoCStage() {
			require.True(t, ec.IsEndOfPoCStage(i))
		} else {
			requireNotAStageBoundary(t, ec, i)
		}

		i++
	}

	// Validation phase starts
	ec = types.NewEpochContext(initialEpoch, epochParams, i)
	require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
	require.Equal(t, types.PoCValidatePhase, ec.GetCurrentPhase(i))
	require.True(t, ec.IsStartOfPoCValidationStage(i))
	require.False(t, ec.IsPoCExchangeWindow(i))
	i++

	for i < startOfPoc+epochParams.GetPoCValidationWindownStage() {
		ec = types.NewEpochContext(initialEpoch, epochParams, i)
		require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
		require.Equal(t, types.PoCValidatePhase, ec.GetCurrentPhase(i))

		require.False(t, ec.IsPoCExchangeWindow(i))
		requireNotAStageBoundary(t, ec, i)

		i++
	}

	for i < startOfPoc+epochParams.GetEndOfPoCValidationStage() {
		ec = types.NewEpochContext(initialEpoch, epochParams, i)
		require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
		require.Equal(t, types.PoCValidateWindDownPhase, ec.GetCurrentPhase(i))

		require.False(t, ec.IsPoCExchangeWindow(i))
		requireNotAStageBoundary(t, ec, i)

		i++
	}

	ec = types.NewEpochContext(initialEpoch, epochParams, i)
	require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
	require.Equal(t, types.InferencePhase, ec.GetCurrentPhase(i))
	require.False(t, ec.IsPoCExchangeWindow(i))
	require.True(t, ec.IsEndOfPoCValidationStage(i))
	i++

	ec = types.NewEpochContext(initialEpoch, epochParams, i)
	require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
	require.Equal(t, types.InferencePhase, ec.GetCurrentPhase(i))
	require.False(t, ec.IsPoCExchangeWindow(i))
	require.True(t, ec.IsSetNewValidatorsStage(i))
	i++

	require.Panics(t, func() {
		fmt.Println("About to call NewEpochContext")
		types.NewEpochContext(initialEpoch, epochParams, i)
		fmt.Println("Returned from NewEpochContext (no panic?)")
	})

	nextEpochGroup := &types.Epoch{Index: getEpochId(initialEpoch) + 1, PocStartBlockHeight: startOfPoc}
	ec = types.NewEpochContext(nextEpochGroup, epochParams, i)
	require.Equal(t, getEpochId(nextEpochGroup), ec.EpochIndex)
	require.Equal(t, types.InferencePhase, ec.GetCurrentPhase(i))
	require.True(t, ec.IsClaimMoneyStage(i))
	require.False(t, ec.IsPoCExchangeWindow(i))
}

func requireNotAStageBoundary(t *testing.T, ec *types.EpochContext, i int64) {
	require.False(t, ec.IsStartOfPocStage(i))
	require.False(t, ec.IsEndOfPoCStage(i))
	require.False(t, ec.IsStartOfPoCValidationStage(i))
	require.False(t, ec.IsEndOfPoCValidationStage(i))
	require.False(t, ec.IsSetNewValidatorsStage(i))
	require.False(t, ec.IsClaimMoneyStage(i))
	require.False(t, ec.IsStartOfNextPoC(i))
}
