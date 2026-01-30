package inference

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func (am AppModule) RegisterTopMiners(ctx context.Context, participants []*types.ActiveParticipant, time int64) error {
	existingTopMiners := am.keeper.GetAllTopMiner(ctx)
	payoutSettings, err := am.GetTopMinerPayoutSettings(ctx)
	if err != nil {
		return err
	}
	p, err := am.keeper.GetParams(ctx)
	if err != nil {
		return err
	}
	qualificationThreshold := p.TokenomicsParams.TopMinerPocQualification
	participantList := am.qualifiedParticipantList(participants, qualificationThreshold)

	var referenceTopMiners []*types.TopMiner
	for _, miner := range existingTopMiners {
		referenceTopMiners = append(referenceTopMiners, &miner)
	}
	minerSet := &keeper.TopMinerSet{
		TopMiners:         referenceTopMiners,
		TimeOfCalculation: time,
		PayoutSettings:    payoutSettings,
		Participants:      participantList,
	}

	actions := keeper.GetTopMinerActions(minerSet)
	minerFound := false
	for _, action := range actions {
		am.LogInfo("top miner action", types.Tokenomics, "address", action.MinerAddress(), "action", action.TopMinerActionName(), "object", action)
		switch typedAction := action.(type) {
		case keeper.DoNothing:
			continue
		case keeper.AddMiner:
			minerFound = true
			err := am.keeper.SetTopMiner(ctx, typedAction.Miner)
			if err != nil {
				return err
			}
		case keeper.UpdateMiner:
			minerFound = true
			err := am.keeper.SetTopMiner(ctx, typedAction.Miner)
			if err != nil {
				return err
			}
		case keeper.UpdateAndPayMiner:
			err := am.keeper.SetTopMiner(ctx, typedAction.Miner)
			if err != nil {
				return err
			}
			params, err := am.keeper.GetParams(ctx)
			if err != nil {
				return err
			}
			topMinerVestingPeriod := &params.TokenomicsParams.TopMinerVestingPeriod
			err = am.keeper.PayParticipantFromModule(ctx, typedAction.Miner.Address, typedAction.Payout, types.TopRewardPoolAccName, "top_miner", topMinerVestingPeriod)
			if err != nil {
				return err
			}
		}
	}
	if payoutSettings.FirstQualifiedTime == 0 && minerFound {
		am.updateTopMinerFirstQualified(ctx, time)
	}
	return nil
}

func (am AppModule) updateTopMinerFirstQualified(ctx context.Context, time int64) {
	data, _ := am.keeper.GetTokenomicsData(ctx)
	data.TopRewardStart = time
	am.keeper.SetTokenomicsData(ctx, data)
}

func (am AppModule) qualifiedParticipantList(participants []*types.ActiveParticipant, threshold int64) []*keeper.Miner {
	var participantList []*keeper.Miner
	for _, participant := range participants {
		participantList = append(participantList, &keeper.Miner{
			Address:   participant.Index,
			Qualified: am.minerIsQualified(participant, threshold),
		})
	}
	return participantList
}

func (am AppModule) minerIsQualified(participant *types.ActiveParticipant, threshold int64) bool {
	return participant.Weight > threshold
}

func (am AppModule) GetTopMinerPayoutSettings(ctx context.Context) (keeper.PayoutSettings, error) {
	genesisOnlyParams, _ := am.keeper.GetGenesisOnlyParams(ctx)
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return keeper.PayoutSettings{}, err
	}
	tokenomicsData, _ := am.keeper.GetTokenomicsData(ctx)
	fullCoin := sdk.NormalizeCoin(sdk.NewInt64Coin(genesisOnlyParams.SupplyDenom, genesisOnlyParams.TopRewardAmount))
	return keeper.PayoutSettings{
		PayoutPeriod:       genesisOnlyParams.TopRewardPeriod,
		TotalRewards:       fullCoin.Amount.Int64(),
		TopNumberOfMiners:  genesisOnlyParams.TopRewards,
		MaxPayoutsTotal:    int32(genesisOnlyParams.TopRewardPayouts),
		MaxPayoutsPerMiner: int32(genesisOnlyParams.TopRewardPayoutsPerMiner),
		AllowedFailureRate: *params.TokenomicsParams.TopRewardAllowedFailure,
		MaximumTime:        genesisOnlyParams.TopRewardMaxDuration,
		FirstQualifiedTime: tokenomicsData.TopRewardStart,
	}, nil
}
