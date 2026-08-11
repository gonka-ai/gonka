package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	authztypes "github.com/cosmos/cosmos-sdk/x/authz"
)

// HasGrantForMsg reports whether grantee may execute msgTypeURL on behalf of
// granter under x/authz.
//
// It mirrors the acceptance rule in authz keeper.DispatchActions: a
// self-execution (granter == grantee) is implicitly accepted, otherwise a grant
// for that exact message type must exist and must not have expired relative to
// the current block time.
//
// Used by the ante layer to authenticate the actor named in a fee-exempt duty
// message that arrived wrapped in MsgExec. There the tx signature only proves
// the grantee signed, so without this check any account could name a registered
// participant as Creator and inherit its authorization (#1539).
//
// This intentionally does not call Authorization.Accept: message-level accept
// logic can mutate grant state, which must not happen during CheckTx. Existence
// plus expiry is the strongest check available without side effects, and
// DeliverTx still runs the full authz dispatch.
func (k Keeper) HasGrantForMsg(ctx sdk.Context, granter, grantee, msgTypeURL string) bool {
	if granter == "" || grantee == "" {
		return false
	}
	if granter == grantee {
		return true
	}
	resp, err := k.AuthzKeeper.Grants(ctx, &authztypes.QueryGrantsRequest{
		Granter:    granter,
		Grantee:    grantee,
		MsgTypeUrl: msgTypeURL,
	})
	if err != nil || resp == nil {
		return false
	}
	blockTime := ctx.BlockTime()
	for _, g := range resp.Grants {
		if g == nil {
			continue
		}
		if g.Expiration != nil && g.Expiration.Before(blockTime) {
			continue
		}
		return true
	}
	return false
}
