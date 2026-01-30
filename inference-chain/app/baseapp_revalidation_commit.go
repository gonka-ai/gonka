package app

import (
	"context"

	"github.com/cosmos/cosmos-sdk/baseapp"
	sdk "github.com/cosmos/cosmos-sdk/types"
	inferencemodulekeeper "github.com/productscience/inference/x/inference/keeper"
)

// RevalidationCommitOption returns a BaseAppOption that wires events hooks
// into the Commit phase via the SDK's Precommiter hook.
// When this option is used, events for the committed block are processed
// as soon as the block is finalized (during Commit)
func RevalidationCommitOption(keeper *inferencemodulekeeper.Keeper) func(*baseapp.BaseApp) {
	return func(bapp *baseapp.BaseApp) {
		bapp.SetPrecommiter(func(ctx sdk.Context) {
			height := ctx.BlockHeight()
			hash := ctx.HeaderInfo().Hash
			//TODO: generalize events, process not only RevalidationEvents
			keeper.ProcessPendingRevalidationEvents(context.Background(), height, hash)
		})
	}
}
