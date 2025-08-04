package types_test

import (
	"fmt"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestZeroEpoch(t *testing.T) {
	epochParams := types.EpochParams{
		EpochLength:           100,
		EpochMultiplier:       1,
		EpochShift:            90,
		PocStageDuration:      20,
		PocExchangeDuration:   1,
		PocValidationDelay:    2,
		PocValidationDuration: 10,
		SetNewValidatorsDelay: 1,
	}
	initialBlockHeight := int64(1)
	startOfPoc := int64(10)
	var initialEpoch = types.Epoch{Index: 0, PocStartBlockHeight: 0}

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
		SetNewValidatorsDelay: 1,
	}
	epoch := types.Epoch{
		Index:               5,
		PocStartBlockHeight: 2800,
	}

	startOfNexEpochPoc := epoch.PocStartBlockHeight + epochParams.EpochLength
	test(t, epochParams, startOfNexEpochPoc-15, startOfNexEpochPoc, epoch)
}

func getEpochId(initialEpoch types.Epoch) uint64 {
	return initialEpoch.Index
}

func test(t *testing.T, epochParams types.EpochParams, initialBlockHeight int64, startOfPoc int64, initialEpoch types.Epoch) {
	var i = initialBlockHeight
	for i < startOfPoc {
		ec := types.NewEpochContextFromEffectiveEpoch(initialEpoch, epochParams, i)
		require.Equal(t, getEpochId(initialEpoch), ec.EpochIndex)
		require.Equal(t, types.InferencePhase, ec.GetCurrentPhase(i))

		require.False(t, ec.IsPoCExchangeWindow(i))
		require.False(t, ec.IsValidationExchangeWindow(i))
		requireNotAStageBoundary(t, ec, i)

		i++
	}

	ec := types.NewEpochContextFromEffectiveEpoch(initialEpoch, epochParams, i)
	require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
	require.Equal(t, types.PoCGeneratePhase, ec.GetCurrentPhase(i))
	require.Equal(t, i, ec.PocStartBlockHeight)
	require.True(t, ec.IsStartOfPocStage(i))
	require.False(t, ec.IsPoCExchangeWindow(i))
	require.False(t, ec.IsValidationExchangeWindow(i))

	i++

	for i < startOfPoc+epochParams.GetPoCWindDownStage() {
		ec := types.NewEpochContextFromEffectiveEpoch(initialEpoch, epochParams, i)
		require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
		require.Equal(t, types.PoCGeneratePhase, ec.GetCurrentPhase(i))
		require.True(t, ec.IsPoCExchangeWindow(i))
		require.False(t, ec.IsValidationExchangeWindow(i))
		requireNotAStageBoundary(t, ec, i)

		i++
	}

	valStart := startOfPoc + epochParams.GetStartOfPoCValidationStage()
	for i < valStart {
		ec := types.NewEpochContextFromEffectiveEpoch(initialEpoch, epochParams, i)
		require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
		require.Equal(t, types.PoCGenerateWindDownPhase, ec.GetCurrentPhase(i))
		require.True(t, ec.IsPoCExchangeWindow(i))
		require.False(t, ec.IsValidationExchangeWindow(i))

		if i == startOfPoc+epochParams.GetEndOfPoCStage() {
			require.True(t, ec.IsEndOfPoCStage(i))
		} else {
			requireNotAStageBoundary(t, ec, i)
		}

		i++
	}

	// Validation phase starts
	ec = types.NewEpochContextFromEffectiveEpoch(initialEpoch, epochParams, i)
	require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
	require.Equal(t, types.PoCValidatePhase, ec.GetCurrentPhase(i))
	require.True(t, ec.IsStartOfPoCValidationStage(i))
	require.False(t, ec.IsPoCExchangeWindow(i))
	require.False(t, ec.IsValidationExchangeWindow(i))
	i++

	for i < startOfPoc+epochParams.GetPoCValidationWindDownStage() {
		ec = types.NewEpochContextFromEffectiveEpoch(initialEpoch, epochParams, i)
		require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
		require.Equal(t, types.PoCValidatePhase, ec.GetCurrentPhase(i))

		require.False(t, ec.IsPoCExchangeWindow(i))
		require.True(t, ec.IsValidationExchangeWindow(i))

		requireNotAStageBoundary(t, ec, i)

		i++
	}

	for i < startOfPoc+epochParams.GetEndOfPoCValidationStage() {
		ec = types.NewEpochContextFromEffectiveEpoch(initialEpoch, epochParams, i)
		require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
		require.Equal(t, types.PoCValidateWindDownPhase, ec.GetCurrentPhase(i))

		require.False(t, ec.IsPoCExchangeWindow(i))
		require.True(t, ec.IsValidationExchangeWindow(i))

		requireNotAStageBoundary(t, ec, i)

		i++
	}

	ec = types.NewEpochContextFromEffectiveEpoch(initialEpoch, epochParams, i)
	require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
	require.Equal(t, types.InferencePhase, ec.GetCurrentPhase(i))
	require.False(t, ec.IsPoCExchangeWindow(i))
	// Validation exchange window now closes at EndOfPoCValidation stage, not SetNewValidators stage
	require.True(t, ec.IsValidationExchangeWindow(i))
	require.True(t, ec.IsEndOfPoCValidationStage(i))
	i++

	ec = types.NewEpochContextFromEffectiveEpoch(initialEpoch, epochParams, i)
	require.Equal(t, getEpochId(initialEpoch)+1, ec.EpochIndex)
	require.Equal(t, types.InferencePhase, ec.GetCurrentPhase(i))
	require.False(t, ec.IsPoCExchangeWindow(i))
	// Validation exchange window is now closed at SetNewValidators stage
	require.False(t, ec.IsValidationExchangeWindow(i))
	require.True(t, ec.IsSetNewValidatorsStage(i))
	i++

	require.Panics(t, func() {
		fmt.Println("About to call NewEpochContextFromEffectiveEpoch")
		types.NewEpochContextFromEffectiveEpoch(initialEpoch, epochParams, i)
		fmt.Println("Returned from NewEpochContextFromEffectiveEpoch (no panic?)")
	})

	nextEpochGroup := types.Epoch{Index: getEpochId(initialEpoch) + 1, PocStartBlockHeight: startOfPoc}
	ec = types.NewEpochContextFromEffectiveEpoch(nextEpochGroup, epochParams, i)
	require.Equal(t, getEpochId(nextEpochGroup), ec.EpochIndex)
	require.Equal(t, types.InferencePhase, ec.GetCurrentPhase(i))
	require.False(t, ec.IsSetNewValidatorsStage(i))
	require.True(t, ec.IsClaimMoneyStage(i))
	require.False(t, ec.IsPoCExchangeWindow(i))
	require.False(t, ec.IsValidationExchangeWindow(i))
}

