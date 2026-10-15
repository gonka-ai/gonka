package poc

import (
	"context"

	"common/logging"

	"github.com/productscience/inference/x/inference/types"
)

// ValidateOpenChallenges is the extra voter pass after ordinary ValidateAll.
// Every participant, including a challenged target, votes finished punishable open segments.
func (v *OffChainValidator) ValidateOpenChallenges() {
	epochState := v.phaseTracker.GetCurrentEpochState()
	if epochState == nil || !epochState.IsSynced {
		return
	}
	height := epochState.CurrentBlock.Height

	queryClient := v.recorder.NewInferenceQueryClient()
	paramsResp, err := queryClient.Params(context.Background(), &types.QueryParamsRequest{})
	if err != nil {
		logging.Error("OffChainValidator: failed to get params for challenge validation", types.PoC, "error", err)
		return
	}
	pocParams := paramsResp.Params.PocParams
	if pocParams == nil {
		logging.Error("OffChainValidator: PocParams missing for challenge validation", types.PoC)
		return
	}
	sampleSize := int(pocParams.ValidationSampleSize)
	if sampleSize == 0 {
		sampleSize = 200
	}

	samplingBlockHash := v.getSamplingBlockHash(epochState)
	if samplingBlockHash == "" {
		logging.Error("OffChainValidator: failed to get sampling block hash for challenges", types.PoC)
		return
	}

	nodes, err := v.getNodesWithRetry(0)
	if err != nil || len(nodes) == 0 {
		logging.Error("OffChainValidator: no nodes available for challenge validation", types.PoC, "error", err)
		return
	}

	for _, ch := range OpenChallenges.Punishable() {
		if ch == nil {
			continue
		}
		if !ShouldValidateChallenge(ch, height) {
			continue
		}
		seedHex := SeedHex(ch.Seed())
		if seedHex == "" {
			logging.Warn("OffChainValidator: skipping challenge with empty seed", types.PoC,
				"target", ch.Target(), "start_height", ch.StartHeight())
			continue
		}
		workItems := v.challengeWorkItems(queryClient, ch)
		if len(workItems) == 0 {
			logging.Info("OffChainValidator: no challenge work items", types.PoC,
				"target", ch.Target(), "start_height", ch.StartHeight())
			continue
		}
		logging.Info("OffChainValidator: validating open PoC challenge", types.PoC,
			"target", ch.Target(), "start_height", ch.StartHeight(), "work_items", len(workItems))
		v.executeValidation(ch.StartHeight(), samplingBlockHash, seedHex, pocParams, sampleSize, nodes, workItems, nil, true)
	}
}

func (v *OffChainValidator) challengeWorkItems(queryClient types.QueryClient, ch *types.OpenPoCChallenge) []participantWork {
	workItems := make([]participantWork, 0, len(ch.Commits))
	for _, commit := range ch.Commits {
		if commit == nil {
			continue
		}
		participantResp, err := queryClient.Participant(context.Background(),
			&types.QueryGetParticipantRequest{Index: commit.ParticipantAddress})
		if err != nil {
			logging.Warn("OffChainValidator: failed to get challenge participant", types.PoC,
				"address", commit.ParticipantAddress, "error", err)
			continue
		}
		if participantResp == nil {
			logging.Warn("OffChainValidator: nil challenge participant response", types.PoC,
				"address", commit.ParticipantAddress)
			continue
		}
		if participantResp.Participant.InferenceUrl == "" {
			logging.Warn("OffChainValidator: challenge participant has no URL", types.PoC,
				"address", commit.ParticipantAddress)
			continue
		}
		accountResp, err := queryClient.AccountByAddress(context.Background(),
			&types.QueryAccountByAddressRequest{Address: commit.ParticipantAddress})
		if err != nil || accountResp == nil || accountResp.Pubkey == "" {
			logging.Warn("OffChainValidator: failed to get challenge account public key", types.PoC,
				"address", commit.ParticipantAddress, "error", err)
			continue
		}
		pubKey := AccountPubKeyToHex(accountResp.Pubkey)
		if pubKey == "" {
			logging.Warn("OffChainValidator: challenge account public key is not base64", types.PoC,
				"address", commit.ParticipantAddress)
			continue
		}
		workItems = append(workItems, participantWork{
			address:   commit.ParticipantAddress,
			modelId:   commit.ModelId,
			url:       participantResp.Participant.InferenceUrl,
			pubKey:    pubKey,
			count:     commit.Count,
			rootHash:  commit.RootHash,
			treeDepth: commit.TreeDepth,
		})
	}
	return workItems
}
