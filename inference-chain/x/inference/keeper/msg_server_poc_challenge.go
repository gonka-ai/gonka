package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/keeper/pocchallenge"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) CreatePoCChallenge(goCtx context.Context, msg *types.MsgCreatePoCChallenge) (*types.MsgCreatePoCChallengeResponse, error) {
	if err := k.CheckPermission(goCtx, msg, AccountPermission); err != nil {
		return nil, err
	}
	return pocchallenge.Create(goCtx, &k.Keeper, k.PoCChallenge, msg)
}

func (k msgServer) PoCChallengeStoreCommit(goCtx context.Context, msg *types.MsgPoCChallengeStoreCommit) (*types.MsgPoCChallengeStoreCommitResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}
	return pocchallenge.StoreCommit(goCtx, &k.Keeper, k.PoCChallenge, msg)
}

func (k msgServer) SubmitPoCChallengeValidations(goCtx context.Context, msg *types.MsgSubmitPoCChallengeValidations) (*types.MsgSubmitPoCChallengeValidationsResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ParticipantPermission); err != nil {
		return nil, err
	}
	return pocchallenge.SubmitValidations(goCtx, &k.Keeper, k.PoCChallenge, msg)
}
