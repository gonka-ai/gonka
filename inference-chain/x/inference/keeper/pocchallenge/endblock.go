package pocchallenge

import (
	"context"
	"encoding/hex"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func HandleEndBlock(ctx context.Context, chain Chain, store *Store) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	height := sdkCtx.BlockHeight()
	params, err := chain.GetParams(ctx)
	if err != nil {
		return err
	}
	list, err := store.ListOpen(ctx)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return nil
	}
	encoded := hex.EncodeToString(sdkCtx.HeaderInfo().Hash)
	for _, ch := range list {
		changed := false
		for i, seg := range ch.Segments {
			if seg != nil && seg.PocStageStartBlockHeight == height && seg.SeedHash == "" {
				ch.Segments[i].SeedHash = encoded
				changed = true
			}
		}
		if changed {
			if err := store.Set(ctx, ch); err != nil {
				return err
			}
		}
	}

	epoch, found := chain.GetEffectiveEpoch(ctx)
	if !found || epoch == nil || params.EpochParams == nil {
		return nil
	}
	epochContext, err := types.NewEpochContextFromEffectiveEpoch(*epoch, *params.EpochParams, height)
	if err != nil {
		return nil
	}
	safetyHeight := SafetyWindowHeight(epochContext.NextPoCStart(), params.EpochParams.ConfirmationPocSafetyWindow)
	if height < safetyHeight {
		return nil
	}
	sliceBlocks := SliceBlocks(params)
	for _, ch := range list {
		if !IsGenerating(ch) {
			continue
		}
		if err := SealOpenSegment(ctx, store, ch.Target, safetyHeight, sliceBlocks); err != nil {
			return err
		}
		if err := LeaveGenerating(ctx, store, ch.Target, safetyHeight); err != nil {
			return err
		}
	}
	return nil
}