func requireNotAStageBoundary(t *testing.T, ec *types.EpochContext, i int64) {
	require.False(t, ec.IsStartOfPocStage(i))
	require.False(t, ec.IsEndOfPoCStage(i))
	require.False(t, ec.IsStartOfPoCValidationStage(i))
	require.False(t, ec.IsEndOfPoCValidationStage(i))
	require.False(t, ec.IsSetNewValidatorsStage(i))
	require.False(t, ec.IsClaimMoneyStage(i))
}

func TestPlain(t *testing.T) {
	epochParams := types.EpochParams{
		EpochLength:           100,
		EpochMultiplier:       1,
		EpochShift:            90,
		PocStageDuration:      20,
		PocExchangeDuration:   1,
		PocValidationDelay:    2,
		PocValidationDuration: 10,
		SetNewValidatorsDelay: 1,
	}
	startOfPoc := int64(10)
	epoch := types.Epoch{Index: 1, PocStartBlockHeight: startOfPoc}

	ec := types.NewEpochContext(epoch, epochParams)
	require.True(t, ec.IsStartOfPocStage(startOfPoc))
	require.False(t, ec.IsPoCExchangeWindow(startOfPoc))
	require.False(t, ec.IsValidationExchangeWindow(startOfPoc))

	require.False(t, ec.IsStartOfPocStage(startOfPoc+1))
	require.True(t, ec.IsPoCExchangeWindow(startOfPoc+1))
	require.False(t, ec.IsStartOfPoCValidationStage(startOfPoc+1))
	require.False(t, ec.IsValidationExchangeWindow(startOfPoc+1))

	startOfVal := startOfPoc + epochParams.GetStartOfPoCValidationStage()
	require.False(t, ec.IsStartOfPocStage(startOfVal))
	require.False(t, ec.IsPoCExchangeWindow(startOfVal))
	require.True(t, ec.IsStartOfPoCValidationStage(startOfVal))
	require.False(t, ec.IsValidationExchangeWindow(startOfVal))

	require.False(t, ec.IsStartOfPocStage(startOfVal+1))
	require.False(t, ec.IsStartOfPoCValidationStage(startOfVal+1))
	require.False(t, ec.IsPoCExchangeWindow(startOfVal+1))
	require.True(t, ec.IsValidationExchangeWindow(startOfVal+1))
}

func TestProdBug(t *testing.T) {
	/*
		"epoch_params": {
		          "epoch_length": "50",
		          "epoch_multiplier": "1",
		          "epoch_shift": "0",
		          "default_unit_of_compute_price": "100",
		          "poc_stage_duration": "4",
		          "poc_exchange_duration": "1",
		          "poc_validation_delay": "1",
		          "poc_validation_duration": "4"
		        }
	*/
	epochParams := types.EpochParams{
		EpochLength:           50,
		EpochMultiplier:       1,
		EpochShift:            0,
		PocStageDuration:      4,
		PocExchangeDuration:   1,
		PocValidationDelay:    1,
		PocValidationDuration: 4,
		SetNewValidatorsDelay: 1,
	}

	epoch := types.Epoch{Index: 1, PocStartBlockHeight: 50}
	ec := types.NewEpochContext(epoch, epochParams)

	require.True(t, ec.IsValidationExchangeWindow(57))
}
