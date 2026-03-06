package keeper

import (
	"context"
	"encoding/json"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// MigrateAllWrappedTokens migrates all known wrapped-token instances to the provided code id.
func (k msgServer) MigrateAllWrappedTokens(goCtx context.Context, req *types.MsgMigrateAllWrappedTokens) (*types.MsgMigrateAllWrappedTokensResponse, error) {
	if err := k.CheckPermission(goCtx, req, GovernancePermission); err != nil {
		return nil, err
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	migrateMsg := json.RawMessage(req.MigrateMsgJson)
	if err := k.MigrateAllWrappedTokenContracts(ctx, req.NewCodeId, migrateMsg); err != nil {
		return nil, err
	}
	return &types.MsgMigrateAllWrappedTokensResponse{Attempted: 0}, nil
}
