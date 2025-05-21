package v2

import (
	"context"

	upgradetypes "cosmossdk.io/x/upgrade/types"
	"github.com/cosmos/cosmos-sdk/types/module"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func CreateUpgradeHandler(
	mm *module.Manager,
	configurator module.Configurator,
	k keeper.Keeper) upgradetypes.UpgradeHandler {
	return func(ctx context.Context, plan upgradetypes.Plan, vm module.VersionMap) (module.VersionMap, error) {
		err := upgradeParams(ctx, k)
		if err != nil {
			return nil, err
		}
		err = upgradeEpochGroupData(ctx, k)
		return vm, nil
	}
}

func upgradeEpochGroupData(ctx context.Context, k keeper.Keeper) error {
	groupData := k.GetAllEpochGroupDataV1(ctx)
	for _, data := range groupData {
		newData := types.EpochGroupData{
			PocStartBlockHeight:   data.PocStartBlockHeight,
			EpochGroupId:          data.EpochGroupId,
			EpochPolicy:           data.EpochPolicy,
			EffectiveBlockHeight:  int64(data.EffectiveBlockHeight),
			LastBlockHeight:       int64(data.LastBlockHeight),
			MemberSeedSignatures:  data.MemberSeedSignatures,
			ValidationWeights:     data.ValidationWeights,
			UnitOfComputePrice:    int64(data.UnitOfComputePrice),
			NumberOfRequests:      int64(data.NumberOfRequests),
			PreviousEpochRequests: int64(data.PreviousEpochRequests),
			ValidationParams:      fromOldParams(data.ValidationParams),
			TotalWeight:           int64(data.TotalWeight),
		}
		k.SetEpochGroupData(ctx, newData)
	}
	return nil
}

func upgradeParams(ctx context.Context, k keeper.Keeper) error {
	oldParams, err := k.GetV1Params(ctx)
	if err != nil {
		return err
	}
	newParams := types.Params{
		EpochParams: &types.EpochParams{
			EpochLength:               oldParams.EpochParams.EpochLength,
			EpochMultiplier:           oldParams.EpochParams.EpochMultiplier,
			EpochShift:                oldParams.EpochParams.EpochShift,
			DefaultUnitOfComputePrice: int64(oldParams.EpochParams.DefaultUnitOfComputePrice),
			PocStageDuration:          oldParams.EpochParams.PocStageDuration,
			PocExchangeDuration:       oldParams.EpochParams.PocExchangeDuration,
			PocValidationDelay:        oldParams.EpochParams.PocValidationDelay,
			PocValidationDuration:     oldParams.EpochParams.PocValidationDuration,
		},
		ValidationParams: fromOldParams(oldParams.ValidationParams),
		PocParams: &types.PocParams{
			DefaultDifficulty: int32(oldParams.PocParams.DefaultDifficulty),
		},
		TokenomicsParams: &types.TokenomicsParams{
			SubsidyReductionInterval: types.DecimalFromFloat(oldParams.TokenomicsParams.SubsidyReductionInterval),
			SubsidyReductionAmount:   types.DecimalFromFloat32(oldParams.TokenomicsParams.SubsidyReductionAmount),
			CurrentSubsidyPercentage: types.DecimalFromFloat32(oldParams.TokenomicsParams.CurrentSubsidyPercentage),
			TopRewardAllowedFailure:  types.DecimalFromFloat32(oldParams.TokenomicsParams.TopRewardAllowedFailure),
			TopMinerPocQualification: oldParams.TokenomicsParams.TopMinerPocQualification,
		},
	}
	return k.SetParams(ctx, newParams)
}

func fromOldParams(oldParams *types.ValidationParamsV1) *types.ValidationParams {
	if oldParams == nil {
		return nil
	}
	return &types.ValidationParams{
		FalsePositiveRate:           types.DecimalFromFloat(oldParams.FalsePositiveRate),
		MinRampUpMeasurements:       int32(oldParams.MinRampUpMeasurements),
		PassValue:                   types.DecimalFromFloat(oldParams.PassValue),
		MinValidationAverage:        types.DecimalFromFloat(oldParams.MinValidationAverage),
		MaxValidationAverage:        types.DecimalFromFloat(oldParams.MaxValidationAverage),
		ExpirationBlocks:            oldParams.ExpirationBlocks,
		EpochsToMax:                 oldParams.EpochsToMax,
		FullValidationTrafficCutoff: oldParams.FullValidationTrafficCutoff,
		MinValidationHalfway:        types.DecimalFromFloat(oldParams.MinValidationHalfway),
		MinValidationTrafficCutoff:  oldParams.MinValidationTrafficCutoff,
		MissPercentageCutoff:        types.DecimalFromFloat(oldParams.MissPercentageCutoff),
		MissRequestsPenalty:         types.DecimalFromFloat(oldParams.MissRequestsPenalty),
	}
}
