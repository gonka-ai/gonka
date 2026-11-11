package chain

import (
	"context"
	"fmt"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	inferencetypes "github.com/productscience/inference/x/inference/types"
)

const (
	activeParticipantsStorePath        = "store/inference/key"
	abciCodeOK                  uint32 = 0
)

var _ payloadFallback = (*GRPCChain)(nil)

// epochInfoPayload reads epoch info from the node in the shape the public API serves it. See README.md, "The poll loop".
func (g *GRPCChain) epochInfoPayload(ctx context.Context) (chainEpochInfoResponse, error) {
	response, err := g.client.InferenceQueryClient().EpochInfo(ctx, &inferencetypes.QueryEpochInfoRequest{})
	if err != nil {
		return chainEpochInfoResponse{}, err
	}
	return epochInfoPayloadFromResponse(response)
}

func epochInfoPayloadFromResponse(response *inferencetypes.QueryEpochInfoResponse) (chainEpochInfoResponse, error) {
	if response == nil || response.Params.EpochParams == nil {
		return chainEpochInfoResponse{}, fmt.Errorf("epoch info response is missing epoch params")
	}
	epoch := inferencetypes.NewEpochContext(response.LatestEpoch, *response.Params.EpochParams)
	currentStages := epoch.GetEpochStages()
	nextEpoch := epoch.NextEpochContext()
	nextStages := nextEpoch.GetEpochStages()
	payload := chainEpochInfoResponse{
		BlockHeight: jsonInt64(response.BlockHeight),
		Phase:       string(epoch.GetCurrentPhase(response.BlockHeight)),
		LatestEpoch: chainLatestEpoch{
			Index:               jsonUint64(response.LatestEpoch.Index),
			PocStartBlockHeight: jsonInt64(response.LatestEpoch.PocStartBlockHeight),
		},
		EpochStages:             chainEpochStages{SetNewValidators: jsonInt64(currentStages.SetNewValidators), NextPoCStart: jsonInt64(currentStages.NextPocStart)},
		NextEpochStages:         chainEpochStages{SetNewValidators: jsonInt64(nextStages.SetNewValidators), NextPoCStart: jsonInt64(nextStages.NextPocStart)},
		IsConfirmationPoCActive: response.IsConfirmationPocActive,
	}
	if event := response.ActiveConfirmationPocEvent; event != nil {
		payload.ActiveConfirmationPoC = &chainConfirmationPoCEventPayload{
			Phase:         confirmationPoCPhaseValue(event.Phase.String()),
			TriggerHeight: jsonInt64(event.TriggerHeight),
		}
	}
	return payload, nil
}

// participantsPayload reads the current epoch's active participants from the node's store in the shape the public API serves them. See README.md, "The poll loop".
func (g *GRPCChain) participantsPayload(ctx context.Context) (chainCurrentParticipantsResponse, error) {
	current, err := g.client.InferenceQueryClient().GetCurrentEpoch(ctx, &inferencetypes.QueryGetCurrentEpochRequest{})
	if err != nil {
		return chainCurrentParticipantsResponse{}, fmt.Errorf("fetch current epoch: %w", err)
	}
	if current == nil {
		return chainCurrentParticipantsResponse{}, fmt.Errorf("current epoch response is nil")
	}
	result, err := g.client.CometServiceClient().ABCIQuery(ctx, &cmtservice.ABCIQueryRequest{
		Path: activeParticipantsStorePath,
		Data: inferencetypes.ActiveParticipantsFullKey(current.GetEpoch()),
	})
	switch {
	case err != nil:
		return chainCurrentParticipantsResponse{}, fmt.Errorf("fetch active participants: %w", err)
	case result.GetCode() != abciCodeOK:
		return chainCurrentParticipantsResponse{}, fmt.Errorf("active participants query failed with code %d", result.GetCode())
	case len(result.GetValue()) == 0:
		return chainCurrentParticipantsResponse{}, fmt.Errorf("active participants are unavailable for epoch %d", current.GetEpoch())
	}
	var active inferencetypes.ActiveParticipants
	if err := active.Unmarshal(result.GetValue()); err != nil {
		return chainCurrentParticipantsResponse{}, fmt.Errorf("decode active participants: %w", err)
	}
	return participantsPayloadFromActive(&active), nil
}

func participantsPayloadFromActive(active *inferencetypes.ActiveParticipants) chainCurrentParticipantsResponse {
	var payload chainCurrentParticipantsResponse
	for _, participant := range active.GetParticipants() {
		if participant == nil {
			continue
		}
		item := chainActiveParticipant{
			Index:        participant.Index,
			InferenceURL: participant.InferenceUrl,
			Models:       append([]string(nil), participant.Models...),
		}
		for _, modelNodes := range participant.MlNodes {
			var converted chainModelMLNodes
			for _, node := range modelNodes.GetMlNodes() {
				if node == nil {
					continue
				}
				converted.MLNodes = append(converted.MLNodes, chainMLNodeInfo{
					NodeID:             node.NodeId,
					TimeslotAllocation: append([]bool(nil), node.TimeslotAllocation...), //nolint:staticcheck // the legacy preservation rule reads the deprecated flag
					PoCWeight:          jsonUint64(max(node.PocWeight, 0)),
				})
			}
			item.MLNodes = append(item.MLNodes, converted)
		}
		payload.ActiveParticipants.Participants = append(payload.ActiveParticipants.Participants, item)
	}
	return payload
}
