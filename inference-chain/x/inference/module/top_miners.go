package inference

import (
	"context"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func (am AppModule) RegisterTopMiners(ctx context.Context, participants []*types.ActiveParticipant, time int64) error {
	existingTopMiners := am.keeper.GetAllTopMiner(ctx)
	payoutSettings := am.GetTopMinerPayoutSettings(ctx)
	qualificationThreshold := int64(10)
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
	for _, action := range actions {
		switch typedAction := action.(type) {
		case keeper.DoNothing:
			continue
		case keeper.AddMiner:
		case keeper.UpdateMiner:
			am.keeper.SetTopMiner(ctx, typedAction.Miner)
		case keeper.UpdateAndPayMiner:
			am.keeper.SetTopMiner(ctx, typedAction.Miner)
			err := am.keeper.PayParticipantFromModule(ctx, typedAction.Miner.Address, uint64(typedAction.Payout), types.TopRewardPoolAccName)
			if err != nil {
				return err
			}
		}
	}
	return nil
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

func (am AppModule) GetTopMinerPayoutSettings(ctx context.Context) keeper.PayoutSettings {
	return keeper.PayoutSettings{}
}
