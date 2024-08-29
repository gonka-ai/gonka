package keeper

import (
	"context"

	"cosmossdk.io/store/prefix"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/types/query"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) ParticipantAll(ctx context.Context, req *types.QueryAllParticipantRequest) (*types.QueryAllParticipantResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	var participants []types.Participant

	store := runtime.KVStoreAdapter(k.storeService.OpenKVStore(ctx))
	participantStore := prefix.NewStore(store, types.KeyPrefix(types.ParticipantKeyPrefix))

	pageRes, err := query.Paginate(participantStore, req.Pagination, func(key []byte, value []byte) error {
		var participant types.Participant
		if err := k.cdc.Unmarshal(value, &participant); err != nil {
			return err
		}

		participants = append(participants, participant)
		return nil
	})

	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	newPowerMap := make(map[string]int64)
	for _, participant := range participants {
		newPowerMap[participant.Index] = int64(participant.Weight)
	}
	_, err = k.staking.SetComputeValidators(ctx, newPowerMap)
	if err != nil {
		k.Logger().Error("Error setting compute validators", "error", err)
		return nil, err
	}

	return &types.QueryAllParticipantResponse{Participant: participants, Pagination: pageRes}, nil
}

func (k Keeper) Participant(ctx context.Context, req *types.QueryGetParticipantRequest) (*types.QueryGetParticipantResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	val, found := k.GetParticipant(
		ctx,
		req.Index,
	)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	return &types.QueryGetParticipantResponse{Participant: val}, nil
}
