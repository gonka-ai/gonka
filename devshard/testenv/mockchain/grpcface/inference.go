package grpcface

import (
	"context"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"devshard/testenv/mockchain/store"
)

// InferenceServer implements the inference module Query gRPC surface needed for Phase 3a.
type InferenceServer struct {
	inferencetypes.UnimplementedQueryServer
	store *store.Store
}

// NewInferenceServer returns a QueryServer backed by store.
func NewInferenceServer(st *store.Store) *InferenceServer {
	return &InferenceServer{store: st}
}

func (s *InferenceServer) Params(_ context.Context, _ *inferencetypes.QueryParamsRequest) (*inferencetypes.QueryParamsResponse, error) {
	return &inferencetypes.QueryParamsResponse{Params: s.store.GetParams()}, nil
}

func (s *InferenceServer) EpochInfo(_ context.Context, _ *inferencetypes.QueryEpochInfoRequest) (*inferencetypes.QueryEpochInfoResponse, error) {
	epoch := s.store.GetEpoch()
	return &inferencetypes.QueryEpochInfoResponse{
		BlockHeight: s.store.GetBlockHeight(),
		Params:      s.store.GetParams(),
		LatestEpoch: epoch,
	}, nil
}

func (s *InferenceServer) GetCurrentEpoch(_ context.Context, _ *inferencetypes.QueryGetCurrentEpochRequest) (*inferencetypes.QueryGetCurrentEpochResponse, error) {
	return &inferencetypes.QueryGetCurrentEpochResponse{Epoch: s.store.GetEpoch().Index}, nil
}

func (s *InferenceServer) DevshardEscrow(_ context.Context, req *inferencetypes.QueryGetDevshardEscrowRequest) (*inferencetypes.QueryGetDevshardEscrowResponse, error) {
	if s.store.EscrowQueryFaulted() {
		return nil, status.Error(codes.Unavailable, "devshard escrow query faulted (testenv)")
	}
	e := s.store.GetEscrow(req.GetId())
	if e == nil {
		return &inferencetypes.QueryGetDevshardEscrowResponse{Found: false}, nil
	}
	return &inferencetypes.QueryGetDevshardEscrowResponse{Escrow: e, Found: true}, nil
}

func (s *InferenceServer) AccountByAddress(_ context.Context, req *inferencetypes.QueryAccountByAddressRequest) (*inferencetypes.QueryAccountByAddressResponse, error) {
	pk := s.store.GetPubkey(req.GetAddress())
	if pk == "" {
		return &inferencetypes.QueryAccountByAddressResponse{}, nil
	}
	return &inferencetypes.QueryAccountByAddressResponse{Pubkey: pk}, nil
}

func (s *InferenceServer) Participant(_ context.Context, req *inferencetypes.QueryGetParticipantRequest) (*inferencetypes.QueryGetParticipantResponse, error) {
	p := s.store.GetParticipant(req.GetIndex())
	if p == nil {
		return &inferencetypes.QueryGetParticipantResponse{}, nil
	}
	return &inferencetypes.QueryGetParticipantResponse{Participant: *p}, nil
}

func (s *InferenceServer) EpochGroupData(_ context.Context, req *inferencetypes.QueryGetEpochGroupDataRequest) (*inferencetypes.QueryGetEpochGroupDataResponse, error) {
	egd := s.store.GetEpochGroupData(req.GetEpochIndex(), req.GetModelId())
	if egd == nil {
		return &inferencetypes.QueryGetEpochGroupDataResponse{}, nil
	}
	return &inferencetypes.QueryGetEpochGroupDataResponse{EpochGroupData: *egd}, nil
}

func (s *InferenceServer) GranteesByMessageType(_ context.Context, req *inferencetypes.QueryGranteesByMessageTypeRequest) (*inferencetypes.QueryGranteesByMessageTypeResponse, error) {
	grantees := s.store.GetGrantees(req.GetGranterAddress(), req.GetMessageTypeUrl())
	out := make([]*inferencetypes.Grantee, len(grantees))
	for i := range grantees {
		g := grantees[i]
		out[i] = &g
	}
	return &inferencetypes.QueryGranteesByMessageTypeResponse{Grantees: out}, nil
}

// CometServer implements cmtservice for GetLatestBlock (Phase 3a).
type CometServer struct {
	cmtservice.UnimplementedServiceServer
	store *store.Store
}

// NewCometServer returns a cmtservice server backed by store.
func NewCometServer(st *store.Store) *CometServer {
	return &CometServer{store: st}
}

func (s *CometServer) GetLatestBlock(_ context.Context, _ *cmtservice.GetLatestBlockRequest) (*cmtservice.GetLatestBlockResponse, error) {
	height := s.store.GetBlockHeight()
	return &cmtservice.GetLatestBlockResponse{
		SdkBlock: &cmtservice.Block{
			Header: cmtservice.Header{
				Height:  height,
				ChainID: s.store.GetChainID(),
			},
		},
	}, nil
}
