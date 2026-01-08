package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

func (k Keeper) IsParticipantAllowlistActive(ctx context.Context, height int64) bool {
	p := k.GetParticipantAccessParams(ctx)
	if p == nil || !p.UseParticipantAllowlist {
		return false
	}
	until := p.ParticipantAllowlistUntilBlockHeight
	if until == 0 {
		return true
	}
	return height < until
}

func (k Keeper) IsAllowlistedParticipant(ctx context.Context, address string) (bool, error) {
	addr, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		return false, err
	}
	return k.ParticipantAllowListSet.Has(ctx, addr)
}

func (k Keeper) IsParticipantAllowed(ctx context.Context, height int64, address string) bool {
	if !k.IsParticipantAllowlistActive(ctx, height) {
		return true
	}
	allowed, err := k.IsAllowlistedParticipant(ctx, address)
	if err != nil {
		return false
	}
	return allowed
}

