package main

import (
	"context"
	"log/slog"

	"common/chain"
	chainbridge "devshard/cmd/devshardd/bridge"
	"devshard/cmd/devshardd/events"

	chaintypes "github.com/productscience/inference/x/inference/types"
)

type chainEventBridge struct {
	listener *events.Listener
	bridge   *chainbridge.ChainBridge
	phase    *chain.Phase
}

func newChainEventBridge(
	chainRPCURL string,
	chainClient *chain.Client,
	submitter chainbridge.Submitter,
) *chainEventBridge {
	phase := new(chain.Phase)
	eventListener := events.NewListener(chainRPCURL)
	br := chainbridge.NewChainBridge(chainClient, submitter)
	br.Subscribe(eventListener)
	eventListener.OnNewBlock(func(bctx context.Context, e events.NewBlockEvent) {
		qc := chainClient.InferenceQueryClient()
		// TODO: shouldn't be called for every block.
		resp, err := qc.GetCurrentEpoch(bctx, &chaintypes.QueryGetCurrentEpochRequest{})
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

func (b *chainEventBridge) Start(ctx context.Context) error {
	if err := b.listener.Start(ctx); err != nil && err != context.Canceled {
		return err
	}
	return nil
}
