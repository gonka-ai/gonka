package inference

import (
	"context"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/productscience/inference/x/inference/types"
	"log"
)

func (am AppModule) SendNewValidatorWeightsToStaking(ctx context.Context) {
	allPower := am.keeper.AllPower(ctx)

	participantsById := make(map[string]types.Participant)
	for _, p := range allPower {
		participant, ok := am.keeper.GetParticipant(ctx, p.ParticipantAddress)
		if !ok {
			continue
		} else {
			participantsById[p.ParticipantAddress] = participant
		}
	}

	var computeResults []keeper.ComputeResult
	for i, p := range allPower {
		computeResults = append(computeResults, keeper.ComputeResult{
			Power:           p.Power,
			ValidatorPubKey: p.ParticipantAddress,
			OperatorAddress: int64(i),
		})
	}

	_, err := am.keeper.Staking.SetComputeValidators(ctx, computeResults)
	if err != nil {
		log.Fatalf("Error setting compute validators: %v", err)
	}

	// TODO: You probably should also set new weight here for Participants?
	//   Or should we do it as soon as we receive nonces?

	// TODO: We should also delete/mark inactive any participants that failed to provide weight

	// TODO: We should also delete any weights from last epochs
}
