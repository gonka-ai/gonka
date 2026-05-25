package keeper

import (
	"context"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/query"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) TeeAttestation(ctx context.Context, req *types.QueryTeeAttestationRequest) (*types.QueryTeeAttestationResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	if req.ParticipantAddress == "" || req.NodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "participant_address and node_id are required")
	}
	addr, err := sdk.AccAddressFromBech32(req.ParticipantAddress)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid participant_address: %v", err)
	}
	att, found, err := k.GetTeeAttestation(ctx, addr, req.NodeId)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if !found {
		return &types.QueryTeeAttestationResponse{Found: false}, nil
	}
	return &types.QueryTeeAttestationResponse{Attestation: &att, Found: true}, nil
}

func (k Keeper) TeeAttestationsByParticipant(ctx context.Context, req *types.QueryTeeAttestationsByParticipantRequest) (*types.QueryTeeAttestationsByParticipantResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	addr, err := sdk.AccAddressFromBech32(req.ParticipantAddress)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid participant_address: %v", err)
	}
	atts, err := k.ListTeeAttestationsByParticipant(ctx, addr)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	out := make([]*types.TeeAttestation, len(atts))
	for i := range atts {
		v := atts[i]
		out[i] = &v
	}
	return &types.QueryTeeAttestationsByParticipantResponse{Attestations: out}, nil
}

func (k Keeper) TeeAttestationsActive(ctx context.Context, req *types.QueryTeeAttestationsActiveRequest) (*types.QueryTeeAttestationsActiveResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	atts, pageRes, height, err := k.pageActiveAttestations(ctx, req.Pagination, req.Provider, false)
	if err != nil {
		return nil, err
	}
	return &types.QueryTeeAttestationsActiveResponse{Attestations: atts, Pagination: pageRes, BlockHeight: height}, nil
}

func (k Keeper) TeeAttestationsVerified(ctx context.Context, req *types.QueryTeeAttestationsVerifiedRequest) (*types.QueryTeeAttestationsVerifiedResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	atts, pageRes, height, err := k.pageActiveAttestations(ctx, req.Pagination, req.Provider, true)
	if err != nil {
		return nil, err
	}
	return &types.QueryTeeAttestationsVerifiedResponse{Attestations: atts, Pagination: pageRes, BlockHeight: height}, nil
}

func (k Keeper) pageActiveAttestations(
	ctx context.Context,
	page *query.PageRequest,
	provider string,
	verifiedOnly bool,
) ([]*types.TeeAttestation, *query.PageResponse, int64, error) {
	currentHeight := sdk.UnwrapSDKContext(ctx).BlockHeight()
	atts, pageRes, err := query.CollectionFilteredPaginate(
		ctx,
		k.TeeAttestations,
		page,
		func(_ collections.Pair[sdk.AccAddress, string], v types.TeeAttestation) (bool, error) {
			if v.ExpiresAtHeight <= currentHeight {
				return false, nil
			}
			if provider != "" && v.Provider != provider {
				return false, nil
			}
			if verifiedOnly && v.VerificationStatus != types.TeeVerificationStatus_TEE_VERIFICATION_VERIFIED {
				return false, nil
			}
			return true, nil
		},
		func(_ collections.Pair[sdk.AccAddress, string], v types.TeeAttestation) (*types.TeeAttestation, error) {
			return &v, nil
		},
	)
	if err != nil {
		return nil, nil, 0, status.Error(codes.Internal, err.Error())
	}
	return atts, pageRes, currentHeight, nil
}

func (k Keeper) PendingTeeAttestationsForStage(ctx context.Context, req *types.QueryPendingTeeAttestationsForStageRequest) (*types.QueryPendingTeeAttestationsForStageResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	if req.PocStageStartBlockHeight <= 0 {
		return nil, status.Error(codes.InvalidArgument, "poc_stage_start_block_height must be > 0")
	}
	currentHeight := sdk.UnwrapSDKContext(ctx).BlockHeight()

	rng := collections.NewPrefixedTripleRange[int64, sdk.AccAddress, string](req.PocStageStartBlockHeight)
	it, err := k.TeePendingAttestationsByStage.Iterate(ctx, rng)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	defer it.Close()
	keys, err := it.Keys()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	out := make([]*types.TeeAttestation, 0, len(keys))
	for _, key := range keys {
		subject := key.K2()
		nodeID := key.K3()
		att, found, err := k.GetTeeAttestation(ctx, subject, nodeID)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		if !found {
			continue
		}
		if att.AttestedAtHeight != req.PocStageStartBlockHeight ||
			att.VerificationStatus != types.TeeVerificationStatus_TEE_VERIFICATION_PENDING ||
			att.ExpiresAtHeight <= currentHeight {
			continue
		}
		attCopy := att
		out = append(out, &attCopy)
	}
	return &types.QueryPendingTeeAttestationsForStageResponse{
		Attestations: out,
		BlockHeight:  currentHeight,
	}, nil
}
