package inference

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	"github.com/cosmos/cosmos-sdk/x/staking/keeper"
	"github.com/productscience/inference/x/inference/types"
	"log"
)

func (am AppModule) SendNewValidatorWeightsToStaking(ctx context.Context, blockHeight int64) {
	allPower := am.keeper.AllPower(ctx)
	am.LogInfo("Amount of power entries found.", "n", len(allPower))

	var computeResults []keeper.ComputeResult
	for _, p := range allPower {
		participant, ok := am.keeper.GetParticipant(ctx, p.ParticipantAddress)
		if !ok {
			am.LogError("Error getting participant.", "participant", p.ParticipantAddress)
			continue
		}

		if participant.ValidatorKey == "" {
			am.LogError("Participant hasn't provided their validator key.", "participant", p.ParticipantAddress)
			continue
		}
		pubKeyBytes, err := base64.StdEncoding.DecodeString(participant.ValidatorKey)
		if err != nil {
			am.LogError("Error decoding pubkey.", "err", err)
			continue
		}

		pubKey := ed25519.PubKey{Key: pubKeyBytes}

		r := keeper.ComputeResult{
			Power:           p.Power,
			ValidatorPubKey: &pubKey,
			OperatorAddress: p.ParticipantAddress,
		}
		am.LogInfo("Setting compute validator.", "computeResult", r)
		computeResults = append(computeResults, r)
	}

	am.keeper.RemoveAllPower(ctx)

	if len(computeResults) == 0 {
		am.LogWarn("No compute validators to set. Keeping validators and active participants the same.")
		return
	}

	_, err := am.keeper.Staking.SetComputeValidators(ctx, computeResults)
	if err != nil {
		msg := fmt.Sprintf("Error setting compute validators: %v", err)
		am.LogError("Error setting compute validators.", "err", err)
		log.Fatalf(msg)
	}

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
