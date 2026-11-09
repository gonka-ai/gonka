package v0_2_18

import (
	"context"
	"errors"
	"fmt"

	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
)

// ConsensusParamsStore is the subset of x/consensus Keeper.ParamsStore
// (collections.Item[cmtproto.ConsensusParams]) used here.
type ConsensusParamsStore interface {
	Get(ctx context.Context) (cmtproto.ConsensusParams, error)
	Set(ctx context.Context, params cmtproto.ConsensusParams) error
}

// EnableVoteExtensions schedules CometBFT vote extensions at
// currentHeight + delay by writing Abci.VoteExtensionsEnableHeight into the
// x/consensus params store. The SDK returns the stored params to CometBFT in
// this block's FinalizeBlock response, so they take effect from the next height.
//
// If an enable height is already set (e.g. a network that enabled vote
// extensions at genesis) the params are left untouched and changed=false:
// CometBFT forbids modifying the height once reached, and an upgrade handler
// error would halt the chain.
//
// The update is validated with CometBFT's own rules before it is stored, so an
// invalid value fails the upgrade here instead of halting the node when
// CometBFT applies the FinalizeBlock response.
func EnableVoteExtensions(ctx context.Context, store ConsensusParamsStore, delay int64) (enableHeight int64, changed bool, err error) {
	if delay < 1 {
		return 0, false, fmt.Errorf("vote extensions enable delay must be >= 1, got %d", delay)
	}
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()

	current, err := store.Get(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("read consensus params: %w", err)
	}
	if current.Abci != nil && current.Abci.VoteExtensionsEnableHeight > 0 {
		return current.Abci.VoteExtensionsEnableHeight, false, nil
	}

	currentCmt, err := toCmtConsensusParams(current)
	if err != nil {
		return 0, false, err
	}

	target := height + delay
	updated := current // shallow copy; only the Abci pointer is replaced
	updated.Abci = &cmtproto.ABCIParams{VoteExtensionsEnableHeight: target}

	if err := currentCmt.ValidateUpdate(&updated, height); err != nil {
		return 0, false, fmt.Errorf("invalid vote extensions enable height %d at height %d: %w", target, height, err)
	}
	updatedCmt, err := toCmtConsensusParams(updated)
	if err != nil {
		return 0, false, err
	}
	if err := updatedCmt.ValidateBasic(); err != nil {
		return 0, false, fmt.Errorf("updated consensus params invalid: %w", err)
	}

	if err := store.Set(ctx, updated); err != nil {
		return 0, false, fmt.Errorf("write consensus params: %w", err)
	}
	return target, true, nil
}

// toCmtConsensusParams is cmttypes.ConsensusParamsFromProto without the nil
// dereference panics. Block, Evidence and Validator are required (CometBFT
// always sets them); a missing Version means app version 0.
func toCmtConsensusParams(p cmtproto.ConsensusParams) (cmttypes.ConsensusParams, error) {
	if p.Block == nil || p.Evidence == nil || p.Validator == nil {
		return cmttypes.ConsensusParams{}, errors.New("consensus params missing block, evidence or validator section")
	}
	if p.Version == nil {
		p.Version = &cmtproto.VersionParams{}
	}
	return cmttypes.ConsensusParamsFromProto(p), nil
}
