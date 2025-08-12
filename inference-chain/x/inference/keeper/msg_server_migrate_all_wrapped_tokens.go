package keeper

import (
	"context"
	"encoding/json"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// MigrateAllWrappedTokens migrates all known wrapped-token instances to the provided code id.
func (k msgServer) MigrateAllWrappedTokens(goCtx context.Context, req *types.MsgMigrateAllWrappedTokens) (*types.MsgMigrateAllWrappedTokensResponse, error) {
	if k.GetAuthority() != req.Authority {
		return nil, errorsmod.Wrapf(types.ErrInvalidSigner, "invalid authority; expected %s, got %s", k.GetAuthority(), req.Authority)
	}
	ctx := sdk.UnwrapSDKContext(goCtx)

	migrateMsg := json.RawMessage(req.MigrateMsgJson)
	if err := k.MigrateAllWrappedTokenContracts(ctx, req.NewCodeId, migrateMsg); err != nil {
		return nil, err
	}
	return &types.MsgMigrateAllWrappedTokensResponse{Attempted: 0}, nil
}
