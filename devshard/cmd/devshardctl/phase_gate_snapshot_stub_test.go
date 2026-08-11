package main

import (
	"context"
	"fmt"

	"common/chain"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc"
)

// preservedSnapshotStub implements chain.InferenceClient for phase_gate tests.
type preservedSnapshotStub struct {
	snapshotResp *inferencetypes.QueryPreservedNodesSnapshotResponse
}

var _ chain.InferenceClient = (*preservedSnapshotStub)(nil)

func (s *preservedSnapshotStub) PreservedNodesSnapshot(context.Context, *inferencetypes.QueryPreservedNodesSnapshotRequest, ...grpc.CallOption) (*inferencetypes.QueryPreservedNodesSnapshotResponse, error) {
	if s.snapshotResp != nil {
		return s.snapshotResp, nil
	}
	return &inferencetypes.QueryPreservedNodesSnapshotResponse{Found: false}, nil
}

func (s *preservedSnapshotStub) Params(context.Context, *inferencetypes.QueryParamsRequest, ...grpc.CallOption) (*inferencetypes.QueryParamsResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) EpochInfo(context.Context, *inferencetypes.QueryEpochInfoRequest, ...grpc.CallOption) (*inferencetypes.QueryEpochInfoResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) GetCurrentEpoch(context.Context, *inferencetypes.QueryGetCurrentEpochRequest, ...grpc.CallOption) (*inferencetypes.QueryGetCurrentEpochResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) ParticipantsWithBalances(context.Context, *inferencetypes.QueryParticipantsWithBalancesRequest, ...grpc.CallOption) (*inferencetypes.QueryParticipantsWithBalancesResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) AccountByAddress(context.Context, *inferencetypes.QueryAccountByAddressRequest, ...grpc.CallOption) (*inferencetypes.QueryAccountByAddressResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) Participant(context.Context, *inferencetypes.QueryGetParticipantRequest, ...grpc.CallOption) (*inferencetypes.QueryGetParticipantResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) DevshardEscrow(context.Context, *inferencetypes.QueryGetDevshardEscrowRequest, ...grpc.CallOption) (*inferencetypes.QueryGetDevshardEscrowResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) GranteesByMessageType(context.Context, *inferencetypes.QueryGranteesByMessageTypeRequest, ...grpc.CallOption) (*inferencetypes.QueryGranteesByMessageTypeResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) ExcludedParticipants(context.Context, *inferencetypes.QueryExcludedParticipantsRequest, ...grpc.CallOption) (*inferencetypes.QueryExcludedParticipantsResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) PocBatchesForStage(context.Context, *inferencetypes.QueryPocBatchesForStageRequest, ...grpc.CallOption) (*inferencetypes.QueryPocBatchesForStageResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) BridgeAddressesByChain(context.Context, *inferencetypes.QueryBridgeAddressesByChainRequest, ...grpc.CallOption) (*inferencetypes.QueryBridgeAddressesByChainResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) CurrentEpochGroupData(context.Context, *inferencetypes.QueryCurrentEpochGroupDataRequest, ...grpc.CallOption) (*inferencetypes.QueryCurrentEpochGroupDataResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) EpochGroupData(context.Context, *inferencetypes.QueryGetEpochGroupDataRequest, ...grpc.CallOption) (*inferencetypes.QueryGetEpochGroupDataResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) ModelsAll(context.Context, *inferencetypes.QueryModelsAllRequest, ...grpc.CallOption) (*inferencetypes.QueryModelsAllResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) GetAllModelPerTokenPrices(context.Context, *inferencetypes.QueryGetAllModelPerTokenPricesRequest, ...grpc.CallOption) (*inferencetypes.QueryGetAllModelPerTokenPricesResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (s *preservedSnapshotStub) GetAllModelCapacities(context.Context, *inferencetypes.QueryGetAllModelCapacitiesRequest, ...grpc.CallOption) (*inferencetypes.QueryGetAllModelCapacitiesResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func preservedSnapshotGRPCResponse(anchor int64, modelID, participantID string, nodeIDs []string) *inferencetypes.QueryPreservedNodesSnapshotResponse {
	participants := make([]*inferencetypes.ParticipantPreservedNodes, 0, 1)
	if participantID != "" {
		participants = append(participants, &inferencetypes.ParticipantPreservedNodes{
			ParticipantId: participantID,
			NodeIds:       nodeIDs,
		})
	}
	modelNodes := []*inferencetypes.ModelPreservedNodes{}
	if modelID != "" {
		modelNodes = append(modelNodes, &inferencetypes.ModelPreservedNodes{
			ModelId:      modelID,
			Participants: participants,
		})
	}
	return &inferencetypes.QueryPreservedNodesSnapshotResponse{
		Found: true,
		Snapshot: &inferencetypes.PreservedNodesSnapshot{
			EpisodeAnchorHeight: anchor,
			ModelPreservedNodes: modelNodes,
		},
	}
}
