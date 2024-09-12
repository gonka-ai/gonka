package inference

import (
	"context"
	"encoding/base64"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/productscience/inference/x/inference/types"
	"log"
)

func (am AppModule) SendNewValidatorWeightsToStaking(ctx context.Context, blockHeight int64) {
	allPower := am.keeper.AllPower(ctx)

	var computeResults []keeper.ComputeResult
	for _, p := range allPower {
		participant, ok := am.keeper.GetParticipant(ctx, p.ParticipantAddress)
		if !ok {
			log.Printf("Error getting participant: %v", p.ParticipantAddress)
			continue
		}

		if participant.ValidatorKey == "" {
			continue
		}
		pubKeyBytes, err := base64.StdEncoding.DecodeString(participant.ValidatorKey)
		if err != nil {
			log.Printf("Error decoding pubkey. err = %v", err)
			continue
		}

		pubKey := ed25519.PubKey{Key: pubKeyBytes}

		r := keeper.ComputeResult{
			Power:           p.Power,
			ValidatorPubKey: &pubKey,
			OperatorAddress: p.ParticipantAddress,
		}
		log.Printf("Setting compute validator: %v", r)
		computeResults = append(computeResults, r)
	}

	_, err := am.keeper.Staking.SetComputeValidators(ctx, computeResults)
	if err != nil {
		log.Fatalf("Error setting compute validators: %v", err)
	}

	am.keeper.RemoveAllPower(ctx)

	activeParticipants := make([]*types.ActiveParticipant, len(computeResults))
	for i, r := range computeResults {
		activeParticipants[i] = &types.ActiveParticipant{
			Index:  r.OperatorAddress,
			Weight: r.Power,
		}
	}

	am.keeper.SetActiveParticipants(ctx, types.ActiveParticipants{
		Participants:         activeParticipants,
		CreatedAtBlockHeight: blockHeight,
	})
}
