package inference

import (
	"context"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func (am AppModule) RegisterTopMiners(ctx context.Context, participants []*types.ActiveParticipant, time int64) error {
	//existingTopMiners := am.keeper.GetAllTopMiner(ctx)
	//payoutSettings := am.GetTopMinerPayoutSettings(ctx)
	//qualificationThreshold := 10
	//qualificationMap := am.qualifiedParticipantMap(participants)
	//minerSet := &keeper.TopMinerSet{
	//	TopMiners:         existingTopMiners,
	//	TimeOfCalculation: time,
	//	PayoutSettings:    payoutSettings,
	//	Qualified:         qualificationMap,
	//}
	//
	//// THE ORDER MATTERS!!!!
	//for _, participant := range participants {
	//	factors := &keeper.TopMinerFactors{
	//		TopMiners:         existingTopMiners,
	//		PayoutSettings:    payoutSettings,
	//		Qualified:         am.minerIsQualified(participant, int64(qualificationThreshold)),
	//		MinerAddress:      participant.Index,
	//		TimeOfCalculation: time,
	//	}
	//	action, _ := keeper.GetTopMinerAction(factors)
	//
	//}
	return nil
}

func (am AppModule) qualifiedParticipantMap(participants []*types.ActiveParticipant) map[string]bool {
	qualifiedMap := make(map[string]bool)
	for _, participant := range participants {
		qualifiedMap[participant.Index] = am.minerIsQualified(participant, 10)
	}
	return qualifiedMap
}

func (am AppModule) minerIsQualified(participant *types.ActiveParticipant, threshold int64) bool {
	return participant.Weight > threshold
}

func (am AppModule) GetTopMinerPayoutSettings(ctx context.Context) keeper.PayoutSettings {

}
