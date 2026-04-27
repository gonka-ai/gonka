package main

import (
	"context"
	"log/slog"

	"common/chain"
	chainbridge "devshard/cmd/devshardd/bridge"
	"devshard/cmd/devshardd/events"

	cmtservice "github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	chaintypes "github.com/productscience/inference/x/inference/types"
)

type chainEventBridge struct {
	listener *events.Listener
	bridge   *chainbridge.ChainBridge
	phase    *chain.Phase
}

// bootstrapPhase fetches the current epoch and latest block height from the
// chain and seeds the phase before the event listener starts ticking. This
// ensures phase.EpochID() is correct from the first inference request.
func bootstrapPhase(ctx context.Context, chainClient *chain.Client, phase *chain.Phase) {
	epochResp, err := chainClient.InferenceQueryClient().GetCurrentEpoch(ctx, &chaintypes.QueryGetCurrentEpochRequest{})
	if err != nil {
		slog.Warn("phase: failed to bootstrap epoch, starting at 0", "error", err)
		return
	}

	blockResp, err := chainClient.CometServiceClient().GetLatestBlock(ctx, &cmtservice.GetLatestBlockRequest{})
	if err != nil {
		slog.Warn("phase: failed to bootstrap block height, starting at 0", "error", err)
		phase.Update(epochResp.Epoch, 0)
		return
	}

	phase.Update(epochResp.Epoch, blockResp.SdkBlock.Header.Height)
}

func newChainEventBridge(
	ctx context.Context,
	chainRPCURL string,
	chainClient *chain.Client,
	submitter chainbridge.Submitter,
) *chainEventBridge {
	phase := new(chain.Phase)
	bootstrapPhase(ctx, chainClient, phase)
	eventListener := events.NewListener(chainRPCURL)
	br := chainbridge.NewChainBridge(chainClient, submitter)
	br.Subscribe(eventListener)
	eventListener.OnNewBlock(func(bctx context.Context, e events.NewBlockEvent) {
		// TODO: should this be called for every block?
		resp, err := chainClient.InferenceQueryClient().GetCurrentEpoch(bctx, &chaintypes.QueryGetCurrentEpochRequest{})
		if err != nil {
			slog.Warn("phase: failed to query current epoch", "block", e.BlockHeight, "error", err)
			phase.SetBlockHeight(e.BlockHeight)
			return
		}
		phase.Update(resp.Epoch, e.BlockHeight)
	})
	return &chainEventBridge{
		listener: eventListener,
		bridge:   br,
		phase:    phase,
	}
}

func (b *chainEventBridge) Bridge() *chainbridge.ChainBridge {
	return b.bridge
}

func (b *chainEventBridge) Phase() *chain.Phase {
	return b.phase
}

// OnNewBlock registers an additional new-block handler on the underlying listener.
// Must be called before Start.
func (b *chainEventBridge) OnNewBlock(h events.NewBlockHandler) {
	b.listener.OnNewBlock(h)
}

func (b *chainEventBridge) Start(ctx context.Context) error {
	if err := b.listener.Start(ctx); err != nil && err != context.Canceled {
		return err
	}
	return nil
}
