package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestSubmitPocValidation_DuplicateRejected(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	ms := keeper.NewMsgServerImpl(k)

	params := types.DefaultParams()
	err := k.SetParams(ctx, params)
	require.NoError(t, err)

	err = k.SetEpoch(ctx, &types.Epoch{Index: 1, PocStartBlockHeight: 0})
	require.NoError(t, err)
	err = k.SetEpoch(ctx, &types.Epoch{Index: 2, PocStartBlockHeight: 100})
	require.NoError(t, err)
	k.SetEffectiveEpochIndex(ctx, 1)

	participant := sample.AccAddress()
	validator := sample.AccAddress()
	startBlockHeight := int64(100)

	epochCtx := types.NewEpochContext(types.Epoch{Index: 2, PocStartBlockHeight: startBlockHeight}, *params.EpochParams)
	currentBlockHeight := epochCtx.StartOfPoCValidation() + 1
	ctx = ctx.WithBlockHeight(currentBlockHeight)

	pAddr, err := sdk.AccAddressFromBech32(participant)
	require.NoError(t, err)
	vAddr, err := sdk.AccAddressFromBech32(validator)
	require.NoError(t, err)
	pk := collections.Join3(startBlockHeight, pAddr, vAddr)
	err = k.PoCValidations.Set(ctx, pk, types.PoCValidation{
		ParticipantAddress:          participant,
		ValidatorParticipantAddress: validator,
		PocStageStartBlockHeight:    startBlockHeight,
		ValidatedAtBlockHeight:      currentBlockHeight,
		Nonces:                      []int64{},
		Dist:                        []float64{},
		ReceivedDist:                []float64{},
		RTarget:                     0,
		FraudThreshold:              0,
		NInvalid:                    0,
		ProbabilityHonest:           0,
		FraudDetected:               false,
	})
	require.NoError(t, err)

	_, err = ms.SubmitPocValidation(ctx, &types.MsgSubmitPocValidation{
		Creator:                  validator,
		ParticipantAddress:       participant,
		PocStageStartBlockHeight: startBlockHeight,
		Nonces:                   []int64{},
		Dist:                     []float64{},
		ReceivedDist:             []float64{},
		RTarget:                  0,
		FraudThreshold:           0,
		NInvalid:                 0,
		ProbabilityHonest:        0,
		FraudDetected:            false,
	})
	require.ErrorIs(t, err, types.ErrPocValidationAlreadyExists)
}
