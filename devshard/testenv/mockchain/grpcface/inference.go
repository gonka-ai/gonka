package grpcface

import (
	"context"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"devshard/testenv/mockchain/rpcface"
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
	if s.store.ParticipantQueryFaulted() {
		return nil, status.Error(codes.Unavailable, "participant query faulted (testenv)")
	}
	p := s.store.GetParticipant(req.GetIndex())
	if p == nil {
		return nil, status.Error(codes.NotFound, "not found")
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

func (s *InferenceServer) WarmKeyLog(_ context.Context, req *inferencetypes.QueryWarmKeyLogRequest) (*inferencetypes.QueryWarmKeyLogResponse, error) {
	if req.GetGranter() == "" {
		return nil, status.Error(codes.InvalidArgument, "invalid granter address")
	}
	return &inferencetypes.QueryWarmKeyLogResponse{Entries: s.store.GetWarmKeyLog(req.GetGranter())}, nil
}

func (s *InferenceServer) WarmKeyEntry(_ context.Context, req *inferencetypes.QueryWarmKeyEntryRequest) (*inferencetypes.QueryWarmKeyEntryResponse, error) {
	if req.GetGranter() == "" || req.GetId() == 0 {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	entry := s.store.GetWarmKeyEntry(req.GetGranter(), req.GetId())
	if entry == nil {
		return nil, status.Error(codes.NotFound, "warm-key entry not found")
	}
	return &inferencetypes.QueryWarmKeyEntryResponse{Entry: entry}, nil
}

// CometServer implements cmtservice for GetLatestBlock (Phase 3a).
type CometServer struct {
	cmtservice.UnimplementedServiceServer
	store *store.Store
	rpc   *rpcface.Service
}

// NewCometServer returns a cmtservice server backed by store.
// rpc, when set, fills the block hash and time from the signed headers the
// JSON-RPC face publishes, so a gRPC reader and the header poller share one stamp.
func NewCometServer(st *store.Store, rpc *rpcface.Service) *CometServer {
	return &CometServer{store: st, rpc: rpc}
}

func (s *CometServer) GetLatestBlock(_ context.Context, _ *cmtservice.GetLatestBlockRequest) (*cmtservice.GetLatestBlockResponse, error) {
	return s.blockAt(s.store.GetBlockHeight()), nil
}

func (s *CometServer) GetBlockByHeight(_ context.Context, req *cmtservice.GetBlockByHeightRequest) (*cmtservice.GetBlockByHeightResponse, error) {
	height := int64(0)
	if req != nil {
		height = req.Height
	}
	if height <= 0 {
		height = s.store.GetBlockHeight()
	}
	got := s.blockAt(height)
	return &cmtservice.GetBlockByHeightResponse{BlockId: got.BlockId, SdkBlock: got.SdkBlock}, nil
}

func (s *CometServer) blockAt(height int64) *cmtservice.GetLatestBlockResponse {
	hdr := cmtservice.Header{Height: height, ChainID: s.store.GetChainID()}
	var id *cmtproto.BlockID
	if s.rpc != nil && height > 0 {
		hash, at, err := s.rpc.BlockStamp(height)
		if err == nil && len(hash) > 0 {
			hdr.Time = at
			id = &cmtproto.BlockID{Hash: hash}
		}
	}
	return &cmtservice.GetLatestBlockResponse{
		BlockId:  id,
		SdkBlock: &cmtservice.Block{Header: hdr},
	}
}
